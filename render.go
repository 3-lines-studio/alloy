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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buke/quickjs-go"
	"github.com/evanw/esbuild/pkg/api"
	"golang.org/x/sync/errgroup"
)

const (
	defaultRenderTimeout   = 2 * time.Second
	liveReloadTickInterval = 5 * time.Second
	quickjsStackSize       = 4 * 1024 * 1024
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

// ClientAssets represents bundled client assets from BuildClientBundles
type ClientAssets struct {
	Entry  string
	Chunks []string
}

// ClientEntry defines a client component entry point for bundling
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

// Internal runtime types
type jsRuntime struct {
	rt  *quickjs.Runtime
	ctx *quickjs.Context
}

type runtimePool struct {
	mu   sync.Mutex
	sem  chan struct{}
	size int
}

// Internal cache types
type bundleCacheEntry struct {
	serverJS   string
	clientByID map[string]string
	css        string
	deps       map[string]fileStamp
	prebuilt   bool
}

type fileStamp struct {
	modTime time.Time
	size    int64
}

type manifestEntry struct {
	Server string   `json:"server"`
	Client string   `json:"client,omitempty"`
	CSS    string   `json:"css"`
	Chunks []string `json:"chunks,omitempty"`
	Shared string   `json:"sharedCss,omitempty"`
}

// Internal handler types
type publicAssetsHandler struct {
	next  http.Handler
	roots []assetRoot
}

type assetRoot struct {
	prefix     string
	fs         fs.FS
	fileServer http.Handler
}

// Internal routing types
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

// Internal helper types
type metaTag struct {
	TagName  string
	Name     string
	Property string
	Content  string
	Rel      string
	Href     string
	Title    string
}

type tailwindWatcher struct {
	outputPath string
	cmd        *exec.Cmd
	ready      chan struct{}
	errCh      chan error
}

var (
	renderTimeout atomic.Value
	jsPool        *runtimePool
)

func init() {
	renderTimeout.Store(defaultRenderTimeout)
	jsPool = newRuntimePool(defaultRuntimePoolSize())
}

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

func newRuntimeWithContext() (*jsRuntime, error) {
	rt := quickjs.NewRuntime()
	rt.SetMaxStackSize(quickjsStackSize)

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
	if vm == nil {
		return
	}
	if vm.ctx != nil {
		vm.ctx.Close()
	}
	if vm.rt != nil {
		vm.rt.Close()
	}
}

func currentRenderTimeout() time.Duration {
	timeout, _ := renderTimeout.Load().(time.Duration)
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

	ticker := time.NewTicker(liveReloadTickInterval)
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

var bundleCache = struct {
	sync.RWMutex
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

	propsJSON, err := json.Marshal(r.Props)
	if err != nil {
		propsJSON = []byte("{}")
	}
	metaTags := metaTagsFromProps(r.Props)
	head := buildHead(metaTags)
	cssTag := r.buildCSSTag()
	scriptTag := r.buildScriptTag()
	liveReload := buildLiveReloadScript()

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

func (r *RenderResult) buildCSSTag() string {
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
	return cssTag
}

func (r *RenderResult) buildScriptTag() string {
	switch {
	case len(r.ClientPaths) > 0:
		var b strings.Builder
		for _, p := range r.ClientPaths {
			fmt.Fprintf(&b, "<script type=\"module\" src=\"%s\"></script>\n", p)
		}
		return strings.TrimSuffix(b.String(), "\n")
	case r.ClientPath != "":
		return fmt.Sprintf(`<script type="module" src="%s"></script>`, r.ClientPath)
	case r.ClientJS != "":
		return fmt.Sprintf(`<script type="module">%s</script>`, r.ClientJS)
	}
	return ""
}

func buildLiveReloadScript() string {
	if !isDevMode() {
		return ""
	}
	return `
<script type="module">
    const es = new EventSource("/__live");
    es.onmessage = function (ev) {
        if (ev.data === "reload") {
            window.location.reload();
        }
    };
</script>`
}

func buildHead(tags []metaTag) string {
	var b strings.Builder

	b.WriteString("\t<meta charset=\"UTF-8\">")

	hasViewport := false
	hasTitle := false

	for _, tag := range tags {
		if tag.Title != "" {
			if !hasTitle {
				escaped := html.EscapeString(tag.Title)
				fmt.Fprintf(&b, "\n\t<title>%s</title>", escaped)
				hasTitle = true
			}
			continue
		}

		if tag.Name == "viewport" {
			hasViewport = true
		}

		tagHTML := buildMetaTagHTML(tag)
		if tagHTML != "" {
			fmt.Fprintf(&b, "\n\t%s", tagHTML)
		}
	}

	if !hasViewport {
		b.WriteString("\n\t<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">")
	}

	if !hasTitle {
		b.WriteString("\n\t<title>Alloy</title>")
	}

	return b.String()
}

func buildMetaTagHTML(tag metaTag) string {
	tagName := tag.TagName
	if tagName == "" {
		if tag.Rel != "" || tag.Href != "" {
			tagName = "link"
		} else {
			tagName = "meta"
		}
	}

	var attrs []string
	addAttr := func(name, val string) {
		if val != "" {
			attrs = append(attrs, fmt.Sprintf(`%s="%s"`, name, html.EscapeString(val)))
		}
	}
	addAttr("name", tag.Name)
	addAttr("property", tag.Property)
	addAttr("content", tag.Content)
	addAttr("rel", tag.Rel)
	addAttr("href", tag.Href)
	if len(attrs) == 0 {
		return ""
	}

	sortMetaAttributes(attrs)

	return fmt.Sprintf("<%s %s>", tagName, strings.Join(attrs, " "))
}

func sortMetaAttributes(attrs []string) {
	priority := func(attr string) int {
		if strings.HasPrefix(attr, "name=") {
			return 0
		}
		if strings.HasPrefix(attr, "property=") {
			return 1
		}
		return 2
	}
	sort.SliceStable(attrs, func(i, j int) bool {
		pi, pj := priority(attrs[i]), priority(attrs[j])
		if pi != pj {
			return pi < pj
		}
		return attrs[i] < attrs[j]
	})
}

func metaTagsFromProps(props map[string]any) []metaTag {
	metaRaw, ok := props["meta"]
	if !ok {
		return buildDefaultMetaTags(props)
	}

	var tags []metaTag

	switch v := metaRaw.(type) {
	case []any:
		tags = parseMetaArray(v)
	case []map[string]any:
		anySlice := make([]any, len(v))
		for i, m := range v {
			anySlice[i] = m
		}
		tags = parseMetaArray(anySlice)
	default:
		return buildDefaultMetaTags(props)
	}

	if len(tags) == 0 {
		return buildDefaultMetaTags(props)
	}

	return tags
}

func parseMetaArray(metaArray []any) []metaTag {
	tags := make([]metaTag, 0, len(metaArray))
	for _, item := range metaArray {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		tag := metaTag{
			TagName:  stringFromMap(itemMap, "tagName"),
			Name:     stringFromMap(itemMap, "name"),
			Property: stringFromMap(itemMap, "property"),
			Content:  stringFromMap(itemMap, "content"),
			Rel:      stringFromMap(itemMap, "rel"),
			Href:     stringFromMap(itemMap, "href"),
			Title:    stringFromMap(itemMap, "title"),
		}

		if !isValidMetaTag(tag) {
			continue
		}

		tags = append(tags, tag)
	}

	return tags
}

func buildDefaultMetaTags(props map[string]any) []metaTag {
	title := stringFromMap(props, "title")
	if title == "" {
		title = "Alloy"
	}

	return []metaTag{
		{Title: title},
		{Name: "viewport", Content: "width=device-width, initial-scale=1.0"},
	}
}

func isValidMetaTag(tag metaTag) bool {
	if tag.Title != "" || (tag.Name != "" && tag.Content != "") || (tag.Property != "" && tag.Content != "") || (tag.Rel != "" && tag.Href != "") {
		return true
	}
	if tag.TagName != "" {
		return tag.Name != "" || tag.Property != "" || tag.Content != "" || tag.Rel != "" || tag.Href != ""
	}
	return false
}

func stringFromMap(m map[string]any, key string) string {
	str, _ := m[key].(string)
	return str
}

// RenderTSXFileWithHydration renders with client-side hydration support
func RenderTSXFileWithHydration(filePath string, props map[string]any, rootID string) (*RenderResult, error) {
	return RenderTSXFileWithHydrationWithContext(context.Background(), filePath, props, rootID)
}

// RenderTSXFileWithHydrationWithContext renders with hydration using a custom context.
func RenderTSXFileWithHydrationWithContext(ctx context.Context, filePath string, props map[string]any, rootID string) (*RenderResult, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("resolve component path %s: %w", filePath, err)
	}

	if _, err := os.Stat(absPath); err != nil {
		return nil, fmt.Errorf("component not found %s: %w", absPath, err)
	}

	dir := filepath.Dir(absPath)
	cssPath := filepath.Join(dir, "app.css")
	if _, err := os.Stat(cssPath); err != nil {
		return nil, fmt.Errorf("missing app.css at %s: %w", cssPath, err)
	}

	devMode := isDevMode()
	serverJS, clientJS, css := readBundlesFromCache(absPath, rootID)
	if serverJS == "" || clientJS == "" || css == "" {
		if !devMode {
			return nil, fmt.Errorf("component %s (rootID=%s) not registered; call RegisterPrebuiltBundle in production", absPath, rootID)
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

	html, err := executeSSR(ctx, absPath, serverJS, props)
	if err != nil {
		return nil, fmt.Errorf("ssr failed for %s: %w", absPath, err)
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
		return nil, fmt.Errorf("resolve component path %s: %w", filePath, err)
	}

	serverJS, clientJS, css := readBundlesFromCache(absPath, rootID)
	if serverJS == "" || clientJS == "" || css == "" {
		return nil, fmt.Errorf("component %s (rootID=%s) not registered; call RegisterPrebuiltBundleFromFS before serving", absPath, rootID)
	}

	html, err := executeSSR(ctx, absPath, serverJS, props)
	if err != nil {
		return nil, fmt.Errorf("ssr failed for %s: %w", absPath, err)
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
		return "", nil, fmt.Errorf("resolve component path %s: %w", filePath, err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", nil, fmt.Errorf("component not found %s: %w", absPath, err)
	}

	serverJS, deps, err := bundleTSXFile(absPath)
	if err != nil {
		return "", nil, fmt.Errorf("bundle server %s: %w", absPath, err)
	}

	return serverJS, deps, nil
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

// RouteParams returns named params captured from the matched route, if any.
func RouteParams(r *http.Request) map[string]string {
	if r == nil {
		return nil
	}
	params, _ := r.Context().Value(routeParamsKey{}).(map[string]string)
	return params
}

func cleanPath(p string) string {
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

func parseRoutePattern(pattern string) ([]routeSegment, error) {
	if pattern == "" {
		return nil, fmt.Errorf("route required")
	}

	cleaned := cleanPath(pattern)

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

func matchRoute(segments []routeSegment, requestPath string) (map[string]string, bool) {
	cleaned := cleanPath(requestPath)
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
		reqPath := cleanPath(r.URL.Path)

		for _, entry := range routeEntries {
			params, ok := matchRoute(entry.segments, reqPath)
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

func (r assetRoot) assetMeta(relPath string, hashed bool) (string, time.Time) {
	if r.fs == nil {
		return "", time.Time{}
	}

	info, err := fs.Stat(r.fs, relPath)
	if err != nil {
		return "", time.Time{}
	}

	if hashed {
		etag := fmt.Sprintf(`"%x-%d"`, info.ModTime().UnixNano(), info.Size())
		return etag, info.ModTime()
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
	hashed := isHashedAsset(assetPath)
	cacheValue := "public, max-age=300"
	if hashed {
		cacheValue = "public, max-age=31536000, immutable"
	}
	w.Header().Set("Cache-Control", cacheValue)

	etag, modTime := root.assetMeta(relPath, hashed)
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

	if distFS, err := fs.Sub(filesystem, path.Join("app", "dist")); err == nil {
		roots = append(roots, assetRoot{
			prefix:     path.Join("app", "dist"),
			fs:         distFS,
			fileServer: http.FileServer(http.FS(distFS)),
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
		dist = defaultDistForComponent(p.Component)
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

func defaultDistForComponent(component string) string {
	if component == "" {
		return "dist/alloy"
	}

	dir := filepath.Dir(component)
	if dir == "" || dir == "." {
		return "dist/alloy"
	}

	parent := filepath.Dir(dir)
	if parent == "" || parent == "." {
		return "dist/alloy"
	}

	return filepath.Join(parent, "dist/alloy")
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
	var serverJS, clientJS, css string
	var serverDeps, clientDeps, cssDeps []string

	var g errgroup.Group

	g.Go(func() error {
		b, deps, err := bundleTSXFile(componentPath)
		if err != nil {
			return fmt.Errorf("bundle server %s: %w", componentPath, err)
		}
		serverJS = b
		serverDeps = deps
		return nil
	})

	g.Go(func() error {
		b, deps, err := bundleClientJS(componentPath, rootID)
		if err != nil {
			return fmt.Errorf("bundle client %s: %w", componentPath, err)
		}
		clientJS = b
		clientDeps = deps
		return nil
	})

	g.Go(func() error {
		out, deps, err := buildTailwindCSS(componentPath)
		if err != nil {
			return fmt.Errorf("build css for %s: %w", componentPath, err)
		}
		css = out
		cssDeps = deps
		return nil
	})

	if err := g.Wait(); err != nil {
		return "", "", "", nil, err
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
	if existing != nil {
		tailwindWatchers.Unlock()
		return waitForWatcher(existing)
	}

	outputPath := filepath.Join(findPackageRoot(filepath.Dir(cssPath)), ".alloy-cache", "tailwind-watch-"+filepath.Base(cssPath)+".css")
	_ = os.MkdirAll(filepath.Dir(outputPath), 0755)

	root := findPackageRoot(filepath.Dir(cssPath))
	cmd, err := tailwindCmd(root,
		"-i", cssPath,
		"-o", outputPath,
		"--content", contentPattern,
		"--watch",
	)
	if err != nil {
		tailwindWatchers.Unlock()
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

	tailwindWatchers.w[cssPath] = watcher
	tailwindWatchers.Unlock()

	go func() {
		if err := cmd.Start(); err != nil {
			watcher.errCh <- err
			close(watcher.ready)
			clearWatcher(cssPath)
			return
		}

		go func() {
			err := cmd.Wait()
			if err != nil {
				select {
				case watcher.errCh <- err:
				default:
				}
			}
			clearWatcher(cssPath)
		}()

		// Wait for initial output
		for range 20 {
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

	return waitForWatcher(watcher)
}

func waitForWatcher(watcher *tailwindWatcher) (string, error) {
	select {
	case <-watcher.ready:
		select {
		case err := <-watcher.errCh:
			if err != nil {
				return "", err
			}
		default:
		}
		info, err := os.Stat(watcher.outputPath)
		if err == nil && info.Size() > 0 {
			return watcher.outputPath, nil
		}
		return "", fmt.Errorf("tailwind watch output not ready")
	case <-time.After(5 * time.Second):
		return "", fmt.Errorf("tailwind watch not ready")
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
import { renderToString } from 'react-dom/server.edge';
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
		return "", nil, fmt.Errorf("esbuild server bundle %s: %s", absPath, result.Errors[0].Text)
	}

	if len(result.OutputFiles) == 0 {
		return "", nil, fmt.Errorf("esbuild produced no server bundle for %s", absPath)
	}

	deps, err := bundleInputs(result.Metafile)
	if err != nil {
		return "", nil, fmt.Errorf("parse server metafile %s: %w", absPath, err)
	}

	deps = filterOutPath(deps, entryPath)

	return string(result.OutputFiles[0].Contents), deps, nil
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
		Format:           api.FormatESModule,
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
		return "", nil, fmt.Errorf("esbuild client bundle %s: %s", absPath, result.Errors[0].Text)
	}

	if len(result.OutputFiles) == 0 {
		return "", nil, fmt.Errorf("esbuild produced no client bundle for %s", absPath)
	}

	deps, err := bundleInputs(result.Metafile)
	if err != nil {
		return "", nil, fmt.Errorf("parse client metafile %s: %w", absPath, err)
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
		return "", nil, fmt.Errorf("component not found %s: %w", absPath, err)
	}

	dir := filepath.Dir(absPath)
	cssPath := filepath.Join(dir, "app.css")
	cssInfo, err := os.Stat(cssPath)
	if err != nil {
		return "", nil, fmt.Errorf("missing app.css at %s: %w", cssPath, err)
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
		return "", fmt.Errorf("tailwind build %s: %w: %s", cssPath, err, strings.TrimSpace(string(output)))
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
	bundleCache.RLock()
	entry := bundleCache.entries[path]
	bundleCache.RUnlock()
	if entry == nil || entryStale(entry) {
		return "", "", ""
	}
	return entry.serverJS, entry.clientByID[rootID], entry.css
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
	if timeout := currentRenderTimeout(); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	vm, err := jsPool.acquire(ctx)
	if err != nil {
		return "", fmt.Errorf("acquire runtime: %w", err)
	}
	defer jsPool.release(vm)

	vm.rt.SetInterruptHandler(makeInterruptHandler(ctx))
	defer vm.rt.ClearInterruptHandler()

	return runSSR(vm.ctx, jsCode, props)
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
		return "", fmt.Errorf("eval component bundle: %s", ctx.Exception())
	}
	defer result.Free()

	propsJSON, err := json.Marshal(props)
	if err != nil {
		return "", fmt.Errorf("marshal props: %w", err)
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
		return "", fmt.Errorf("render returned non-string: %s", renderResult.String())
	}

	return renderResult.String(), nil
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
		if err != nil || !info.ModTime().Equal(stamp.modTime) || info.Size() != stamp.size {
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
