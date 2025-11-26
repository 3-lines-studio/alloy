package alloy

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buke/quickjs-go"
	"github.com/evanw/esbuild/pkg/api"
)

// RenderResult contains SSR HTML and optional client bundle for hydration
type RenderResult struct {
	HTML        string
	ClientJS    string
	CSS         string
	Props       map[string]any
	ClientPath  string
	ClientPaths []string
	CSSPath     string
	SharedCSS   string
}

type ClientAssets struct {
	Entry  string
	Chunks []string
}

type ClientEntry struct {
	Name      string
	Component string
	RootID    string
}

// PrebuiltFiles holds paths to prebuilt assets on disk.
type PrebuiltFiles struct {
	Server       string
	Client       string
	ClientChunks []string
	CSS          string
	SharedCSS    string
}

type RenderOutcome string

const (
	RenderOutcomeSuccess RenderOutcome = "success"
	RenderOutcomeError   RenderOutcome = "error"
)

type MetricsRecorder interface {
	RecordRender(component string, duration time.Duration, outcome RenderOutcome, propsBytes int)
}

type noopMetrics struct{}

func (noopMetrics) RecordRender(string, time.Duration, RenderOutcome, int) {}

var metricsRecorder atomic.Value
var renderTimeout atomic.Value

func init() {
	metricsRecorder.Store(MetricsRecorder(noopMetrics{}))
	renderTimeout.Store(2 * time.Second)
	jsPool = newRuntimePool(defaultRuntimePoolSize())
}

// SetMetricsRecorder configures a process-wide recorder; pass nil to reset to no-op.
func SetMetricsRecorder(recorder MetricsRecorder) {
	if recorder == nil {
		metricsRecorder.Store(MetricsRecorder(noopMetrics{}))
		return
	}
	metricsRecorder.Store(recorder)
}

// SetRenderTimeout sets a per-render timeout; zero disables the timeout.
func SetRenderTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	renderTimeout.Store(d)
}

// SetRuntimePoolSize adjusts the max pooled runtimes; minimum is 1.
func SetRuntimePoolSize(size int) {
	if size < 1 {
		size = 1
	}
	jsPool.setSize(size)
}

type jsRuntime struct {
	rt  *quickjs.Runtime
	ctx *quickjs.Context
}

type runtimePool struct {
	mu   sync.Mutex
	sem  chan struct{}
	size int
}

var jsPool *runtimePool

func newRuntimePool(size int) *runtimePool {
	if size < 1 {
		size = 1
	}
	return &runtimePool{
		sem:  make(chan struct{}, size),
		size: size,
	}
}

func defaultRuntimePoolSize() int {
	size := runtime.GOMAXPROCS(0) * 2
	if size < 1 {
		return 1
	}
	return size
}

func (p *runtimePool) setSize(size int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if size < 1 {
		size = 1
	}

	if size == p.size {
		return
	}

	p.sem = make(chan struct{}, size)
	p.size = size
}

func (p *runtimePool) acquire(ctx context.Context) (*jsRuntime, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	vm, err := newRuntimeWithContext()
	if err != nil {
		select {
		case <-p.sem:
		default:
		}
		return nil, err
	}

	return vm, nil
}

func (p *runtimePool) release(vm *jsRuntime) {
	if vm == nil {
		return
	}

	closeRuntime(vm)

	select {
	case <-p.sem:
	default:
	}
}

func (p *runtimePool) discard(vm *jsRuntime) {
	closeRuntime(vm)
	select {
	case <-p.sem:
	default:
	}
}

func newRuntimeWithContext() (*jsRuntime, error) {
	rt := quickjs.NewRuntime()
	rt.SetMaxStackSize(4 * 1024 * 1024)

	ctx := rt.NewContext()
	if err := loadPolyfills(ctx); err != nil {
		ctx.Close()
		rt.Close()
		return nil, err
	}

	return &jsRuntime{
		rt:  rt,
		ctx: ctx,
	}, nil
}

func closeRuntime(vm *jsRuntime) {
	if vm == nil || vm.rt == nil {
		return
	}

	if vm.ctx != nil {
		vm.ctx.Close()
	}
	vm.rt.Close()
}

func currentRenderTimeout() time.Duration {
	value := renderTimeout.Load()
	if value == nil {
		return 0
	}
	timeout, ok := value.(time.Duration)
	if !ok {
		return 0
	}
	return timeout
}

const polyfillsSource = `
		var globalThis = this;
		var window = this;
		var self = this;
		var process = { env: { NODE_ENV: 'production' } };
		var console = console || { log: function(){}, warn: function(){}, error: function(){}, info: function(){}, debug: function(){} };
		var performance = performance || { now: function() { return Date.now(); } };

		function TextEncoder() {}
		TextEncoder.prototype.encode = function(str) {
			var arr = [];
			for (var i = 0; i < str.length; i++) {
				var c = str.charCodeAt(i);
				if (c < 128) arr.push(c);
				else if (c < 2048) { arr.push(192 | (c >> 6)); arr.push(128 | (c & 63)); }
				else { arr.push(224 | (c >> 12)); arr.push(128 | ((c >> 6) & 63)); arr.push(128 | (c & 63)); }
			}
			return new Uint8Array(arr);
		};

		function TextDecoder() {}
		TextDecoder.prototype.decode = function(arr) {
			var str = '';
			for (var i = 0; i < arr.length; i++) str += String.fromCharCode(arr[i]);
			return str;
		};
	`

func loadPolyfills(ctx *quickjs.Context) error {
	if ctx == nil {
		return fmt.Errorf("context required")
	}

	result := ctx.Eval(polyfillsSource)
	if result.IsException() {
		return fmt.Errorf("polyfills: %s", ctx.Exception().Error())
	}
	result.Free()
	return nil
}

// Page describes a routed component.
type Page struct {
	Route     string
	Component string
	RootID    string
	Files     PrebuiltFiles
	DistDir   string
	Name      string
	Props     func(r *http.Request) map[string]any
	Ctx       func(r *http.Request) context.Context
}

// ServeLiveEvents streams keepalive messages for dev reload.
func ServeLiveEvents(w http.ResponseWriter, r *http.Request) {
	if !isDevMode() {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	fmt.Fprintf(w, "data: ok\n\n")
	flusher.Flush()

	for {
		select {
		case <-liveReloadCh:
			fmt.Fprintf(w, "data: reload\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		case t := <-ticker.C:
			fmt.Fprintf(w, "data: %d\n\n", t.Unix())
			flusher.Flush()
		}
	}
}

// ServePage renders a TSX component and writes an HTML document.
func ServePage(w http.ResponseWriter, r *http.Request, componentPath string, props map[string]any, rootID string) {
	result, err := RenderTSXFileWithHydrationWithContext(r.Context(), componentPath, props, rootID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, result.ToHTML(rootID))
}

// ServePageWithContext is like ServePage but uses a provided context for SSR timeouts.
func ServePageWithContext(ctx context.Context, w http.ResponseWriter, r *http.Request, componentPath string, props map[string]any, rootID string) {
	if ctx == nil {
		ctx = r.Context()
	}
	result, err := RenderTSXFileWithHydrationWithContext(ctx, componentPath, props, rootID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, result.ToHTML(rootID))
}

// ServePrebuiltPage renders using prebuilt assets referenced by file paths.
func ServePrebuiltPage(w http.ResponseWriter, r *http.Request, componentPath string, props map[string]any, rootID string, files PrebuiltFiles) {
	result, err := RenderPrebuiltWithContext(r.Context(), componentPath, props, rootID, files)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, result.ToHTML(rootID))
}

// RegisterPrebuiltBundle seeds the cache with prebuilt assets for production.
func RegisterPrebuiltBundle(componentPath string, rootID string, serverJS string, clientJS string, css string) error {
	if componentPath == "" || rootID == "" {
		return fmt.Errorf("component path and root id required")
	}
	if serverJS == "" || clientJS == "" || css == "" {
		return fmt.Errorf("prebuilt assets cannot be empty")
	}

	absPath, err := filepath.Abs(componentPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	bundleCache.Lock()
	entry := bundleCache.entries[absPath]

	if entry == nil || entryStale(entry) {
		entry = &bundleCacheEntry{
			serverJS:   serverJS,
			clientByID: map[string]string{rootID: clientJS},
			css:        css,
			prebuilt:   true,
		}
		bundleCache.entries[absPath] = entry
		bundleCache.Unlock()
		return nil
	}

	entry.serverJS = serverJS
	entry.css = css
	entry.clientByID[rootID] = clientJS
	entry.prebuilt = true
	entry.deps = nil
	bundleCache.Unlock()

	return nil
}

type bundleCacheEntry struct {
	serverJS   string
	clientByID map[string]string
	css        string
	deps       map[string]fileStamp
	prebuilt   bool
}

var bundleCache = struct {
	sync.Mutex
	entries map[string]*bundleCacheEntry
}{
	entries: make(map[string]*bundleCacheEntry),
}

var liveReloadCh = make(chan struct{}, 1)

func triggerLiveReload() {
	select {
	case liveReloadCh <- struct{}{}:
	default:
	}
}

// ToHTML returns complete HTML document with hydration script
func (r *RenderResult) ToHTML(rootID string) string {
	if r.ClientJS == "" && r.ClientPath == "" && len(r.ClientPaths) == 0 {
		return r.HTML
	}

	propsJSON, _ := json.Marshal(r.Props)

	meta := metaFromProps(r.Props)
	if meta.Title == "" {
		meta.Title = "Page"
	}
	if meta.OGType == "" {
		meta.OGType = "website"
	}

	head := buildHead(meta)
	cssTag := ""
	if r.SharedCSS != "" {
		cssTag += fmt.Sprintf("\n\t<link rel=\"stylesheet\" href=\"%s\" />", r.SharedCSS)
	}
	switch {
	case r.CSSPath != "":
		cssTag += fmt.Sprintf("\n\t<link rel=\"stylesheet\" href=\"%s\" />", r.CSSPath)
	case r.CSS != "":
		cssTag += fmt.Sprintf("\n\t<style>%s</style>", r.CSS)
	}

	liveReload := ""
	if isDevMode() {
		liveReload = `
	<script>(function(){if(typeof EventSource==="undefined"){return;}var es=new EventSource("/__live");es.onmessage=function(ev){if(ev.data==="reload"){window.location.reload();}};es.onerror=function(){es.close();var check=function(){fetch(window.location.href,{cache:"no-store"}).then(function(){window.location.reload();}).catch(function(){setTimeout(check,400);});};setTimeout(check,400);};})();</script>`
	}

	scriptTag := ""
	switch {
	case len(r.ClientPaths) > 0:
		var b strings.Builder
		for _, p := range r.ClientPaths {
			fmt.Fprintf(&b, "<script type=\"module\" src=\"%s\"></script>\n", p)
		}
		scriptTag = strings.TrimSuffix(b.String(), "\n")
	case r.ClientPath != "":
		scriptTag = fmt.Sprintf(`<script src="%s"></script>`, r.ClientPath)
	case r.ClientJS != "":
		scriptTag = fmt.Sprintf(`<script>%s</script>`, r.ClientJS)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
%s%s
</head>
<body>
	<div id="%s">%s</div>
	<script id="%s-props" type="application/json">%s</script>
	%s
</body>%s
</html>`, head, cssTag, rootID, r.HTML, rootID, string(propsJSON), scriptTag, liveReload)
}

type pageMeta struct {
	Title       string
	Description string
	URL         string
	Canonical   string
	Image       string
	OGType      string
}

func buildHead(meta pageMeta) string {
	var b strings.Builder
	b.WriteString("\t<meta charset=\"UTF-8\">")
	b.WriteString("\n\t<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">")
	b.WriteString(fmt.Sprintf("\n\t<title>%s</title>", html.EscapeString(meta.Title)))

	if meta.Description != "" {
		escaped := html.EscapeString(meta.Description)
		fmt.Fprintf(&b, "\n\t<meta name=\"description\" content=\"%s\">", escaped)
		fmt.Fprintf(&b, "\n\t<meta property=\"og:description\" content=\"%s\">", escaped)
	}

	fmt.Fprintf(&b, "\n\t<meta property=\"og:title\" content=\"%s\">", html.EscapeString(meta.Title))

	url := meta.URL
	if url == "" {
		url = meta.Canonical
	}
	if url != "" {
		escaped := html.EscapeString(url)
		fmt.Fprintf(&b, "\n\t<link rel=\"canonical\" href=\"%s\">", escaped)
		fmt.Fprintf(&b, "\n\t<meta property=\"og:url\" content=\"%s\">", escaped)
	}

	if meta.Image != "" {
		fmt.Fprintf(&b, "\n\t<meta property=\"og:image\" content=\"%s\">", html.EscapeString(meta.Image))
	}

	fmt.Fprintf(&b, "\n\t<meta property=\"og:type\" content=\"%s\">", html.EscapeString(meta.OGType))

	return b.String()
}

func metaFromProps(props map[string]any) pageMeta {
	meta := pageMeta{
		Title:       stringProp(props, "title"),
		Description: stringProp(props, "description"),
		URL:         stringProp(props, "url"),
		Canonical:   stringProp(props, "canonical"),
		Image:       stringProp(props, "image"),
		OGType:      stringProp(props, "ogType"),
	}

	raw, ok := props["meta"].(map[string]any)
	if !ok || len(raw) == 0 {
		return meta
	}

	if v := stringFromMap(raw, "title"); v != "" {
		meta.Title = v
	}
	if v := stringFromMap(raw, "description"); v != "" {
		meta.Description = v
	}
	if v := stringFromMap(raw, "url"); v != "" {
		meta.URL = v
	}
	if v := stringFromMap(raw, "canonical"); v != "" {
		meta.Canonical = v
	}
	if v := stringFromMap(raw, "image"); v != "" {
		meta.Image = v
	}
	if v := stringFromMap(raw, "ogType"); v != "" {
		meta.OGType = v
	}

	return meta
}

func stringFromMap(m map[string]any, key string) string {
	if len(m) == 0 {
		return ""
	}
	val, ok := m[key]
	if !ok {
		return ""
	}
	str, ok := val.(string)
	if !ok {
		return ""
	}
	return str
}

func stringProp(props map[string]any, key string) string {
	if len(props) == 0 {
		return ""
	}
	return stringFromMap(props, key)
}

// RenderTSXFileWithHydration renders with client-side hydration support
func RenderTSXFileWithHydration(filePath string, props map[string]any, rootID string) (*RenderResult, error) {
	return RenderTSXFileWithHydrationWithContext(context.Background(), filePath, props, rootID)
}

// RenderTSXFileWithHydrationWithContext renders with hydration using a custom context.
func RenderTSXFileWithHydrationWithContext(ctx context.Context, filePath string, props map[string]any, rootID string) (*RenderResult, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	if _, err := os.Stat(absPath); err != nil {
		return nil, fmt.Errorf("stat component: %w", err)
	}

	dir := filepath.Dir(absPath)
	cssPath := filepath.Join(dir, "app.css")
	if _, err := os.Stat(cssPath); err != nil {
		return nil, fmt.Errorf("missing css file: %w", err)
	}

	devMode := isDevMode()
	serverJS, clientJS, css := readBundlesFromCache(absPath, rootID)
	if serverJS == "" || clientJS == "" || css == "" {
		if !devMode {
			return nil, fmt.Errorf("bundle not registered for %s (root %s); use RegisterPrebuiltBundle before serving in production", absPath, rootID)
		}

		var deps []string
		var buildErr error
		serverJS, clientJS, css, deps, buildErr = buildAll(absPath, rootID)
		if buildErr != nil {
			return nil, buildErr
		}

		stamps, stampErr := captureFileStamps(deps)
		if stampErr != nil {
			return nil, fmt.Errorf("capture deps: %w", stampErr)
		}

		storeBundles(absPath, rootID, serverJS, clientJS, css, stamps)
		if devMode {
			triggerLiveReload()
		}
	}

	if os.Getenv("ALLOY_DEBUG_BUNDLE") != "" {
		fmt.Println("render path server len:", len(serverJS))
	}

	propsSize := propsSizeBytes(props)

	html, err := renderWithMetrics(absPath, propsSize, func() (string, error) {
		return executeSSR(ctx, absPath, serverJS, props)
	})
	if err != nil {
		return nil, fmt.Errorf("ssr error: %w", err)
	}

	return &RenderResult{
		HTML:     html,
		ClientJS: clientJS,
		CSS:      css,
		Props:    props,
	}, nil
}

// RenderPrebuilt renders using prebuilt assets referenced by paths (for production).
func RenderPrebuilt(filePath string, props map[string]any, rootID string, files PrebuiltFiles) (*RenderResult, error) {
	return RenderPrebuiltWithContext(context.Background(), filePath, props, rootID, files)
}

// RenderPrebuiltWithContext renders using prebuilt assets with a custom context.
func RenderPrebuiltWithContext(ctx context.Context, filePath string, props map[string]any, rootID string, files PrebuiltFiles) (*RenderResult, error) {
	if files.Server == "" || files.Client == "" || files.CSS == "" {
		return nil, fmt.Errorf("prebuilt file paths required")
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	serverJS, clientJS, css := readBundlesFromCache(absPath, rootID)
	if serverJS == "" || clientJS == "" || css == "" {
		return nil, fmt.Errorf("bundle not registered for %s (root %s); use RegisterPrebuiltBundle before serving", absPath, rootID)
	}

	propsSize := propsSizeBytes(props)

	html, err := renderWithMetrics(absPath, propsSize, func() (string, error) {
		return executeSSR(ctx, absPath, serverJS, props)
	})
	if err != nil {
		return nil, fmt.Errorf("ssr error: %w", err)
	}

	paths := []string{ensureLeadingSlash(filepath.ToSlash(files.Client))}

	return &RenderResult{
		HTML:        html,
		ClientPaths: paths,
		CSSPath:     ensureLeadingSlash(filepath.ToSlash(files.CSS)),
		SharedCSS:   ensureLeadingSlash(filepath.ToSlash(files.SharedCSS)),
		Props:       props,
	}, nil
}

// BuildServerBundle builds only the server bundle for a component.
func BuildServerBundle(filePath string) (string, []string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", nil, fmt.Errorf("resolve path: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", nil, fmt.Errorf("stat component: %w", err)
	}

	serverJS, deps, err := bundleTSXFile(absPath)
	if err != nil {
		return "", nil, fmt.Errorf("server bundle error: %w", err)
	}

	return serverJS, deps, nil
}

type manifestEntry struct {
	Server string   `json:"server"`
	Client string   `json:"client,omitempty"`
	CSS    string   `json:"css"`
	Chunks []string `json:"chunks,omitempty"`
	Shared string   `json:"sharedCss,omitempty"`
}

func writeManifestEntry(dir string, name string, files *PrebuiltFiles) error {
	if files == nil {
		return fmt.Errorf("files required")
	}

	path := filepath.Join(dir, "manifest.json")

	manifest := map[string]manifestEntry{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("decode manifest: %w", err)
		}
	}

	manifest[name] = manifestEntry{
		Server: filepath.Base(files.Server),
		Client: filepath.Base(files.Client),
		CSS:    filepath.Base(files.CSS),
		Chunks: baseNames(files.ClientChunks),
		Shared: filepath.Base(files.SharedCSS),
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write manifest file: %w", err)
	}

	return nil
}

// WriteManifest writes or updates manifest.json with provided files.
func WriteManifest(dir string, name string, files PrebuiltFiles) error {
	return writeManifestEntry(dir, name, &files)
}

// SaveServerBundle writes only the server bundle.
func SaveServerBundle(serverJS string, dir string, name string) (*PrebuiltFiles, error) {
	if serverJS == "" {
		return nil, fmt.Errorf("server required")
	}
	if dir == "" || name == "" {
		return nil, fmt.Errorf("dir and name required")
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("make dir: %w", err)
	}

	serverHash := shortHash(serverJS)

	files := &PrebuiltFiles{
		Server: filepath.Join(dir, fmt.Sprintf("%s-%s-server.js", name, serverHash)),
	}

	if err := os.WriteFile(files.Server, []byte(serverJS), 0644); err != nil {
		return nil, fmt.Errorf("write server bundle: %w", err)
	}

	return files, nil
}

// SaveSharedCSS writes a shared CSS bundle to disk.
func SaveSharedCSS(css string, dir string, name string) (string, error) {
	if css == "" {
		return "", fmt.Errorf("css required")
	}
	if dir == "" {
		return "", fmt.Errorf("dir required")
	}
	if name == "" {
		name = "shared"
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("make dir: %w", err)
	}

	cssHash := shortHash(css)
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.css", name, cssHash))
	if err := os.WriteFile(path, []byte(css), 0644); err != nil {
		return "", fmt.Errorf("write shared css: %w", err)
	}

	return path, nil
}

func shortHash(content string) string {
	sum := sha1.Sum([]byte(content))
	return fmt.Sprintf("%x", sum)[:8]
}

func baseNames(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

type routeEntry struct {
	segments []routeSegment
	page     Page
	rootID   string
	files    PrebuiltFiles
}

type routeSegment struct {
	literal string
	param   string
}

type routeParamsKey struct{}

// RouteParams returns named params captured from the matched route, if any.
func RouteParams(r *http.Request) map[string]string {
	if r == nil {
		return nil
	}

	val := r.Context().Value(routeParamsKey{})
	if val == nil {
		return nil
	}

	params, ok := val.(map[string]string)
	if !ok {
		return nil
	}

	return params
}

func parseRoutePattern(pattern string) ([]routeSegment, error) {
	if pattern == "" {
		return nil, fmt.Errorf("route required")
	}

	cleaned := path.Clean(pattern)
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	if cleaned != "/" {
		cleaned = strings.TrimSuffix(cleaned, "/")
		if cleaned == "" {
			cleaned = "/"
		}
	}

	if cleaned == "/" {
		return nil, nil
	}

	parts := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	segments := make([]routeSegment, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("invalid route segment in %s", pattern)
		}
		if strings.HasPrefix(part, ":") {
			name := strings.TrimPrefix(part, ":")
			if name == "" {
				return nil, fmt.Errorf("param name required in %s", pattern)
			}
			segments = append(segments, routeSegment{param: name})
			continue
		}
		segments = append(segments, routeSegment{literal: part})
	}

	return segments, nil
}

func cleanRequestPath(p string) string {
	if p == "" {
		return "/"
	}

	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}

	if cleaned != "/" {
		cleaned = strings.TrimSuffix(cleaned, "/")
		if cleaned == "" {
			return "/"
		}
	}

	return cleaned
}

func matchRoute(segments []routeSegment, requestPath string) (map[string]string, bool) {
	cleaned := cleanRequestPath(requestPath)
	if cleaned == "/" && len(segments) == 0 {
		return nil, true
	}

	parts := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	if len(parts) != len(segments) {
		return nil, false
	}

	params := map[string]string{}
	for i, seg := range segments {
		part := parts[i]
		if seg.literal != "" {
			if part != seg.literal {
				return nil, false
			}
			continue
		}
		params[seg.param] = part
	}

	return params, true
}

// RegisterPrebuiltBundleFromFS reads assets from an fs.FS and registers them.
func RegisterPrebuiltBundleFromFS(componentPath string, rootID string, filesystem fs.FS, files PrebuiltFiles) error {
	if filesystem == nil {
		return fmt.Errorf("filesystem required")
	}
	if files.Server == "" || files.Client == "" || files.CSS == "" {
		return fmt.Errorf("prebuilt file paths required")
	}
	serverBytes, err := fs.ReadFile(filesystem, files.Server)
	if err != nil {
		return fmt.Errorf("read server bundle: %w", err)
	}
	clientBytes, err := fs.ReadFile(filesystem, files.Client)
	if err != nil {
		return fmt.Errorf("read client bundle: %w", err)
	}
	cssBytes, err := fs.ReadFile(filesystem, files.CSS)
	if err != nil {
		return fmt.Errorf("read css: %w", err)
	}

	return RegisterPrebuiltBundle(componentPath, rootID, string(serverBytes), string(clientBytes), string(cssBytes))
}

// RegisterPages registers routes and prebuilt bundles (when not in dev).
func RegisterPages(mux *http.ServeMux, filesystem fs.FS, pages []Page) error {
	if mux == nil {
		return fmt.Errorf("mux required")
	}
	if len(pages) == 0 {
		return nil
	}

	devMode := isDevMode()
	routeEntries := make([]routeEntry, 0, len(pages))

	for _, p := range pages {
		if p.Route == "" || p.Component == "" {
			return fmt.Errorf("route and component path required")
		}

		rootID := p.RootID
		if rootID == "" {
			rootID = defaultRootID(p.Component)
		}

		var prebuiltFiles PrebuiltFiles
		if !devMode {
			files, err := resolvePrebuiltFiles(filesystem, p)
			if err != nil {
				return err
			}
			if err := RegisterPrebuiltBundleFromFS(p.Component, rootID, filesystem, files); err != nil {
				return err
			}
			prebuiltFiles = files
		}

		segments, err := parseRoutePattern(p.Route)
		if err != nil {
			return err
		}

		routeEntries = append(routeEntries, routeEntry{
			segments: segments,
			page:     p,
			rootID:   rootID,
			files:    prebuiltFiles,
		})
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := cleanRequestPath(r.URL.Path)

		for _, entry := range routeEntries {
			params, ok := matchRoute(entry.segments, path)
			if !ok {
				continue
			}

			req := r
			if len(params) > 0 {
				ctx := context.WithValue(r.Context(), routeParamsKey{}, params)
				req = r.Clone(ctx)
			}

			props := map[string]any{}
			if entry.page.Props != nil {
				props = entry.page.Props(req)
			}

			ctx := req.Context()
			if entry.page.Ctx != nil {
				custom := entry.page.Ctx(req)
				if custom != nil {
					ctx = custom
				}
			}

			if entry.files.Server != "" {
				ServePrebuiltPageWithContext(ctx, w, req, entry.page.Component, props, entry.rootID, entry.files)
				return
			}

			ServePageWithContext(ctx, w, req, entry.page.Component, props, entry.rootID)
			return
		}

		http.NotFound(w, r)
	})

	return nil
}

// PagesHandler builds an http.HandlerFunc that serves the provided pages.
func PagesHandler(filesystem fs.FS, pages []Page) (http.HandlerFunc, error) {
	mux := http.NewServeMux()

	if err := RegisterPages(mux, filesystem, pages); err != nil {
		return nil, err
	}

	return mux.ServeHTTP, nil
}

// Handler builds an http.Handler with pages, live reload, and public assets.
func Handler(filesystem fs.FS, pages []Page) (http.Handler, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("filesystem required")
	}

	mux := http.NewServeMux()

	if err := RegisterPages(mux, filesystem, pages); err != nil {
		return nil, err
	}

	mux.HandleFunc("/__live", ServeLiveEvents)

	return WithPublicAssets(mux.ServeHTTP, filesystem), nil
}

// ListenAndServe starts an HTTP server with the provided pages and assets.
func ListenAndServe(addr string, filesystem fs.FS, pages []Page) error {
	handler, err := Handler(filesystem, pages)
	if err != nil {
		return err
	}

	return http.ListenAndServe(addr, handler)
}

// WithPublicAssets serves files from a "public" directory before falling back to the next handler.
func WithPublicAssets(next http.HandlerFunc, filesystem fs.FS) http.HandlerFunc {
	if next == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "next handler required", http.StatusInternalServerError)
		}
	}
	if filesystem == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "filesystem required", http.StatusInternalServerError)
		}
	}

	roots := collectAssetRoots(filesystem)
	if len(roots) == 0 {
		return next
	}

	handler := &publicAssetsHandler{
		next:  next,
		roots: roots,
	}

	return handler.ServeHTTP
}

type publicAssetsHandler struct {
	next  http.Handler
	roots []assetRoot
}

type assetRoot struct {
	prefix     string
	fs         fs.FS
	fileServer http.Handler
}

func (r assetRoot) match(assetPath string) (string, bool) {
	if r.prefix == "" {
		return assetPath, true
	}

	if assetPath == r.prefix {
		return "", true
	}

	prefix := r.prefix + "/"
	if !strings.HasPrefix(assetPath, prefix) {
		return "", false
	}

	return strings.TrimPrefix(assetPath, prefix), true
}

func (r assetRoot) assetExists(relPath string) bool {
	if r.fs == nil {
		return false
	}

	info, err := fs.Stat(r.fs, relPath)
	if err != nil {
		return false
	}

	return !info.IsDir()
}

func (r assetRoot) serve(w http.ResponseWriter, req *http.Request, relPath string) {
	if r.fileServer == nil {
		http.NotFound(w, req)
		return
	}

	cloned := req.Clone(req.Context())
	cloned.URL.Path = "/" + relPath
	r.fileServer.ServeHTTP(w, cloned)
}

func (r assetRoot) assetMeta(relPath string) (string, time.Time) {
	if r.fs == nil {
		return "", time.Time{}
	}

	info, err := fs.Stat(r.fs, relPath)
	if err != nil {
		return "", time.Time{}
	}

	data, err := fs.ReadFile(r.fs, relPath)
	if err != nil {
		return "", info.ModTime()
	}

	sum := sha1.Sum(data)
	etag := fmt.Sprintf(`"%x"`, sum)
	return etag, info.ModTime()
}

func addCacheHeaders(w http.ResponseWriter, assetPath string, root assetRoot, relPath string) {
	cacheValue := "public, max-age=300"
	if isHashedAsset(assetPath) {
		cacheValue = "public, max-age=31536000, immutable"
	}
	w.Header().Set("Cache-Control", cacheValue)

	etag, modTime := root.assetMeta(relPath)
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	if !modTime.IsZero() {
		w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))
	}
}

// BuildClientBundles bundles multiple client entries with code splitting.
func BuildClientBundles(entries []ClientEntry, outDir string) (map[string]ClientAssets, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("entries required")
	}
	if outDir == "" {
		return nil, fmt.Errorf("out dir required")
	}

	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return nil, fmt.Errorf("resolve out dir: %w", err)
	}
	if err := os.MkdirAll(absOut, 0755); err != nil {
		return nil, fmt.Errorf("make out dir: %w", err)
	}

	entryPoints := make(map[string]ClientEntry, len(entries))

	tmpDir, err := os.MkdirTemp("", "alloy-clients-")
	if err != nil {
		return nil, fmt.Errorf("make temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	for _, e := range entries {
		if e.Name == "" || e.Component == "" {
			return nil, fmt.Errorf("entry name and component required")
		}
		rootID := e.RootID
		if rootID == "" {
			rootID = defaultRootID(e.Component)
		}

		absPath, err := filepath.Abs(e.Component)
		if err != nil {
			return nil, fmt.Errorf("resolve entry %s: %w", e.Name, err)
		}

		wrapper := fmt.Sprintf(`
import { hydrateRoot } from 'react-dom/client';
import Component from '%s';

const propsEl = document.getElementById('%s-props');
const props = propsEl ? JSON.parse(propsEl.textContent || '{}') : {};
const rootEl = document.getElementById('%s');

if (rootEl) {
	hydrateRoot(rootEl, <Component {...props} />);
}
`, absPath, rootID, rootID)

		entryPath := filepath.Join(tmpDir, e.Name+".tsx")
		if err := os.WriteFile(entryPath, []byte(wrapper), 0644); err != nil {
			return nil, fmt.Errorf("write entry %s: %w", e.Name, err)
		}

		entryPoints[e.Name] = ClientEntry{
			Name:      e.Name,
			Component: entryPath,
			RootID:    rootID,
		}
	}

	cwd, _ := os.Getwd()

	result := api.Build(api.BuildOptions{
		EntryPointsAdvanced: toEntryPoints(entryPoints),
		Outdir:              absOut,
		Bundle:              true,
		Splitting:           true,
		Format:              api.FormatESModule,
		Write:               true,
		Metafile:            true,
		JSX:                 api.JSXAutomatic,
		JSXImportSource:     "react",
		MainFields:          []string{"browser", "module", "main"},
		Target:              api.ES2020,
		EntryNames:          "client-[name]-[hash]",
		ChunkNames:          "chunk-[hash]",
		MinifyWhitespace:    true,
		MinifySyntax:        true,
		NodePaths:           []string{filepath.Join(cwd, "node_modules")},
	})

	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("client build error: %s", result.Errors[0].Text)
	}

	var meta struct {
		Outputs map[string]struct {
			EntryPoint string `json:"entryPoint"`
			Imports    []struct {
				Path string `json:"path"`
				Kind string `json:"kind"`
			} `json:"imports"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(result.Metafile), &meta); err != nil {
		return nil, fmt.Errorf("parse metafile: %w", err)
	}

	outputs := map[string]ClientAssets{}
	prefix := filepath.Join(filepath.Base(filepath.Dir(absOut)), filepath.Base(absOut))
	prefix = filepath.ToSlash(prefix)
	for outPath, out := range meta.Outputs {
		if out.EntryPoint == "" {
			continue
		}
		name := entryName(out.EntryPoint, entryPoints)
		outPathAbs := outPath
		if !filepath.IsAbs(outPathAbs) {
			outPathAbs = filepath.Join(cwd, outPathAbs)
		}

		entryRel, err := filepath.Rel(absOut, outPathAbs)
		if err != nil {
			return nil, fmt.Errorf("entry rel: %w", err)
		}
		entryRel = filepath.ToSlash(entryRel)
		if name == "" {
			name = entryNameFromOutput(entryRel)
		}
		if name == "" {
			continue
		}

		var chunks []string
		for _, imp := range out.Imports {
			if imp.Kind == "import-statement" && !strings.HasPrefix(imp.Path, "http") {
				chunks = append(chunks, filepath.ToSlash(filepath.Join(prefix, imp.Path)))
			}
		}

		outputs[name] = ClientAssets{
			Entry:  filepath.ToSlash(filepath.Join(prefix, entryRel)),
			Chunks: chunks,
		}
	}

	for name := range entryPoints {
		if outputs[name].Entry == "" {
			return nil, fmt.Errorf("missing client bundle for %s", name)
		}
	}

	return outputs, nil
}

func entryName(entryPath string, entries map[string]ClientEntry) string {
	for name, entry := range entries {
		if filepath.Clean(entry.Component) == filepath.Clean(entryPath) {
			return name
		}
	}
	return ""
}

func entryNameFromOutput(entryRel string) string {
	base := strings.TrimSuffix(filepath.Base(entryRel), filepath.Ext(entryRel))
	base = strings.TrimPrefix(base, "client-")
	lastDash := strings.LastIndex(base, "-")
	if lastDash <= 0 {
		return ""
	}
	return base[:lastDash]
}

func toEntryPoints(entries map[string]ClientEntry) []api.EntryPoint {
	out := make([]api.EntryPoint, 0, len(entries))
	for name, entry := range entries {
		out = append(out, api.EntryPoint{
			InputPath:  entry.Component,
			OutputPath: name,
		})
	}
	return out
}

func collectAssetRoots(filesystem fs.FS) []assetRoot {
	var roots []assetRoot

	if publicFS, err := fs.Sub(filesystem, "public"); err == nil {
		roots = append(roots, assetRoot{
			prefix:     "",
			fs:         publicFS,
			fileServer: http.FileServer(http.FS(publicFS)),
		})
	}

	if distFS, err := fs.Sub(filesystem, "dist"); err == nil {
		roots = append(roots, assetRoot{
			prefix:     "dist",
			fs:         distFS,
			fileServer: http.FileServer(http.FS(distFS)),
		})
	}

	return roots
}

func (h *publicAssetsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	isAllowedMethod := r.Method == http.MethodGet || r.Method == http.MethodHead
	if !isAllowedMethod {
		h.next.ServeHTTP(w, r)
		return
	}

	assetPath := normalizeAssetPath(r.URL.Path)
	if assetPath == "" {
		h.next.ServeHTTP(w, r)
		return
	}

	for _, root := range h.roots {
		rel, ok := root.match(assetPath)
		if !ok {
			continue
		}
		if !root.assetExists(rel) {
			continue
		}

		fullPath := assetPath
		if root.prefix != "" {
			fullPath = path.Join(root.prefix, rel)
		}
		addCacheHeaders(w, fullPath, root, rel)
		root.serve(w, r, rel)
		return
	}

	h.next.ServeHTTP(w, r)
}

func isHashedAsset(assetPath string) bool {
	base := filepath.Base(assetPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	parts := strings.Split(name, "-")
	for _, part := range parts {
		if len(part) < 8 {
			continue
		}
		hashed := true
		for _, r := range part {
			if !strings.ContainsRune("0123456789abcdef", r) {
				hashed = false
				break
			}
		}
		if hashed {
			return true
		}
	}
	return false
}

func normalizeAssetPath(requestPath string) string {
	if requestPath == "" {
		return ""
	}

	trimmed := strings.TrimPrefix(requestPath, "/")
	clean := path.Clean(trimmed)
	isRoot := clean == "." || clean == ""
	if isRoot {
		return ""
	}

	if strings.HasPrefix(clean, "..") {
		return ""
	}

	return clean
}

func ensureLeadingSlash(p string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

func resolvePrebuiltFiles(filesystem fs.FS, p Page) (PrebuiltFiles, error) {
	if p.Files.Server != "" || p.Files.Client != "" || p.Files.CSS != "" {
		return p.Files, nil
	}

	dist := p.DistDir
	if dist == "" {
		dist = "dist/alloy"
	}

	base := p.Name
	if base == "" {
		component := filepath.Base(p.Component)
		base = strings.TrimSuffix(component, filepath.Ext(component))
	}

	if filesystem != nil {
		if manifestFiles, ok, err := lookupManifest(filesystem, dist, base); err != nil {
			return PrebuiltFiles{}, err
		} else if ok {
			return manifestFiles, nil
		}
	}

	return PrebuiltFiles{
		Server: filepath.Join(dist, fmt.Sprintf("%s-server.js", base)),
		Client: filepath.Join(dist, fmt.Sprintf("%s-client.js", base)),
		CSS:    filepath.Join(dist, fmt.Sprintf("%s.css", base)),
	}, nil
}

func lookupManifest(filesystem fs.FS, dist string, base string) (PrebuiltFiles, bool, error) {
	if base == "" {
		return PrebuiltFiles{}, false, nil
	}

	manifestPath := path.Join(filepath.ToSlash(dist), "manifest.json")
	data, err := fs.ReadFile(filesystem, manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return PrebuiltFiles{}, false, nil
	}
	if err != nil {
		return PrebuiltFiles{}, false, fmt.Errorf("read manifest: %w", err)
	}

	manifest := map[string]manifestEntry{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PrebuiltFiles{}, false, fmt.Errorf("decode manifest: %w", err)
	}

	entry, ok := manifest[base]
	if !ok {
		return PrebuiltFiles{}, false, nil
	}

	client := entry.Client
	if client == "" && len(entry.Chunks) > 0 {
		client = entry.Chunks[0]
		entry.Chunks = entry.Chunks[1:]
	}

	shared := ""
	if entry.Shared != "" {
		shared = path.Join(dist, entry.Shared)
	}

	return PrebuiltFiles{
		Server:       path.Join(dist, entry.Server),
		Client:       path.Join(dist, client),
		ClientChunks: joinPaths(dist, entry.Chunks),
		CSS:          path.Join(dist, entry.CSS),
		SharedCSS:    shared,
	}, true, nil
}

func joinPaths(prefix string, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, path.Join(prefix, n))
	}
	return out
}

func defaultRootID(componentPath string) string {
	base := filepath.Base(componentPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		return "root"
	}
	return base + "-root"
}

func isDevMode() bool {
	return os.Getenv("ALLOY_DEV") != ""
}

func buildAll(componentPath string, rootID string) (string, string, string, []string, error) {
	var wg sync.WaitGroup
	wg.Add(3)

	var serverJS string
	var clientJS string
	var css string
	var serverDeps []string
	var clientDeps []string
	var cssDeps []string
	errs := make(chan error, 3)

	go func() {
		defer wg.Done()
		b, deps, buildErr := bundleTSXFile(componentPath)
		if buildErr != nil {
			errs <- fmt.Errorf("server bundle error: %w", buildErr)
			return
		}
		serverJS = b
		serverDeps = deps
	}()

	go func() {
		defer wg.Done()
		b, deps, buildErr := bundleClientJS(componentPath, rootID)
		if buildErr != nil {
			errs <- fmt.Errorf("client bundle error: %w", buildErr)
			return
		}
		clientJS = b
		clientDeps = deps
	}()

	go func() {
		defer wg.Done()
		out, deps, buildErr := buildTailwindCSS(componentPath)
		if buildErr != nil {
			errs <- fmt.Errorf("css error: %w", buildErr)
			return
		}
		css = out
		cssDeps = deps
	}()

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			return "", "", "", nil, err
		}
	}

	if os.Getenv("ALLOY_DEBUG_BUNDLE") != "" {
		fmt.Println("buildAll lengths:", len(serverJS), len(clientJS), len(css))
	}

	deps := mergePaths(serverDeps, clientDeps, cssDeps)
	return serverJS, clientJS, css, deps, nil
}

func readDevCSSCache(cssPath string, componentPath string, cssModTime time.Time, componentModTime time.Time) (string, bool) {
	if !isDevMode() {
		return "", false
	}

	cachePath := devCSSCachePath(cssPath, componentPath, cssModTime, componentModTime)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return "", false
	}

	return string(data), true
}

func writeDevCSSCache(cssPath string, componentPath string, cssModTime time.Time, componentModTime time.Time, css string) {
	if !isDevMode() {
		return
	}

	cachePath := devCSSCachePath(cssPath, componentPath, cssModTime, componentModTime)
	_ = os.MkdirAll(filepath.Dir(cachePath), 0755)
	_ = os.WriteFile(cachePath, []byte(css), 0644)
}

func devCSSCachePath(cssPath string, componentPath string, cssModTime time.Time, componentModTime time.Time) string {
	dir := findPackageRoot(filepath.Dir(cssPath))
	sum := filepath.Base(cssPath) +
		strconv.FormatInt(cssModTime.UnixNano(), 10) +
		componentPath +
		strconv.FormatInt(componentModTime.UnixNano(), 10)
	filename := fmt.Sprintf("%s-%x.css", filepath.Base(dir), sha1String(sum))
	return filepath.Join(dir, ".alloy-cache", filename)
}

func sha1String(s string) []byte {
	h := sha1.New()
	_, _ = h.Write([]byte(s))
	return h.Sum(nil)
}

type tailwindWatcher struct {
	outputPath string
	cmd        *exec.Cmd
	ready      chan struct{}
	errCh      chan error
}

var tailwindWatchers = struct {
	sync.Mutex
	w map[string]*tailwindWatcher
}{
	w: make(map[string]*tailwindWatcher),
}

func ensureTailwindWatcher(componentPath string, cssPath string) (string, error) {
	contentPattern := filepath.Join(filepath.Dir(componentPath), "**", "*.{html,js,jsx,ts,tsx}")

	tailwindWatchers.Lock()
	existing := tailwindWatchers.w[cssPath]
	tailwindWatchers.Unlock()

	if existing != nil {
		select {
		case <-existing.ready:
			select {
			case err := <-existing.errCh:
				if err != nil {
					return "", err
				}
			default:
			}
			info, err := os.Stat(existing.outputPath)
			if err == nil && info.Size() > 0 {
				return existing.outputPath, nil
			}
			return "", fmt.Errorf("tailwind watch output not ready")
		case <-time.After(5 * time.Second):
			return "", fmt.Errorf("tailwind watch not ready")
		}
	}

	outputPath := filepath.Join(findPackageRoot(filepath.Dir(cssPath)), ".alloy-cache", "tailwind-watch-"+filepath.Base(cssPath)+".css")
	_ = os.MkdirAll(filepath.Dir(outputPath), 0755)

	root := findPackageRoot(filepath.Dir(cssPath))
	cmd, err := tailwindCmd(root,
		"-i", cssPath,
		"-o", outputPath,
		"--content", contentPattern,
		"--watch",
		"--poll=500",
	)
	if err != nil {
		return "", err
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	watcher := &tailwindWatcher{
		outputPath: outputPath,
		cmd:        cmd,
		ready:      make(chan struct{}),
		errCh:      make(chan error, 1),
	}

	tailwindWatchers.Lock()
	tailwindWatchers.w[cssPath] = watcher
	tailwindWatchers.Unlock()

	go func() {
		if err := cmd.Start(); err != nil {
			watcher.errCh <- err
			close(watcher.ready)
			clearWatcher(cssPath)
			return
		}

		// Wait for initial output
		for i := 0; i < 20; i++ {
			if info, err := os.Stat(outputPath); err == nil && info.Size() > 0 {
				close(watcher.ready)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		watcher.errCh <- fmt.Errorf("tailwind watch output not ready")
		close(watcher.ready)
		clearWatcher(cssPath)
	}()

	select {
	case <-watcher.ready:
		select {
		case err := <-watcher.errCh:
			if err != nil {
				return "", err
			}
		default:
		}
		if info, err := os.Stat(outputPath); err == nil && info.Size() > 0 {
			return outputPath, nil
		}
		return "", fmt.Errorf("tailwind watch output not ready")
	case <-time.After(3 * time.Second):
		return "", fmt.Errorf("tailwind watch start timeout")
	}
}

func clearWatcher(cssPath string) {
	tailwindWatchers.Lock()
	delete(tailwindWatchers.w, cssPath)
	tailwindWatchers.Unlock()
}

func readWatchedCSS(outputPath string, minModTime time.Time) (string, error) {
	for range 20 {
		info, err := os.Stat(outputPath)
		if err == nil && info.Size() > 0 && (info.ModTime().After(minModTime) || info.ModTime().Equal(minModTime)) {
			data, readErr := os.ReadFile(outputPath)
			if readErr != nil {
				return "", readErr
			}
			return string(data), nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("tailwind watch output not fresh")
}

// bundleTSXFile builds from a file path, supporting relative imports
func bundleTSXFile(componentPath string) (string, []string, error) {
	absPath, err := filepath.Abs(componentPath)
	if err != nil {
		return "", nil, err
	}

	tmpDir, err := os.MkdirTemp("", "alloy-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmpDir)

	// Write entry that wraps the component file with renderToString
	entryCode := fmt.Sprintf(`
import { renderToString } from 'react-dom/server';
import Component from '%s';

export default function render(props: any) {
	return renderToString(<Component {...props} />);
}
	`, absPath)

	entryPath := filepath.Join(tmpDir, "entry.tsx")
	if err := os.WriteFile(entryPath, []byte(entryCode), 0644); err != nil {
		return "", nil, err
	}

	cwd, _ := os.Getwd()
	minify := !isDevMode()

	result := api.Build(api.BuildOptions{
		EntryPoints:      []string{entryPath},
		Bundle:           true,
		Write:            false,
		Metafile:         true,
		Format:           api.FormatIIFE,
		GlobalName:       "__Component",
		MinifyWhitespace: minify,
		MinifySyntax:     minify,
		Target:           api.ES2020,
		JSX:              api.JSXAutomatic,
		JSXImportSource:  "react",
		NodePaths:        []string{filepath.Join(cwd, "node_modules")},
		Platform:         api.PlatformBrowser,
		MainFields:       []string{"browser", "module", "main"},
	})

	if len(result.Errors) > 0 {
		return "", nil, fmt.Errorf("%s", result.Errors[0].Text)
	}

	if len(result.OutputFiles) == 0 {
		return "", nil, fmt.Errorf("no output from esbuild")
	}

	deps, err := bundleInputs(result.Metafile)
	if err != nil {
		return "", nil, err
	}

	deps = filterOutPath(deps, entryPath)

	serverCode := string(result.OutputFiles[0].Contents)
	if os.Getenv("ALLOY_DEBUG_BUNDLE") != "" {
		_ = os.WriteFile("/tmp/alloy-server.js", []byte(serverCode), 0644)
	}
	return serverCode, deps, nil
}

// bundleClientJS creates browser bundle for hydration
func bundleClientJS(componentPath string, rootID string) (string, []string, error) {
	absPath, err := filepath.Abs(componentPath)
	if err != nil {
		return "", nil, err
	}

	tmpDir, err := os.MkdirTemp("", "alloy-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmpDir)

	// Client entry that hydrates the component
	entryCode := fmt.Sprintf(`
import { hydrateRoot } from 'react-dom/client';
import Component from '%s';

const propsEl = document.getElementById('%s-props');
const props = propsEl ? JSON.parse(propsEl.textContent) : {};
const rootEl = document.getElementById('%s');

if (rootEl) {
	hydrateRoot(rootEl, <Component {...props} />);
}
	`, absPath, rootID, rootID)

	entryPath := filepath.Join(tmpDir, "client.tsx")
	if err := os.WriteFile(entryPath, []byte(entryCode), 0644); err != nil {
		return "", nil, err
	}

	cwd, _ := os.Getwd()
	minify := !isDevMode()

	result := api.Build(api.BuildOptions{
		EntryPoints:      []string{entryPath},
		Bundle:           true,
		Write:            false,
		Metafile:         true,
		Format:           api.FormatIIFE,
		MinifyWhitespace: minify,
		MinifySyntax:     minify,
		Target:           api.ES2020,
		JSX:              api.JSXAutomatic,
		JSXImportSource:  "react",
		NodePaths:        []string{filepath.Join(cwd, "node_modules")},
		Platform:         api.PlatformBrowser,
		MainFields:       []string{"browser", "module", "main"},
	})

	if len(result.Errors) > 0 {
		return "", nil, fmt.Errorf("%s", result.Errors[0].Text)
	}

	if len(result.OutputFiles) == 0 {
		return "", nil, fmt.Errorf("no output from esbuild")
	}

	deps, err := bundleInputs(result.Metafile)
	if err != nil {
		return "", nil, err
	}

	deps = filterOutPath(deps, entryPath)
	return string(result.OutputFiles[0].Contents), deps, nil
}

// buildTailwindCSS uses the Tailwind CLI to compile CSS for a component.
func buildTailwindCSS(componentPath string) (string, []string, error) {
	absPath, err := filepath.Abs(componentPath)
	if err != nil {
		return "", nil, err
	}

	componentInfo, err := os.Stat(absPath)
	if err != nil {
		return "", nil, fmt.Errorf("stat component: %w", err)
	}

	dir := filepath.Dir(absPath)
	cssPath := filepath.Join(dir, "app.css")
	cssInfo, err := os.Stat(cssPath)
	if err != nil {
		return "", nil, fmt.Errorf("missing css file: %w", err)
	}

	cssDeps := []string{cssPath}

	if isDevMode() {
		if css, ok := readDevCSSCache(cssPath, absPath, cssInfo.ModTime(), componentInfo.ModTime()); ok {
			go func() {
				_, _ = ensureTailwindWatcher(componentPath, cssPath)
			}()
			return css, cssDeps, nil
		}

		outputPath, err := ensureTailwindWatcher(componentPath, cssPath)
		if err == nil {
			latest := cssInfo.ModTime()
			if componentInfo.ModTime().After(latest) {
				latest = componentInfo.ModTime()
			}
			css, readErr := readWatchedCSS(outputPath, latest)
			if readErr == nil {
				writeDevCSSCache(cssPath, absPath, cssInfo.ModTime(), componentInfo.ModTime(), css)
				return css, cssDeps, nil
			}
		}
	}

	cssString, err := runTailwind(cssPath, dir)
	if err != nil {
		return "", nil, err
	}

	writeDevCSSCache(cssPath, absPath, cssInfo.ModTime(), componentInfo.ModTime(), cssString)
	return cssString, cssDeps, nil
}

// BuildSharedCSS builds a single CSS bundle from a base app.css and content globs.
func BuildSharedCSS(cssPath string) (string, error) {
	if cssPath == "" {
		return "", fmt.Errorf("css path required")
	}

	root := findPackageRoot(filepath.Dir(cssPath))
	return runTailwind(cssPath, root)
}

func runTailwind(cssPath string, root string) (string, error) {
	if root == "" {
		root = findPackageRoot(filepath.Dir(cssPath))
	}

	outputFile, err := os.CreateTemp("", "alloy-tailwind-*.css")
	if err != nil {
		return "", fmt.Errorf("create temp css: %w", err)
	}
	outputPath := outputFile.Name()
	outputFile.Close()
	defer os.Remove(outputPath)

	args := []string{"-i", cssPath, "-o", outputPath}
	if !isDevMode() {
		args = append(args, "--minify")
	}

	cmd, err := tailwindCmd(root, args...)
	if err != nil {
		return "", err
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tailwind cli: %v: %s", err, strings.TrimSpace(string(output)))
	}

	css, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("read css output: %w", err)
	}

	return string(css), nil
}

func findPackageRoot(startDir string) string {
	dir := startDir

	for {
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}

		dir = parent
	}
}

func tailwindCmd(root string, args ...string) (*exec.Cmd, error) {
	runner, baseArgs := resolveTailwindRunner(root)
	if runner == "" {
		return nil, fmt.Errorf("tailwind runner not found")
	}
	fullArgs := append(baseArgs, args...)
	cmd := exec.Command(runner, fullArgs...)
	cmd.Dir = root
	return cmd, nil
}

func resolveTailwindRunner(root string) (string, []string) {
	switch {
	case fileExists(filepath.Join(root, "pnpm-lock.yaml")):
		return tailwindRunnerFor("pnpm")
	case fileExists(filepath.Join(root, "yarn.lock")):
		return tailwindRunnerFor("yarn")
	case fileExists(filepath.Join(root, "bun.lockb")):
		return tailwindRunnerFor("bun")
	case fileExists(filepath.Join(root, "package-lock.json")):
		return tailwindRunnerFor("npm")
	default:
		return tailwindRunnerFor("npx")
	}
}

func tailwindRunnerFor(name string) (string, []string) {
	switch name {
	case "pnpm":
		return "pnpm", []string{"exec", "tailwindcss"}
	case "npm", "npx":
		return "npx", []string{"tailwindcss"}
	case "yarn":
		return "yarn", []string{"tailwindcss"}
	case "bun", "bunx":
		return "bunx", []string{"tailwindcss"}
	default:
		return name, []string{"tailwindcss"}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readBundlesFromCache(path string, rootID string) (string, string, string) {
	bundleCache.Lock()
	entry := bundleCache.entries[path]
	bundleCache.Unlock()

	if entry == nil {
		return "", "", ""
	}
	if entryStale(entry) {
		return "", "", ""
	}

	client := entry.clientByID[rootID]
	return entry.serverJS, client, entry.css
}

func storeBundles(path string, rootID string, serverJS string, clientJS string, css string, deps map[string]fileStamp) {
	bundleCache.Lock()
	entry := bundleCache.entries[path]
	if entry == nil || entryStale(entry) {
		entry = &bundleCacheEntry{
			serverJS:   serverJS,
			clientByID: map[string]string{rootID: clientJS},
			css:        css,
			deps:       deps,
			prebuilt:   false,
		}
		bundleCache.entries[path] = entry
		bundleCache.Unlock()
		return
	}

	entry.clientByID[rootID] = clientJS
	entry.deps = deps
	entry.prebuilt = false
	bundleCache.Unlock()
}

func entryStale(entry *bundleCacheEntry) bool {
	if entry == nil {
		return true
	}
	if entry.prebuilt {
		return false
	}
	return cacheStale(entry.deps)
}

// executeSSR runs the JS code in QuickJS using the runtime pool.
func executeSSR(ctx context.Context, componentPath string, jsCode string, props map[string]any) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	timeout := currentRenderTimeout()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()

	vm, err := jsPool.acquire(ctx)
	if err != nil {
		return "", fmt.Errorf("acquire runtime: %w", err)
	}

	vm.rt.SetInterruptHandler(makeInterruptHandler(ctx))
	html, runErr := runSSR(vm.ctx, jsCode, props)
	vm.rt.ClearInterruptHandler()

	if runErr != nil {
		jsPool.release(vm)
		return "", runErr
	}

	jsPool.release(vm)
	return html, nil
}

func makeInterruptHandler(ctx context.Context) quickjs.InterruptHandler {
	if ctx == nil {
		return nil
	}
	return func() int {
		select {
		case <-ctx.Done():
			return 1
		default:
			return 0
		}
	}
}

func runSSR(ctx *quickjs.Context, jsCode string, props map[string]any) (string, error) {
	result := ctx.Eval(jsCode)
	if result.IsException() {
		result.Free()
		return "", fmt.Errorf("component eval: %s", ctx.Exception())
	}
	defer result.Free()

	if os.Getenv("ALLOY_DEBUG_BUNDLE") != "" {
		fmt.Println("serverJS length:", len(jsCode))

		fmt.Println("bundle eval undefined?", result.IsUndefined(), "null?", result.IsNull())

		typeofComponent := ctx.Eval("typeof __Component")
		fmt.Println("typeof __Component:", typeofComponent.String())
		typeofComponent.Free()

		typeofDefault := ctx.Eval("typeof __Component.default")
		fmt.Println("typeof __Component.default:", typeofDefault.String())
		typeofDefault.Free()
	}

	propsJSON, err := json.Marshal(props)
	if err != nil {
		return "", fmt.Errorf("props marshal: %w", err)
	}

	renderCode := fmt.Sprintf(`
			(function() {
				var render = __Component.default || __Component;
				return render(%s);
			})()
		`, string(propsJSON))

	renderResult := ctx.Eval(renderCode)
	defer renderResult.Free()

	if !renderResult.IsString() {
		return "", fmt.Errorf("expected string result, got %s", renderResult.String())
	}

	return renderResult.String(), nil
}

func renderWithMetrics(componentPath string, propsSize int, render func() (string, error)) (string, error) {
	start := time.Now()
	html, err := render()
	outcome := RenderOutcomeSuccess
	if err != nil {
		outcome = RenderOutcomeError
	}
	recordRender(componentPath, time.Since(start), outcome, propsSize)
	return html, err
}

func recordRender(component string, duration time.Duration, outcome RenderOutcome, propsBytes int) {
	rec := metricsRecorder.Load()
	if rec == nil {
		return
	}
	recorder, ok := rec.(MetricsRecorder)
	if !ok {
		return
	}
	recorder.RecordRender(component, duration, outcome, propsBytes)
}

func propsSizeBytes(props map[string]any) int {
	if len(props) == 0 {
		return 0
	}
	data, err := json.Marshal(props)
	if err != nil {
		return 0
	}
	return len(data)
}

type fileStamp struct {
	modTime time.Time
	size    int64
}

func bundleInputs(meta string) ([]string, error) {
	type metafile struct {
		Inputs map[string]struct {
			Bytes int64 `json:"bytes"`
		} `json:"inputs"`
	}

	var mf metafile
	if err := json.Unmarshal([]byte(meta), &mf); err != nil {
		return nil, fmt.Errorf("parse esbuild metafile: %w", err)
	}

	var inputs []string
	for path := range mf.Inputs {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, abs)
	}
	return inputs, nil
}

func mergePaths(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var merged []string

	for _, group := range groups {
		for _, path := range group {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			merged = append(merged, path)
		}
	}

	return merged
}

func captureFileStamps(paths []string) (map[string]fileStamp, error) {
	stamps := make(map[string]fileStamp, len(paths))

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		stamps[path] = fileStamp{
			modTime: info.ModTime(),
			size:    info.Size(),
		}
	}

	return stamps, nil
}

func cacheStale(stamps map[string]fileStamp) bool {
	if len(stamps) == 0 {
		return true
	}

	for path, stamp := range stamps {
		info, err := os.Stat(path)
		if err != nil {
			return true
		}
		if !info.ModTime().Equal(stamp.modTime) {
			return true
		}
		if info.Size() != stamp.size {
			return true
		}
	}

	return false
}

func filterOutPath(paths []string, skip string) []string {
	var filtered []string

	for _, path := range paths {
		if path == skip {
			continue
		}
		filtered = append(filtered, path)
	}

	return filtered
}

// ServePrebuiltPageWithContext is like ServePrebuiltPage but uses a provided context for SSR timeouts.
func ServePrebuiltPageWithContext(ctx context.Context, w http.ResponseWriter, r *http.Request, componentPath string, props map[string]any, rootID string, files PrebuiltFiles) {
	if ctx == nil {
		ctx = r.Context()
	}
	result, err := RenderPrebuiltWithContext(ctx, componentPath, props, rootID, files)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, result.ToHTML(rootID))
}
