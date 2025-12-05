package alloy

import (
	"context"
	"crypto/sha1"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buke/quickjs-go"
	"github.com/evanw/esbuild/pkg/api"
	"golang.org/x/sync/errgroup"
)

//go:embed assets/*
var assetsFS embed.FS

var (
	polyfillsSource     string
	htmlTemplate        string
	entryTemplate       string
	clientEntryTemplate string
	renderTemplate      string
	renderTimeout       atomic.Value
	globalConfig        atomic.Value
)

const (
	DefaultAppDir   = "app"
	DefaultPagesDir = "app/pages"
	DefaultDistDir  = "dist/build"

	defaultRenderTimeout = 2 * time.Second
	quickjsStackSize     = 4 * 1024 * 1024
)

type jsRuntime struct {
	rt  *quickjs.Runtime
	ctx *quickjs.Context
}

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

type assetRoot struct {
	prefix     string
	fs         fs.FS
	fileServer http.Handler
}

type serverCtxInfo struct {
	ctx api.BuildContext
	tmp string
}

type PrebuiltFiles struct {
	Server       string
	Client       string
	ClientChunks []string
	CSS          string
}

type RenderResult struct {
	HTML        string
	ClientJS    string
	CSS         string
	Props       map[string]any
	ClientPath  string
	ClientPaths []string
	CSSPath     string
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

type HeadTag struct {
	Tag   string
	Attrs map[string]string
	Text  string
}

type Config struct {
	FS            fs.FS
	DefaultTitle  string
	DefaultMeta   []HeadTag
	AppDir        string
	PagesDir      string
	DistDir       string
	RenderTimeout time.Duration
}

type PageHandler struct {
	component string
	loader    func(r *http.Request) map[string]any
	ctx       func(r *http.Request) context.Context
}

type PageSpec struct {
	Component string
	Name      string
	RootID    string
}

func init() {
	loadEmbeddedAssets()
	renderTimeout.Store(defaultRenderTimeout)
	globalConfig.Store(&Config{
		DefaultTitle:  "Alloy",
		AppDir:        DefaultAppDir,
		PagesDir:      DefaultPagesDir,
		DistDir:       DefaultDistDir,
		RenderTimeout: defaultRenderTimeout,
	})
}

func loadEmbeddedAssets() {
	polyfillsSource = MustReadAsset("assets/polyfills.js")
	htmlTemplate = MustReadAsset("assets/html-template.html")
	entryTemplate = MustReadAsset("assets/server-entry.tsx")
	clientEntryTemplate = MustReadAsset("assets/client-entry.tsx")
	renderTemplate = MustReadAsset("assets/render-invoke.js")
}

func MustReadAsset(path string) string {
	data, _ := assetsFS.ReadFile(path)
	return string(data)
}

func Init(filesystem fs.FS, options ...func(*Config)) {
	if os.Getenv("ALLOY_DEV") == "1" {
		filesystem = os.DirFS(".")
	}

	cfg := &Config{
		FS:            filesystem,
		DefaultTitle:  "Alloy",
		AppDir:        DefaultAppDir,
		PagesDir:      DefaultPagesDir,
		DistDir:       DefaultDistDir,
		RenderTimeout: defaultRenderTimeout,
	}

	for _, opt := range options {
		if opt != nil {
			opt(cfg)
		}
	}

	if cfg.RenderTimeout > 0 {
		renderTimeout.Store(cfg.RenderTimeout)
	}

	globalConfig.Store(cfg)
}

func getConfig() *Config {
	cfg, _ := globalConfig.Load().(*Config)
	return cfg
}

func NewPage(component string) *PageHandler {
	return &PageHandler{
		component: component,
	}
}

func (h *PageHandler) WithLoader(loader func(r *http.Request) map[string]any) *PageHandler {
	h.loader = loader
	return h
}

func (h *PageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()

	if tryServeAsset(w, r, cfg.FS) {
		return
	}

	rootID := defaultRootID(h.component)
	props := map[string]any{}
	if h.loader != nil {
		props = h.loader(r)
	}

	files, err := resolvePrebuiltFiles(cfg.FS, h.component)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if files.Server != "" {
		if err := RegisterPrebuiltBundleFromFS(h.component, rootID, cfg.FS, files); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ServePrebuiltPageWithContext(w, r, h.component, props, rootID, files)
		return
	}

	ServePageWithContext(w, r, h.component, props, rootID)
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

func loadPolyfills(ctx *quickjs.Context) error {
	result := ctx.Eval(polyfillsSource)
	if result.IsException() {
		return fmt.Errorf("🔴 polyfills: %s", ctx.Exception().Error())
	}
	result.Free()
	return nil
}

func ServePage(w http.ResponseWriter, r *http.Request, componentPath string, props map[string]any, rootID string) {
	result, err := RenderTSXFileWithHydrationWithContext(r.Context(), componentPath, props, rootID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, result.ToHTML(rootID))
}

func ServePageWithContext(w http.ResponseWriter, r *http.Request, componentPath string, props map[string]any, rootID string) {
	result, err := RenderTSXFileWithHydrationWithContext(r.Context(), componentPath, props, rootID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, result.ToHTML(rootID))
}

func ServePrebuiltPage(w http.ResponseWriter, r *http.Request, componentPath string, props map[string]any, rootID string, files PrebuiltFiles) {
	result, err := RenderPrebuiltWithContext(r.Context(), componentPath, props, rootID, files)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, result.ToHTML(rootID))
}

func RegisterPrebuiltBundle(componentPath string, rootID string, serverJS string, clientJS string, css string) error {
	if componentPath == "" || rootID == "" {
		return fmt.Errorf("🔴 component path and root id required")
	}
	if serverJS == "" || clientJS == "" || css == "" {
		return fmt.Errorf("🔴 prebuilt assets cannot be empty")
	}

	absPath, err := resolveAbsPath(componentPath, "component path")
	if err != nil {
		return err
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

func (r *RenderResult) ToHTML(rootID string) string {
	if r.ClientJS == "" && r.ClientPath == "" && len(r.ClientPaths) == 0 {
		return r.HTML
	}

	propsJSON, err := json.Marshal(r.Props)
	if err != nil {
		propsJSON = []byte("{}")
	}
	head := buildHead(r.Props)
	cssTag := r.buildCSSTag()
	scriptTag := r.buildScriptTag()

	return fmt.Sprintf(htmlTemplate, head, cssTag, rootID, r.HTML, rootID, string(propsJSON), scriptTag)
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
	timestamp := time.Now().UnixNano()
	switch {
	case len(r.ClientPaths) > 0:
		var b strings.Builder
		for _, p := range r.ClientPaths {
			scriptURL := p
			if !isHashedAsset(scriptURL) {
				scriptURL = fmt.Sprintf("%s?v=%d", scriptURL, timestamp)
			}
			fmt.Fprintf(&b, "<script type=\"module\" src=\"%s\"></script>\n", scriptURL)
		}
		return strings.TrimSuffix(b.String(), "\n")
	case r.ClientPath != "":
		scriptURL := r.ClientPath
		if !isHashedAsset(scriptURL) {
			scriptURL = fmt.Sprintf("%s?v=%d", scriptURL, timestamp)
		}
		return fmt.Sprintf(`<script type="module" src="%s"></script>`, scriptURL)
	case r.ClientJS != "":
		return fmt.Sprintf(`<script type="module">%s</script>`, r.ClientJS)
	}
	return ""
}

func buildHead(props map[string]any) string {
	var b strings.Builder

	b.WriteString("\t<meta charset=\"UTF-8\">\n")
	b.WriteString("\t<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">\n")

	title := stringFromMap(props, "title")
	if title == "" {
		title = "Alloy"
	}
	fmt.Fprintf(&b, "\t<title>%s</title>", html.EscapeString(title))

	if meta, ok := props["meta"].([]any); ok {
		for _, tag := range parseMetaTags(meta) {
			fmt.Fprintf(&b, "\n\t<%s", tag.Tag)
			for k, v := range tag.Attrs {
				fmt.Fprintf(&b, " %s=\"%s\"", k, html.EscapeString(v))
			}
			b.WriteString(">")
		}
	}

	return b.String()
}

func parseMetaTags(meta []any) []HeadTag {
	var tags []HeadTag
	for _, item := range meta {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		tag := stringFromMap(m, "tag")
		if tag == "" {
			tag = "meta"
		}

		attrs := make(map[string]string)
		for k, v := range m {
			if k == "tag" {
				continue
			}
			if s, ok := v.(string); ok {
				attrs[k] = s
			}
		}

		if len(attrs) > 0 {
			tags = append(tags, HeadTag{Tag: tag, Attrs: attrs})
		}
	}
	return tags
}

func stringFromMap(m map[string]any, key string) string {
	str, _ := m[key].(string)
	return str
}

func RenderTSXFileWithHydrationWithContext(ctx context.Context, filePath string, props map[string]any, rootID string) (*RenderResult, error) {
	absPath, err := resolveAbsPath(filePath, "component path")
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(absPath); err != nil {
		return nil, fmt.Errorf("🔴 component not found %s: %w", absPath, err)
	}

	serverJS, clientJS, css := readBundlesFromCache(absPath, rootID)
	if serverJS == "" || clientJS == "" || css == "" {
		return nil, fmt.Errorf("🔴 component %s (rootID=%s) not registered; run 'alloy dev' or 'alloy build' first", absPath, rootID)
	}

	html, err := executeSSR(ctx, serverJS, props)
	if err != nil {
		return nil, fmt.Errorf("🔴 ssr failed for %s: %w", absPath, err)
	}

	return &RenderResult{
		HTML:     html,
		ClientJS: clientJS,
		CSS:      css,
		Props:    props,
	}, nil
}

func RenderPrebuiltWithContext(ctx context.Context, filePath string, props map[string]any, rootID string, files PrebuiltFiles) (*RenderResult, error) {
	if files.Server == "" || files.Client == "" || files.CSS == "" {
		return nil, fmt.Errorf("🔴 prebuilt file paths required")
	}

	absPath, err := resolveAbsPath(filePath, "component path")
	if err != nil {
		return nil, err
	}

	serverJS, clientJS, css := readBundlesFromCache(absPath, rootID)
	if serverJS == "" || clientJS == "" || css == "" {
		return nil, fmt.Errorf("🔴 component %s (rootID=%s) not registered; call RegisterPrebuiltBundleFromFS before serving", absPath, rootID)
	}

	html, err := executeSSR(ctx, serverJS, props)
	if err != nil {
		return nil, fmt.Errorf("🔴 ssr failed for %s: %w", absPath, err)
	}

	paths := []string{ensureLeadingSlash(filepath.ToSlash(files.Client))}

	return &RenderResult{
		HTML:        html,
		ClientPaths: paths,
		CSSPath:     ensureLeadingSlash(filepath.ToSlash(files.CSS)),
		Props:       props,
	}, nil
}

func generateServerEntryCode(componentPath string) string {
	return fmt.Sprintf(entryTemplate, componentPath)
}

func generateClientEntryCode(componentPath, rootID string) string {
	return fmt.Sprintf(clientEntryTemplate, componentPath, rootID, rootID)
}

func BuildServerBundle(filePath string) (string, []string, error) {
	absPath, err := resolveAbsPath(filePath, "component path")
	if err != nil {
		return "", nil, err
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", nil, fmt.Errorf("🔴 component not found %s: %w", absPath, err)
	}

	tmpDir, err := os.MkdirTemp("", "alloy-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(tmpDir)

	entryCode := generateServerEntryCode(absPath)

	entryPath := filepath.Join(tmpDir, "entry.tsx")
	if err := os.WriteFile(entryPath, []byte(entryCode), 0644); err != nil {
		return "", nil, err
	}

	opts := commonBuildOptions()
	opts.EntryPoints = []string{entryPath}
	opts.Write = false
	opts.Metafile = true
	opts.Format = api.FormatIIFE
	opts.GlobalName = "__Component"
	opts.Platform = api.PlatformBrowser

	result := api.Build(opts)

	if err := checkBuildErrors(result, fmt.Sprintf("esbuild server bundle %s", absPath)); err != nil {
		return "", nil, err
	}

	if len(result.OutputFiles) == 0 {
		return "", nil, fmt.Errorf("🔴 esbuild produced no server bundle for %s", absPath)
	}

	deps, err := bundleInputs(result.Metafile)
	if err != nil {
		return "", nil, fmt.Errorf("🔴 parse server metafile %s: %w", absPath, err)
	}

	deps = filterOutPath(deps, entryPath)

	return string(result.OutputFiles[0].Contents), deps, nil
}

func writeManifestEntry(dir string, name string, files *PrebuiltFiles) error {
	if files == nil {
		return fmt.Errorf("🔴 files required")
	}

	path := filepath.Join(dir, "manifest.json")
	updates := map[string]manifestEntry{
		name: {
			Server: filepath.Base(files.Server),
			Client: filepath.Base(files.Client),
			CSS:    filepath.Base(files.CSS),
			Chunks: baseNames(files.ClientChunks),
		},
	}

	return updateManifest(path, updates)
}

func updateManifest(manifestPath string, updates map[string]manifestEntry) error {
	manifest := map[string]manifestEntry{}

	if data, err := os.ReadFile(manifestPath); err == nil {
		if err := json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("🔴 decode manifest: %w", err)
		}
	}

	maps.Copy(manifest, updates)

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("🔴 encode manifest: %w", err)
	}

	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		return fmt.Errorf("🔴 write manifest file: %w", err)
	}

	return nil
}

func WriteManifest(dir string, name string, files PrebuiltFiles) error {
	return writeManifestEntry(dir, name, &files)
}

func SaveServerBundle(serverJS string, dir string, name string) (*PrebuiltFiles, error) {
	if serverJS == "" {
		return nil, fmt.Errorf("🔴 server required")
	}
	if dir == "" || name == "" {
		return nil, fmt.Errorf("🔴 dir and name required")
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("🔴 make dir: %w", err)
	}

	serverHash := shortHash(serverJS)

	files := &PrebuiltFiles{
		Server: filepath.Join(dir, fmt.Sprintf("%s-%s-server.js", name, serverHash)),
	}

	if err := os.WriteFile(files.Server, []byte(serverJS), 0644); err != nil {
		return nil, fmt.Errorf("🔴 write server bundle: %w", err)
	}

	return files, nil
}

func SaveCSS(css string, dir string, name string) (string, error) {
	if css == "" {
		return "", fmt.Errorf("🔴 css required")
	}
	if dir == "" {
		return "", fmt.Errorf("🔴 dir required")
	}
	if name == "" {
		name = "shared"
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("🔴 make dir: %w", err)
	}

	cssHash := shortHash(css)
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.css", name, cssHash))
	if err := os.WriteFile(path, []byte(css), 0644); err != nil {
		return "", fmt.Errorf("🔴 write shared css: %w", err)
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

func RegisterPrebuiltBundleFromFS(componentPath string, rootID string, filesystem fs.FS, files PrebuiltFiles) error {
	if filesystem == nil {
		return fmt.Errorf("🔴 filesystem required")
	}
	if files.Server == "" || files.Client == "" || files.CSS == "" {
		return fmt.Errorf("🔴 prebuilt file paths required")
	}

	readFS := filesystem
	serverBytes, err := fs.ReadFile(readFS, files.Server)
	if errors.Is(err, fs.ErrNotExist) {
		readFS = os.DirFS(".")
		serverBytes, err = fs.ReadFile(readFS, files.Server)
	}
	if err != nil {
		return fmt.Errorf("🔴 read server bundle: %w", err)
	}

	clientBytes, err := fs.ReadFile(readFS, files.Client)
	if err != nil {
		return fmt.Errorf("🔴 read client bundle: %w", err)
	}
	cssBytes, err := fs.ReadFile(readFS, files.CSS)
	if err != nil {
		return fmt.Errorf("🔴 read css: %w", err)
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

func BuildClientBundles(entries []ClientEntry, outDir string) (map[string]ClientAssets, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("🔴 entries required")
	}
	if outDir == "" {
		return nil, fmt.Errorf("🔴 out dir required")
	}

	absOut, err := resolveAbsPath(outDir, "out dir")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absOut, 0755); err != nil {
		return nil, fmt.Errorf("🔴 make out dir: %w", err)
	}

	entryPoints := make(map[string]ClientEntry, len(entries))

	tmpDir, err := os.MkdirTemp("", "alloy-clients-")
	if err != nil {
		return nil, fmt.Errorf("🔴 make temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	for _, e := range entries {
		if e.Name == "" || e.Component == "" {
			return nil, fmt.Errorf("🔴 entry name and component required")
		}
		rootID := e.RootID
		if rootID == "" {
			rootID = defaultRootID(e.Component)
		}

		absPath, err := resolveAbsPath(e.Component, fmt.Sprintf("entry %s", e.Name))
		if err != nil {
			return nil, err
		}

		wrapper := generateClientEntryCode(absPath, rootID)

		entryPath := filepath.Join(tmpDir, e.Name+".tsx")
		if err := os.WriteFile(entryPath, []byte(wrapper), 0644); err != nil {
			return nil, fmt.Errorf("🔴 write entry %s: %w", e.Name, err)
		}

		entryPoints[e.Name] = ClientEntry{
			Name:      e.Name,
			Component: entryPath,
			RootID:    rootID,
		}
	}

	cwd, _ := os.Getwd()

	opts := commonBuildOptions()
	opts.EntryPointsAdvanced = toEntryPoints(entryPoints)
	opts.Outdir = absOut
	opts.Splitting = true
	opts.Format = api.FormatESModule
	opts.Write = true
	opts.Metafile = true
	opts.EntryNames = "client-[name]-[hash]"
	opts.ChunkNames = "chunk-[hash]"

	result := api.Build(opts)

	if err := checkBuildErrors(result, "client build error"); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("🔴 parse metafile: %w", err)
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
			return nil, fmt.Errorf("🔴 entry rel: %w", err)
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
			return nil, fmt.Errorf("🔴 missing client bundle for %s", name)
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

func commonBuildOptions() api.BuildOptions {
	cwd, _ := os.Getwd()
	return api.BuildOptions{
		Bundle:           true,
		JSX:              api.JSXAutomatic,
		JSXImportSource:  "react",
		Target:           api.ES2020,
		MainFields:       []string{"browser", "module", "main"},
		NodePaths:        []string{filepath.Join(cwd, "node_modules")},
		MinifyWhitespace: true,
		MinifySyntax:     true,
	}
}

func disableMinify(opts *api.BuildOptions) {
	opts.MinifyWhitespace = false
	opts.MinifySyntax = false
}

func checkBuildErrors(result api.BuildResult, context string) error {
	if len(result.Errors) > 0 {
		return fmt.Errorf("🔴 %s: %s", context, result.Errors[0].Text)
	}
	return nil
}

func checkContextError(err error, context string) error {
	if ctxErr, ok := err.(*api.ContextError); ok && ctxErr != nil {
		return fmt.Errorf("🔴 %s: %v", context, err)
	}
	return nil
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

var hashPattern = regexp.MustCompile(`-[a-fA-F0-9]{8,}\.`)

func isHashedAsset(assetPath string) bool {
	return hashPattern.MatchString(filepath.Base(assetPath))
}

func normalizeAssetPath(requestPath string) string {
	clean := path.Clean(strings.TrimPrefix(requestPath, "/"))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "..") {
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

func resolveAbsPath(path, context string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("🔴 resolve %s: %w", context, err)
	}
	return abs, nil
}

func mustResolveAbsPath(path string) string {
	abs, _ := filepath.Abs(path)
	return abs
}

func resolvePrebuiltFiles(filesystem fs.FS, component string) (PrebuiltFiles, error) {
	dist := DefaultDistDir
	componentBase := filepath.Base(component)
	base := strings.TrimSuffix(componentBase, filepath.Ext(componentBase))

	if manifestFiles, ok, err := lookupManifest(filesystem, dist, base); err != nil {
		return PrebuiltFiles{}, err
	} else if ok {
		return manifestFiles, nil
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
		return PrebuiltFiles{}, false, fmt.Errorf("🔴 read manifest: %w", err)
	}

	manifest := map[string]manifestEntry{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PrebuiltFiles{}, false, fmt.Errorf("🔴 decode manifest: %w", err)
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
		return "", fmt.Errorf("🔴 create temp css: %w", err)
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
		return "", fmt.Errorf("🔴 tailwind build %s: %w: %s", cssPath, err, strings.TrimSpace(string(output)))
	}

	css, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("🔴 read css output: %w", err)
	}

	return string(css), nil
}

func tailwindCmd(root string, args ...string) (*exec.Cmd, error) {
	runner, baseArgs := ResolveTailwindRunner(root)
	if runner == "" {
		return nil, fmt.Errorf("🔴 tailwind runner not found")
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

func BuildDevBundles(pages []PageSpec, distDir string) error {
	if len(pages) == 0 {
		return fmt.Errorf("🔴 no pages provided")
	}
	if distDir == "" {
		return fmt.Errorf("🔴 distDir required")
	}

	for _, page := range pages {
		serverJS, _, err := BuildServerBundle(page.Component)
		if err != nil {
			return fmt.Errorf("🔴 build server %s: %w", page.Name, err)
		}

		serverPath := filepath.Join(distDir, fmt.Sprintf("%s-server.js", page.Name))
		if err := os.WriteFile(serverPath, []byte(serverJS), 0644); err != nil {
			return fmt.Errorf("🔴 write server %s: %w", page.Name, err)
		}
	}

	tmpClientDir, err := os.MkdirTemp("", "alloy-initial-client-")
	if err != nil {
		return fmt.Errorf("🔴 create temp client dir: %w", err)
	}
	defer os.RemoveAll(tmpClientDir)

	clientEntries := make([]api.EntryPoint, 0, len(pages))
	for _, page := range pages {
		absComponent, err := resolveAbsPath(page.Component, "component path")
		if err != nil {
			return err
		}

		wrapperCode := generateClientEntryCode(absComponent, page.RootID)

		entryPath := filepath.Join(tmpClientDir, page.Name+".tsx")
		if err := os.WriteFile(entryPath, []byte(wrapperCode), 0644); err != nil {
			return fmt.Errorf("🔴 write client entry: %w", err)
		}

		clientEntries = append(clientEntries, api.EntryPoint{
			InputPath:  entryPath,
			OutputPath: page.Name,
		})
	}

	opts := commonBuildOptions()
	opts.EntryPointsAdvanced = clientEntries
	opts.Outdir = distDir
	opts.Splitting = true
	opts.Format = api.FormatESModule
	opts.Write = true
	opts.EntryNames = "[name]-client"
	opts.ChunkNames = "chunk-[hash]"
	disableMinify(&opts)

	result := api.Build(opts)

	if err := checkBuildErrors(result, "build client"); err != nil {
		return err
	}

	if err := writeDevManifest(pages, distDir); err != nil {
		return fmt.Errorf("🔴 write manifest: %w", err)
	}

	return nil
}

func writeDevManifest(pages []PageSpec, distDir string) error {
	manifestPath := filepath.Join(distDir, "manifest.json")

	existingManifest := map[string]manifestEntry{}
	if data, err := os.ReadFile(manifestPath); err == nil {
		json.Unmarshal(data, &existingManifest)
	}

	updates := make(map[string]manifestEntry, len(pages))
	for _, page := range pages {
		updates[page.Name] = manifestEntry{
			Server: fmt.Sprintf("%s-server.js", page.Name),
			Client: fmt.Sprintf("%s-client.js", page.Name),
			CSS:    "shared.css",
		}
	}

	for name, entry := range existingManifest {
		if _, exists := updates[name]; !exists {
			updates[name] = entry
		}
	}

	return updateManifest(manifestPath, updates)
}

func WatchTailwind(ctx context.Context, inputPath, outputPath, cwd string) *exec.Cmd {
	runner, baseArgs := ResolveTailwindRunner(cwd)
	if runner == "" {
		return nil
	}

	absInput := mustResolveAbsPath(inputPath)
	absOutput := mustResolveAbsPath(outputPath)

	args := append(baseArgs, "-i", absInput, "-o", absOutput, "--watch=always")

	cmd := exec.CommandContext(ctx, runner, args...)
	cmd.Dir = cwd
	cmd.Stdout = QuietWriter()
	cmd.Stderr = QuietWriter()

	return cmd
}

func WatchAndBuild(ctx context.Context, pages []PageSpec, distDir string, buildDone chan<- struct{}) error {
	cssPath := filepath.Join(DefaultAppDir, "app.css")
	cwd, _ := os.Getwd()

	if err := BuildDevBundles(pages, distDir); err != nil {
		return fmt.Errorf("🔴 initial build: %w", err)
	}

	fmt.Fprintf(os.Stdout, "✅ Initial build complete\n")

	if buildDone != nil {
		close(buildDone)
	}

	serverTmpDir, err := os.MkdirTemp("", "alloy-server-")
	if err != nil {
		return fmt.Errorf("🔴 create server temp dir: %w", err)
	}
	defer os.RemoveAll(serverTmpDir)

	var g errgroup.Group

	serverCtxs := make([]serverCtxInfo, 0, len(pages))

	for _, page := range pages {
		absComponent, err := resolveAbsPath(page.Component, "component path")
		if err != nil {
			return err
		}

		entryCode := generateServerEntryCode(absComponent)

		entryPath := filepath.Join(serverTmpDir, page.Name+"-entry.tsx")
		if err := os.WriteFile(entryPath, []byte(entryCode), 0644); err != nil {
			return fmt.Errorf("🔴 write entry: %w", err)
		}

		outPath := filepath.Join(distDir, fmt.Sprintf("%s-server.js", page.Name))

		opts := commonBuildOptions()
		opts.EntryPoints = []string{entryPath}
		opts.Write = true
		opts.Outfile = outPath
		opts.Format = api.FormatIIFE
		opts.GlobalName = "__Component"
		opts.Platform = api.PlatformBrowser
		disableMinify(&opts)

		buildCtx, err := api.Context(opts)
		if err := checkContextError(err, fmt.Sprintf("create server context %s", page.Name)); err != nil {
			return err
		}

		serverCtxs = append(serverCtxs, serverCtxInfo{ctx: buildCtx, tmp: entryPath})

		watchCtx := buildCtx
		pageName := page.Name
		g.Go(func() error {
			err := watchCtx.Watch(api.WatchOptions{})
			if err != nil {
				return fmt.Errorf("🔴 watch server %s: %w", pageName, err)
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

	tmpClientDir, err := os.MkdirTemp("", "alloy-client-")
	if err != nil {
		return fmt.Errorf("🔴 create client temp: %w", err)
	}
	defer os.RemoveAll(tmpClientDir)

	clientEntries := make([]api.EntryPoint, 0, len(pages))
	for _, page := range pages {
		absComponent, err := resolveAbsPath(page.Component, "component path")
		if err != nil {
			return err
		}

		wrapperCode := generateClientEntryCode(absComponent, page.RootID)

		entryPath := filepath.Join(tmpClientDir, page.Name+".tsx")
		if err := os.WriteFile(entryPath, []byte(wrapperCode), 0644); err != nil {
			return fmt.Errorf("🔴 write client entry: %w", err)
		}

		clientEntries = append(clientEntries, api.EntryPoint{
			InputPath:  entryPath,
			OutputPath: page.Name,
		})
	}

	opts := commonBuildOptions()
	opts.EntryPointsAdvanced = clientEntries
	opts.Outdir = distDir
	opts.Splitting = true
	opts.Format = api.FormatESModule
	opts.Write = true
	opts.EntryNames = "[name]-client"
	opts.ChunkNames = "chunk-[hash]"
	disableMinify(&opts)

	clientCtx, err := api.Context(opts)
	if err := checkContextError(err, "create client context"); err != nil {
		return err
	}
	defer clientCtx.Dispose()

	g.Go(func() error {
		err := clientCtx.Watch(api.WatchOptions{})
		if err != nil {
			return fmt.Errorf("🔴 watch client: %w", err)
		}

		<-ctx.Done()
		return nil
	})

	g.Go(func() error {
		cmd := WatchTailwind(ctx, cssPath, filepath.Join(distDir, "shared.css"), cwd)
		if cmd == nil {
			return fmt.Errorf("🔴 tailwind runner not found")
		}

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("🔴 start tailwind: %w", err)
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
		return fmt.Errorf("🔴 write manifest: %w", err)
	}

	return g.Wait()
}

func DiscoverPages(dir string) ([]PageSpec, error) {
	pattern := filepath.Join(dir, "*.tsx")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("🔴 find pages: %w", err)
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

func executeSSR(ctx context.Context, jsCode string, props map[string]any) (string, error) {
	if timeout := currentRenderTimeout(); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	vm, err := newRuntimeWithContext()
	if err != nil {
		return "", fmt.Errorf("🔴 create runtime: %w", err)
	}
	defer closeRuntime(vm)

	vm.rt.SetInterruptHandler(makeInterruptHandler(ctx))
	defer vm.rt.ClearInterruptHandler()

	return runSSR(vm.ctx, jsCode, props)
}

func makeInterruptHandler(ctx context.Context) quickjs.InterruptHandler {
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
		return "", fmt.Errorf("🔴 eval component bundle: %s", ctx.Exception())
	}
	defer result.Free()

	propsJSON, err := json.Marshal(props)
	if err != nil {
		return "", fmt.Errorf("🔴 marshal props: %w", err)
	}

	renderCode := fmt.Sprintf(renderTemplate, string(propsJSON))

	renderResult := ctx.Eval(renderCode)
	defer renderResult.Free()

	if !renderResult.IsString() {
		return "", fmt.Errorf("🔴 render returned non-string: %s", renderResult.String())
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
		return nil, fmt.Errorf("🔴 parse esbuild metafile: %w", err)
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

func ServePrebuiltPageWithContext(w http.ResponseWriter, r *http.Request, componentPath string, props map[string]any, rootID string, files PrebuiltFiles) {
	result, err := RenderPrebuiltWithContext(r.Context(), componentPath, props, rootID, files)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, result.ToHTML(rootID))
}

func QuietWriter() io.Writer {
	if os.Getenv("DEBUG") != "" {
		return os.Stdout
	}
	return io.Discard
}

func FormatPath(path string) string {
	cwd, _ := os.Getwd()
	rel := strings.TrimPrefix(path, cwd+"/")
	return rel
}
