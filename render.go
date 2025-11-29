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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buke/quickjs-go"
	"github.com/evanw/esbuild/pkg/api"
	"golang.org/x/sync/errgroup"
)

const (
	DefaultAppDir   = "app"
	DefaultPagesDir = "app/pages"
	DefaultDistDir  = "dist"
)

const (
	defaultRenderTimeout = 2 * time.Second
	quickjsStackSize     = 4 * 1024 * 1024
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
}

// Internal runtime types
type jsRuntime struct {
	rt  *quickjs.Runtime
	ctx *quickjs.Context
}

type runtimePool struct {
	sem  chan struct{}
	size int
}

// Internal cache types
type bundleCacheEntry struct {
	serverJS   string
	clientByID map[string]string
	css        string
	prebuilt   bool
}

type manifestEntry struct {
	Server string   `json:"server"`
	Client string   `json:"client,omitempty"`
	CSS    string   `json:"css"`
	Chunks []string `json:"chunks,omitempty"`
}

// Internal handler types
type assetRoot struct {
	prefix     string
	fs         fs.FS
	fileServer http.Handler
}

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

var (
	renderTimeout atomic.Value
	jsPool        *runtimePool
	globalConfig  atomic.Value
)

type Config struct {
	FS              fs.FS
	DefaultTitle    string
	DefaultMeta     []metaTag
	AppDir          string
	PagesDir        string
	DistDir         string
	RenderTimeout   time.Duration
	RuntimePoolSize int
}

func init() {
	renderTimeout.Store(defaultRenderTimeout)
	jsPool = newRuntimePool(defaultRuntimePoolSize())
	globalConfig.Store(&Config{
		DefaultTitle:    "Alloy",
		AppDir:          DefaultAppDir,
		PagesDir:        DefaultPagesDir,
		DistDir:         DefaultDistDir,
		RenderTimeout:   defaultRenderTimeout,
		RuntimePoolSize: defaultRuntimePoolSize(),
	})
}

// Init configures global settings for Alloy.
func Init(filesystem fs.FS, options ...func(*Config)) {
	cfg := &Config{
		FS:              filesystem,
		DefaultTitle:    "Alloy",
		AppDir:          DefaultAppDir,
		PagesDir:        DefaultPagesDir,
		DistDir:         DefaultDistDir,
		RenderTimeout:   defaultRenderTimeout,
		RuntimePoolSize: defaultRuntimePoolSize(),
	}

	for _, opt := range options {
		if opt != nil {
			opt(cfg)
		}
	}

	if cfg.RenderTimeout > 0 {
		renderTimeout.Store(cfg.RenderTimeout)
	}

	if cfg.RuntimePoolSize > 0 && cfg.RuntimePoolSize != defaultRuntimePoolSize() {
		jsPool = newRuntimePool(cfg.RuntimePoolSize)
	}

	globalConfig.Store(cfg)
}

func getConfig() *Config {
	cfg, _ := globalConfig.Load().(*Config)
	return cfg
}

// PageHandler provides a fluent API for building page handlers.
type PageHandler struct {
	component string
	loader    func(r *http.Request) map[string]any
	ctx       func(r *http.Request) context.Context
}

// NewPage creates a new PageHandler for the given component path.
func NewPage(component string) *PageHandler {
	return &PageHandler{
		component: component,
	}
}

// WithLoader sets the loader function for this page.
func (h *PageHandler) WithLoader(loader func(r *http.Request) map[string]any) *PageHandler {
	h.loader = loader
	return h
}

// WithContext sets a custom context function for this page.
func (h *PageHandler) WithContext(ctx func(r *http.Request) context.Context) *PageHandler {
	h.ctx = ctx
	return h
}

// ServeHTTP implements http.Handler.
func (h *PageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	if cfg == nil || cfg.FS == nil {
		http.Error(w, "alloy not initialized: call alloy.Init(fs) first", http.StatusInternalServerError)
		return
	}

	if tryServeAsset(w, r, cfg.FS) {
		return
	}

	rootID := defaultRootID(h.component)
	props := map[string]any{}
	if h.loader != nil {
		props = h.loader(r)
	}

	ctx := r.Context()
	if h.ctx != nil {
		custom := h.ctx(r)
		if custom != nil {
			ctx = custom
		}
	}

	files, err := resolvePrebuiltFiles(cfg.FS, h.component, "", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if files.Server != "" {
		if err := RegisterPrebuiltBundleFromFS(h.component, rootID, cfg.FS, files); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ServePrebuiltPageWithContext(ctx, w, r, h.component, props, rootID, files)
		return
	}

	ServePageWithContext(ctx, w, r, h.component, props, rootID)
}

func tryServeAsset(w http.ResponseWriter, r *http.Request, filesystem fs.FS) bool {
	isAllowedMethod := r.Method == http.MethodGet || r.Method == http.MethodHead
	if !isAllowedMethod {
		return false
	}

	assetPath := normalizeAssetPath(r.URL.Path)
	if assetPath == "" {
		return false
	}

	roots := collectAssetRoots(filesystem)
	for _, root := range roots {
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
		return true
	}

	return false
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

	if entry == nil {
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
	bundleCache.Unlock()

	return nil
}

var bundleCache = struct {
	sync.RWMutex
	entries map[string]*bundleCacheEntry
}{
	entries: make(map[string]*bundleCacheEntry),
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

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
%s%s
</head>
<body>
	<div id="%s">%s</div>
	<script id="%s-props" type="application/json">%s</script>
	%s
</body>
</html>`, head, cssTag, rootID, r.HTML, rootID, string(propsJSON), scriptTag)
}

func (r *RenderResult) buildCSSTag() string {
	switch {
	case r.CSSPath != "":
		cssURL := r.CSSPath
		if !isHashedAsset(cssURL) {
			cssURL = fmt.Sprintf("%s?v=%d", cssURL, time.Now().UnixNano())
		}
		return fmt.Sprintf("\n\t<link rel=\"stylesheet\" href=\"%s\" />", cssURL)
	case r.CSS != "":
		return fmt.Sprintf("\n\t<style>%s</style>", r.CSS)
	}
	return ""
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

	serverJS, clientJS, css := readBundlesFromCache(absPath, rootID)
	if serverJS == "" || clientJS == "" || css == "" {
		return nil, fmt.Errorf("component %s (rootID=%s) not registered; run 'alloy dev' or 'alloy build' first", absPath, rootID)
	}

	html, err := executeSSR(ctx, serverJS, props)
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

	html, err := executeSSR(ctx, serverJS, props)
	if err != nil {
		return nil, fmt.Errorf("ssr failed for %s: %w", absPath, err)
	}

	paths := []string{ensureLeadingSlash(filepath.ToSlash(files.Client))}

	return &RenderResult{
		HTML:        html,
		ClientPaths: paths,
		CSSPath:     ensureLeadingSlash(filepath.ToSlash(files.CSS)),
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

	tmpDir, err := os.MkdirTemp("", "alloy-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmpDir)

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

	result := api.Build(api.BuildOptions{
		EntryPoints:      []string{entryPath},
		Bundle:           true,
		Write:            false,
		Metafile:         true,
		Format:           api.FormatIIFE,
		GlobalName:       "__Component",
		MinifyWhitespace: true,
		MinifySyntax:     true,
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

// SaveCSS writes a CSS bundle to disk.
func SaveCSS(css string, dir string, name string) (string, error) {
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

	if distFS, err := fs.Sub(filesystem, DefaultDistDir); err == nil {
		roots = append(roots, assetRoot{
			prefix:     DefaultDistDir,
			fs:         distFS,
			fileServer: http.FileServer(http.FS(distFS)),
		})
	}

	return roots
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

func resolvePrebuiltFiles(filesystem fs.FS, component string, distDir string, name string) (PrebuiltFiles, error) {
	dist := distDir
	if dist == "" {
		dist = DefaultDistDir
	}

	base := name
	if base == "" {
		componentBase := filepath.Base(component)
		base = strings.TrimSuffix(componentBase, filepath.Ext(componentBase))
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

	return PrebuiltFiles{
		Server:       path.Join(dist, entry.Server),
		Client:       path.Join(dist, client),
		ClientChunks: joinPaths(dist, entry.Chunks),
		CSS:          path.Join(dist, entry.CSS),
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

func RunTailwind(cssPath string, root string) (string, error) {
	outputFile, err := os.CreateTemp("", "alloy-tailwind-*.css")
	if err != nil {
		return "", fmt.Errorf("create temp css: %w", err)
	}
	outputPath := outputFile.Name()
	outputFile.Close()
	defer os.Remove(outputPath)

	args := []string{"-i", cssPath, "-o", outputPath, "--minify"}

	cmd, err := tailwindCmd("./", args...)
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

func tailwindCmd(root string, args ...string) (*exec.Cmd, error) {
	runner, baseArgs := ResolveTailwindRunner(root)
	if runner == "" {
		return nil, fmt.Errorf("tailwind runner not found")
	}
	fullArgs := append(baseArgs, args...)
	cmd := exec.Command(runner, fullArgs...)
	cmd.Dir = root
	return cmd, nil
}

func ResolveTailwindRunner(root string) (string, []string) {
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
		return "pnpx", []string{"@tailwindcss/cli"}
	case "npm", "npx":
		return "npx", []string{"@tailwindcss/cli"}
	case "yarn":
		return "yarn", []string{"@tailwindcss/cli"}
	case "bun", "bunx":
		return "bunx", []string{"@tailwindcss/cli"}
	default:
		return name, []string{"@tailwindcss/cli"}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// PageSpec defines a page component for building.
type PageSpec struct {
	Component string
	Name      string
	RootID    string
}

// BuildDevBundles performs initial dev build of all bundles.
func BuildDevBundles(pages []PageSpec, distDir string) error {
	if len(pages) == 0 {
		return fmt.Errorf("no pages provided")
	}
	if distDir == "" {
		return fmt.Errorf("distDir required")
	}

	cwd, _ := os.Getwd()

	for _, page := range pages {
		serverJS, _, err := BuildServerBundle(page.Component)
		if err != nil {
			return fmt.Errorf("build server %s: %w", page.Name, err)
		}

		serverPath := filepath.Join(distDir, fmt.Sprintf("%s-server.js", page.Name))
		if err := os.WriteFile(serverPath, []byte(serverJS), 0644); err != nil {
			return fmt.Errorf("write server %s: %w", page.Name, err)
		}
	}

	tmpClientDir, err := os.MkdirTemp("", "alloy-initial-client-")
	if err != nil {
		return fmt.Errorf("create temp client dir: %w", err)
	}
	defer os.RemoveAll(tmpClientDir)

	clientEntries := make([]api.EntryPoint, 0, len(pages))
	for _, page := range pages {
		absComponent, err := filepath.Abs(page.Component)
		if err != nil {
			return fmt.Errorf("resolve component path: %w", err)
		}

		wrapperCode := fmt.Sprintf(`
import { hydrateRoot } from 'react-dom/client';
import Component from '%s';

const propsEl = document.getElementById('%s-props');
const props = propsEl ? JSON.parse(propsEl.textContent || '{}') : {};
const rootEl = document.getElementById('%s');

if (rootEl) {
	hydrateRoot(rootEl, <Component {...props} />);
}
		`, absComponent, page.RootID, page.RootID)

		entryPath := filepath.Join(tmpClientDir, page.Name+".tsx")
		if err := os.WriteFile(entryPath, []byte(wrapperCode), 0644); err != nil {
			return fmt.Errorf("write client entry: %w", err)
		}

		clientEntries = append(clientEntries, api.EntryPoint{
			InputPath:  entryPath,
			OutputPath: page.Name,
		})
	}

	result := api.Build(api.BuildOptions{
		EntryPointsAdvanced: clientEntries,
		Outdir:              distDir,
		Bundle:              true,
		Splitting:           true,
		Format:              api.FormatESModule,
		Write:               true,
		JSX:                 api.JSXAutomatic,
		JSXImportSource:     "react",
		MainFields:          []string{"browser", "module", "main"},
		Target:              api.ES2020,
		EntryNames:          "[name]-client",
		ChunkNames:          "chunk-[hash]",
		MinifyWhitespace:    false,
		MinifySyntax:        false,
		NodePaths:           []string{filepath.Join(cwd, "node_modules")},
	})

	if len(result.Errors) > 0 {
		return fmt.Errorf("build client: %s", result.Errors[0].Text)
	}

	if err := writeDevManifest(pages, distDir); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	return nil
}

func writeDevManifest(pages []PageSpec, distDir string) error {
	manifestPath := filepath.Join(distDir, "manifest.json")
	manifest := make(map[string]map[string]any)

	for _, page := range pages {
		manifest[page.Name] = map[string]any{
			"server": fmt.Sprintf("%s-server.js", page.Name),
			"client": fmt.Sprintf("%s-client.js", page.Name),
			"css":    "shared.css",
		}
	}

	data, err := os.ReadFile(manifestPath)
	if err == nil {
		var existing map[string]map[string]any
		if err := json.Unmarshal(data, &existing); err == nil {
			for name, entry := range existing {
				if _, ok := manifest[name]; !ok {
					manifest[name] = entry
				}
			}
		}
	}

	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(manifestPath, manifestData, 0644)
}

// WatchTailwind runs Tailwind in watch mode.
func WatchTailwind(ctx context.Context, inputPath, outputPath, cwd string) *exec.Cmd {
	runner, baseArgs := ResolveTailwindRunner(cwd)
	if runner == "" {
		return nil
	}

	absInput, _ := filepath.Abs(inputPath)
	absOutput, _ := filepath.Abs(outputPath)

	args := append(baseArgs, "-i", absInput, "-o", absOutput, "--watch=always")

	cmd := exec.CommandContext(ctx, runner, args...)
	cmd.Dir = cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd
}

// WatchAndBuild watches for changes and rebuilds bundles in dev mode.
func WatchAndBuild(ctx context.Context, pages []PageSpec, distDir string, buildDone chan<- struct{}) error {
	cssPath := filepath.Join(DefaultAppDir, "app.css")
	cwd, _ := os.Getwd()

	if err := BuildDevBundles(pages, distDir); err != nil {
		return fmt.Errorf("initial build: %w", err)
	}

	if buildDone != nil {
		close(buildDone)
	}

	serverTmpDir, err := os.MkdirTemp("", "alloy-server-")
	if err != nil {
		return fmt.Errorf("create server temp dir: %w", err)
	}
	defer os.RemoveAll(serverTmpDir)

	var g errgroup.Group

	type serverCtxInfo struct {
		ctx api.BuildContext
		tmp string
	}
	serverCtxs := make([]serverCtxInfo, 0, len(pages))

	for _, page := range pages {
		absComponent, err := filepath.Abs(page.Component)
		if err != nil {
			return fmt.Errorf("resolve component path: %w", err)
		}

		entryCode := fmt.Sprintf(`
import { renderToString } from 'react-dom/server.edge';
import Component from '%s';

export default function render(props: any) {
	return renderToString(<Component {...props} />);
}
		`, absComponent)

		entryPath := filepath.Join(serverTmpDir, page.Name+"-entry.tsx")
		if err := os.WriteFile(entryPath, []byte(entryCode), 0644); err != nil {
			return fmt.Errorf("write entry: %w", err)
		}

		outPath := filepath.Join(distDir, fmt.Sprintf("%s-server.js", page.Name))

		buildCtx, err := api.Context(api.BuildOptions{
			EntryPoints:      []string{entryPath},
			Bundle:           true,
			Write:            true,
			Outfile:          outPath,
			Format:           api.FormatIIFE,
			GlobalName:       "__Component",
			Target:           api.ES2020,
			JSX:              api.JSXAutomatic,
			JSXImportSource:  "react",
			NodePaths:        []string{filepath.Join(cwd, "node_modules")},
			Platform:         api.PlatformBrowser,
			MainFields:       []string{"browser", "module", "main"},
			MinifyWhitespace: false,
			MinifySyntax:     false,
		})

		if ctxErr, ok := err.(*api.ContextError); ok && ctxErr != nil {
			return fmt.Errorf("create server context %s: %v", page.Name, err)
		}

		serverCtxs = append(serverCtxs, serverCtxInfo{ctx: buildCtx, tmp: entryPath})

		watchCtx := buildCtx
		pageName := page.Name
		g.Go(func() error {
			err := watchCtx.Watch(api.WatchOptions{})
			if err != nil {
				return fmt.Errorf("watch server %s: %w", pageName, err)
			}

			<-ctx.Done()
			return nil
		})
	}

	defer func() {
		for _, info := range serverCtxs {
			info.ctx.Dispose()
		}
	}()

	go func() {
		<-ctx.Done()
		for _, info := range serverCtxs {
			info.ctx.Dispose()
		}
	}()

	tmpClientDir, err := os.MkdirTemp("", "alloy-client-")
	if err != nil {
		return fmt.Errorf("create client temp: %w", err)
	}
	defer os.RemoveAll(tmpClientDir)

	clientEntries := make([]api.EntryPoint, 0, len(pages))
	for _, page := range pages {
		absComponent, err := filepath.Abs(page.Component)
		if err != nil {
			return fmt.Errorf("resolve component path: %w", err)
		}

		wrapperCode := fmt.Sprintf(`
import { hydrateRoot } from 'react-dom/client';
import Component from '%s';

const propsEl = document.getElementById('%s-props');
const props = propsEl ? JSON.parse(propsEl.textContent || '{}') : {};
const rootEl = document.getElementById('%s');

if (rootEl) {
	hydrateRoot(rootEl, <Component {...props} />);
}
		`, absComponent, page.RootID, page.RootID)

		entryPath := filepath.Join(tmpClientDir, page.Name+".tsx")
		if err := os.WriteFile(entryPath, []byte(wrapperCode), 0644); err != nil {
			return fmt.Errorf("write client entry: %w", err)
		}

		clientEntries = append(clientEntries, api.EntryPoint{
			InputPath:  entryPath,
			OutputPath: page.Name,
		})
	}

	clientCtx, err := api.Context(api.BuildOptions{
		EntryPointsAdvanced: clientEntries,
		Outdir:              distDir,
		Bundle:              true,
		Splitting:           true,
		Format:              api.FormatESModule,
		Write:               true,
		JSX:                 api.JSXAutomatic,
		JSXImportSource:     "react",
		MainFields:          []string{"browser", "module", "main"},
		Target:              api.ES2020,
		EntryNames:          "[name]-client",
		ChunkNames:          "chunk-[hash]",
		MinifyWhitespace:    false,
		MinifySyntax:        false,
		NodePaths:           []string{filepath.Join(cwd, "node_modules")},
	})
	if ctxErr, ok := err.(*api.ContextError); ok && ctxErr != nil {
		return fmt.Errorf("create client context: %v", err)
	}
	defer clientCtx.Dispose()

	go func() {
		<-ctx.Done()
		clientCtx.Dispose()
	}()

	g.Go(func() error {
		err := clientCtx.Watch(api.WatchOptions{})
		if err != nil {
			return fmt.Errorf("watch client: %w", err)
		}

		<-ctx.Done()
		return nil
	})

	g.Go(func() error {
		cmd := WatchTailwind(ctx, cssPath, filepath.Join(distDir, "shared.css"), cwd)
		if cmd == nil {
			return fmt.Errorf("tailwind runner not found")
		}

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start tailwind: %w", err)
		}

		go func() {
			<-ctx.Done()
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		}()

		return cmd.Wait()
	})

	if err := writeDevManifest(pages, distDir); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	return g.Wait()
}

// DiscoverPages finds all .tsx files in a directory.
func DiscoverPages(dir string) ([]PageSpec, error) {
	pattern := filepath.Join(dir, "*.tsx")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("find pages: %w", err)
	}

	var pages []PageSpec
	for _, match := range matches {
		base := filepath.Base(match)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		if name == "" {
			continue
		}
		pages = append(pages, PageSpec{
			Component: match,
			Name:      name,
			RootID:    defaultRootID(name),
		})
	}

	return pages, nil
}

func readBundlesFromCache(path string, rootID string) (string, string, string) {
	bundleCache.RLock()
	entry := bundleCache.entries[path]
	bundleCache.RUnlock()

	if entry != nil {
		return entry.serverJS, entry.clientByID[rootID], entry.css
	}

	return "", "", ""
}

// executeSSR runs the JS code in QuickJS using the runtime pool.
func executeSSR(ctx context.Context, jsCode string, props map[string]any) (string, error) {
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
