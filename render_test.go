package alloy

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTSXFileWithHydration(t *testing.T) {
	resetBundleCache()
	t.Cleanup(resetBundleCache)

	sampleDir := filepath.Join("cmd", "sample")
	componentPath := filepath.Join(sampleDir, "pages", "home.tsx")
	rootID := defaultRootID(componentPath)

	page := Page{
		Component: "pages/home.tsx",
		Name:      "home",
		DistDir:   "dist/alloy",
	}
	files, err := resolvePrebuiltFiles(os.DirFS(sampleDir), page)
	if err != nil {
		t.Fatalf("resolve prebuilt files: %v", err)
	}
	if err := RegisterPrebuiltBundleFromFS(componentPath, rootID, os.DirFS(sampleDir), files); err != nil {
		t.Fatalf("register prebuilt: %v", err)
	}

	props := map[string]any{"title": "Fixture", "items": []string{"One", "Two"}}
	result, err := RenderTSXFileWithHydration(componentPath, props, rootID)
	if err != nil {
		t.Fatalf("RenderTSXFileWithHydration failed: %v", err)
	}

	if !strings.Contains(result.HTML, "Fixture") {
		t.Errorf("SSR output missing props: %s", result.HTML)
	}
	if result.ClientJS == "" {
		t.Errorf("client bundle missing")
	}
	if result.CSS == "" {
		t.Errorf("expected css output")
	}

	full := result.ToHTML(rootID)
	if !strings.Contains(full, `<div id="home-root">`) {
		t.Errorf("wrapped html missing root container: %s", full)
	}
	if !strings.Contains(full, `"title":"Fixture"`) {
		t.Errorf("props json missing from document: %s", full)
	}
}

func TestRegisterPagesServesPrebuilt(t *testing.T) {
	resetBundleCache()
	t.Cleanup(resetBundleCache)
	t.Setenv("ALLOY_DEV", "")

	sampleDir := filepath.Join("cmd", "sample")
	filesystem := os.DirFS(sampleDir)

	pages := []Page{
		{
			Route:     "/",
			Component: "pages/home.tsx",
			Name:      "home",
			DistDir:   "dist/alloy",
			Props: func(r *http.Request) map[string]any {
				return map[string]any{"title": "Alloy sample", "items": []string{"First", "Second"}}
			},
		},
	}

	handler, err := PagesHandler(filesystem, pages)
	if err != nil {
		t.Fatalf("pages handler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Alloy sample") {
		t.Fatalf("body missing title: %s", body)
	}
	if !strings.Contains(body, "First") {
		t.Fatalf("body missing items: %s", body)
	}
}

func TestRegisterPagesSupportsRouteParams(t *testing.T) {
	resetBundleCache()
	t.Cleanup(resetBundleCache)
	t.Setenv("ALLOY_DEV", "")

	dir := t.TempDir()
	serverBundle := `var __Component = { default: function(props) { return "<div>slug:"+props.slug+"</div>"; } };`
	if err := os.WriteFile(filepath.Join(dir, "server.js"), []byte(serverBundle), 0644); err != nil {
		t.Fatalf("write server bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.js"), []byte("console.log('client')"), 0644); err != nil {
		t.Fatalf("write client bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0644); err != nil {
		t.Fatalf("write css bundle: %v", err)
	}

	filesystem := os.DirFS(dir)
	pages := []Page{
		{
			Route:     "/blog/:slug",
			Component: "pages/blog.tsx",
			Files: PrebuiltFiles{
				Server: "server.js",
				Client: "client.js",
				CSS:    "style.css",
			},
			Props: func(r *http.Request) map[string]any {
				params := RouteParams(r)
				slug := params["slug"]
				return map[string]any{"slug": slug}
			},
		},
	}

	mux := http.NewServeMux()
	if err := RegisterPages(mux, filesystem, pages); err != nil {
		t.Fatalf("register pages: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/blog/hello-world", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "hello-world") {
		t.Fatalf("response missing slug: %s", rr.Body.String())
	}
}

func TestRenderTSXFileWithHydrationRequiresCSS(t *testing.T) {
	dir, err := os.MkdirTemp(".", "alloy-test-no-css-")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	componentPath := filepath.Join(dir, "component.tsx")
	component := `
export default function Page() {
	return <div>no css file</div>;
}
`
	if err := os.WriteFile(componentPath, []byte(component), 0644); err != nil {
		t.Fatalf("write component: %v", err)
	}

	_, err = RenderTSXFileWithHydration(componentPath, nil, "root")
	if err == nil {
		t.Fatalf("expected error when css file is missing")
	}
	if !strings.Contains(err.Error(), "missing css file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRenderTSXFileWithHydrationRequiresRegisteredInProd(t *testing.T) {
	dir := t.TempDir()
	componentPath := filepath.Join(dir, "component.tsx")
	cssPath := filepath.Join(dir, "app.css")

	if err := os.WriteFile(componentPath, []byte("export default function Page(){ return null; }"), 0644); err != nil {
		t.Fatalf("write component: %v", err)
	}
	if err := os.WriteFile(cssPath, []byte("body{}"), 0644); err != nil {
		t.Fatalf("write css: %v", err)
	}

	_, err := RenderTSXFileWithHydration(componentPath, nil, "root")
	if err == nil {
		t.Fatalf("expected error when bundle not registered in prod")
	}
	if !strings.Contains(err.Error(), "bundle not registered") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWithPublicAssetsServesFiles(t *testing.T) {
	dir := t.TempDir()
	publicDir := filepath.Join(dir, "public")
	if err := os.MkdirAll(publicDir, 0755); err != nil {
		t.Fatalf("create public dir: %v", err)
	}

	assetContent := []byte("icon")
	assetPath := filepath.Join(publicDir, "favicon.ico")
	if err := os.WriteFile(assetPath, assetContent, 0644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	nextStatus := http.StatusNoContent
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(nextStatus)
	})

	handler := WithPublicAssets(next, os.DirFS(dir))

	request := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatalf("read asset body: %v", err)
	}

	if response.Code != http.StatusOK {
		t.Fatalf("asset status: want 200, got %d", response.Code)
	}
	if string(body) != string(assetContent) {
		t.Fatalf("asset body mismatch: %q", string(body))
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("cache control: want %q, got %q", "public, max-age=300", got)
	}
	if etag := response.Header().Get("ETag"); etag == "" {
		t.Fatalf("expected etag header")
	}
	if nextCalled {
		t.Fatalf("next handler should not run for asset request")
	}

	nextCalled = false
	fallbackRequest := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	fallbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(fallbackResponse, fallbackRequest)

	if fallbackResponse.Code != nextStatus {
		t.Fatalf("fallback status: want %d, got %d", nextStatus, fallbackResponse.Code)
	}
	if !nextCalled {
		t.Fatalf("expected next handler for missing asset")
	}
}

func TestWithPublicAssetsHashedAssetsCacheLonger(t *testing.T) {
	dir := t.TempDir()
	publicDir := filepath.Join(dir, "public")
	if err := os.MkdirAll(publicDir, 0755); err != nil {
		t.Fatalf("create public dir: %v", err)
	}

	assetContent := []byte("icon-hash")
	assetPath := filepath.Join(publicDir, "logo-abcdef12.png")
	if err := os.WriteFile(assetPath, assetContent, 0644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	handler := WithPublicAssets(http.NotFound, os.DirFS(dir))

	request := httptest.NewRequest(http.MethodGet, "/logo-abcdef12.png", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("asset status: want 200, got %d", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("cache control: want long cache, got %q", got)
	}
	if etag := response.Header().Get("ETag"); etag == "" {
		t.Fatalf("expected etag header")
	}
}

func TestWithPublicAssetsServesDistFiles(t *testing.T) {
	sampleDir := filepath.Join("cmd", "sample")
	filesystem := os.DirFS(sampleDir)

	data, err := fs.ReadFile(filesystem, path.Join("dist", "alloy", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var manifest map[string]manifestEntry
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	entry := manifest["home"]
	if entry.Client == "" {
		t.Fatalf("manifest missing client entry")
	}

	asset := path.Join("dist/alloy", entry.Client)
	request := httptest.NewRequest(http.MethodGet, "/"+asset, nil)

	handler := WithPublicAssets(http.NotFound, filesystem)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("asset status: want 200, got %d", response.Code)
	}
	if response.Header().Get("Cache-Control") == "" {
		t.Fatalf("missing cache header")
	}
	if response.Body.Len() == 0 {
		t.Fatalf("expected asset content")
	}
}

func TestWithPublicAssetsHandlesMissingNext(t *testing.T) {
	handler := WithPublicAssets(nil, os.DirFS("."))

	request := httptest.NewRequest(http.MethodGet, "/any", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when next is nil, got %d", response.Code)
	}
}

func TestRenderResultToHTMLWithAssets(t *testing.T) {
	result := RenderResult{
		HTML:        "<div>ok</div>",
		ClientPaths: []string{"/static/a.js", "/static/b.js"},
		CSSPath:     "/styles/app.css",
		SharedCSS:   "/styles/shared.css",
		Props: map[string]any{
			"title": "Docs",
			"note":  "n",
			"meta": map[string]any{
				"title":       "Docs page",
				"description": "Docs description",
				"url":         "https://example.com/docs",
				"image":       "https://cdn.example.com/hero.png",
				"ogType":      "product",
			},
		},
	}

	html := result.ToHTML("app-root")

	if !strings.Contains(html, `<link rel="stylesheet" href="/styles/shared.css" />`) {
		t.Fatalf("missing shared css link: %s", html)
	}
	if !strings.Contains(html, `<link rel="stylesheet" href="/styles/app.css" />`) {
		t.Fatalf("missing css link: %s", html)
	}
	if !strings.Contains(html, `<script type="module" src="/static/a.js"></script>`) {
		t.Fatalf("missing first client script: %s", html)
	}
	if !strings.Contains(html, `<script type="module" src="/static/b.js"></script>`) {
		t.Fatalf("missing second client script: %s", html)
	}
	if !strings.Contains(html, `"note":"n"`) {
		t.Fatalf("props json missing: %s", html)
	}
	if !strings.Contains(html, `<meta name="description" content="Docs description">`) {
		t.Fatalf("missing description meta: %s", html)
	}
	if !strings.Contains(html, `<meta property="og:image" content="https://cdn.example.com/hero.png">`) {
		t.Fatalf("missing og image: %s", html)
	}
	if !strings.Contains(html, `<link rel="canonical" href="https://example.com/docs">`) {
		t.Fatalf("missing canonical: %s", html)
	}
	if !strings.Contains(html, `<meta property="og:type" content="product">`) {
		t.Fatalf("missing og type: %s", html)
	}
}

func TestRenderResultToHTMLWithoutClient(t *testing.T) {
	result := RenderResult{
		HTML:  "<p>server only</p>",
		Props: map[string]any{"title": "Only"},
	}

	html := result.ToHTML("root")
	if html != result.HTML {
		t.Fatalf("expected raw html without hydration, got %s", html)
	}
}

func TestRegisterPrebuiltBundleStoresClientsPerRoot(t *testing.T) {
	resetBundleCache()
	t.Cleanup(resetBundleCache)

	dir := t.TempDir()
	component := filepath.Join(dir, "component.tsx")
	if err := os.WriteFile(component, []byte("export default {}"), 0644); err != nil {
		t.Fatalf("write component: %v", err)
	}

	serverA := `var __Component = { default: function() { return "<div>a</div>"; } };`
	serverB := serverA + "//b"
	clientA := "console.log('a');"
	clientB := "console.log('b');"

	if err := RegisterPrebuiltBundle(component, "root-a", serverA, clientA, "css-a"); err != nil {
		t.Fatalf("register first bundle: %v", err)
	}
	if err := RegisterPrebuiltBundle(component, "root-b", serverB, clientB, "css-b"); err != nil {
		t.Fatalf("register second bundle: %v", err)
	}

	absPath, _ := filepath.Abs(component)
	serverOut, clientOutA, cssOut := readBundlesFromCache(absPath, "root-a")
	if serverOut != serverB {
		t.Fatalf("server bundle should update to latest, got %s", serverOut)
	}
	if clientOutA != clientA {
		t.Fatalf("client bundle for root-a mismatch: %s", clientOutA)
	}
	if cssOut != "css-b" {
		t.Fatalf("css should update to latest, got %s", cssOut)
	}

	_, clientOutB, _ := readBundlesFromCache(absPath, "root-b")
	if clientOutB != clientB {
		t.Fatalf("client bundle for root-b mismatch: %s", clientOutB)
	}
}

func TestRenderPrebuiltUsesCachedBundles(t *testing.T) {
	resetBundleCache()
	t.Cleanup(resetBundleCache)

	dir := t.TempDir()
	component := filepath.Join(dir, "component.tsx")
	if err := os.WriteFile(component, []byte("export default {}"), 0644); err != nil {
		t.Fatalf("write component: %v", err)
	}

	serverJS := `var __Component = { default: function(props) { return "<span>"+props.msg+"</span>"; } };`
	if err := RegisterPrebuiltBundle(component, "root", serverJS, "client-code", "css-code"); err != nil {
		t.Fatalf("register bundle: %v", err)
	}

	files := PrebuiltFiles{
		Server:       filepath.Join("dist", "page-server.js"),
		Client:       filepath.Join("dist", "page-client.js"),
		CSS:          filepath.Join("dist", "page.css"),
		SharedCSS:    filepath.Join("dist", "shared.css"),
		ClientChunks: []string{filepath.Join("dist", "chunk-1.js")},
	}

	props := map[string]any{"msg": "hi"}
	result, err := RenderPrebuilt(component, props, "root", files)
	if err != nil {
		t.Fatalf("render prebuilt: %v", err)
	}

	if !strings.Contains(result.HTML, "hi") {
		t.Fatalf("ssr output missing props: %s", result.HTML)
	}
	wantClient := "/" + filepath.ToSlash(files.Client)
	if len(result.ClientPaths) != 1 || result.ClientPaths[0] != wantClient {
		t.Fatalf("client path: want %s, got %v", wantClient, result.ClientPaths)
	}
	wantCSS := "/" + filepath.ToSlash(files.CSS)
	if result.CSSPath != wantCSS {
		t.Fatalf("css path: want %s, got %s", wantCSS, result.CSSPath)
	}
	wantShared := "/" + filepath.ToSlash(files.SharedCSS)
	if result.SharedCSS != wantShared {
		t.Fatalf("shared css: want %s, got %s", wantShared, result.SharedCSS)
	}
	if len(result.Props) == 0 || result.Props["msg"] != "hi" {
		t.Fatalf("props not forwarded: %v", result.Props)
	}
}

func TestSaveFilesAndWriteManifest(t *testing.T) {
	dir := t.TempDir()

	files, err := SaveServerBundle("console.log('srv')", dir, "home")
	if err != nil {
		t.Fatalf("save server: %v", err)
	}
	if _, err := os.Stat(files.Server); err != nil {
		t.Fatalf("server file missing: %v", err)
	}

	sharedPath, err := SaveSharedCSS("body{}", dir, "shared")
	if err != nil {
		t.Fatalf("save shared css: %v", err)
	}
	if _, err := os.Stat(sharedPath); err != nil {
		t.Fatalf("shared css missing: %v", err)
	}

	files.Client = filepath.Join(dir, "home-client.js")
	files.CSS = filepath.Join(dir, "home.css")
	files.ClientChunks = []string{filepath.Join(dir, "chunk-a.js")}
	files.SharedCSS = sharedPath

	if err := os.WriteFile(files.Client, []byte("client"), 0644); err != nil {
		t.Fatalf("write client: %v", err)
	}
	if err := os.WriteFile(files.CSS, []byte("css"), 0644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	if err := os.WriteFile(files.ClientChunks[0], []byte("chunk"), 0644); err != nil {
		t.Fatalf("write chunk: %v", err)
	}

	if err := WriteManifest(dir, "home", *files); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var manifest map[string]manifestEntry
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	entry, ok := manifest["home"]
	if !ok {
		t.Fatalf("manifest missing home entry")
	}
	if entry.Server == "" || entry.Client == "" || entry.CSS == "" {
		t.Fatalf("manifest entry incomplete: %+v", entry)
	}
	if len(entry.Chunks) != 1 || entry.Chunks[0] != filepath.Base(files.ClientChunks[0]) {
		t.Fatalf("manifest chunks wrong: %+v", entry.Chunks)
	}
	if entry.Shared == "" {
		t.Fatalf("manifest shared css missing")
	}
}

func TestHelpersNormalizePaths(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{path: "", want: ""},
		{path: "/", want: ""},
		{path: "/favicon.ico", want: "favicon.ico"},
		{path: "/../etc/passwd", want: ""},
	}

	for _, tt := range cases {
		got := normalizeAssetPath(tt.path)
		if got != tt.want {
			t.Fatalf("normalize %q: want %q, got %q", tt.path, tt.want, got)
		}
	}

	slashCases := []struct {
		path string
		want string
	}{
		{path: "style.css", want: "/style.css"},
		{path: "/already", want: "/already"},
		{path: "", want: ""},
	}

	for _, tt := range slashCases {
		got := ensureLeadingSlash(tt.path)
		if got != tt.want {
			t.Fatalf("ensure slash %q: want %q, got %q", tt.path, tt.want, got)
		}
	}
}

func TestIsHashedAsset(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "logo-abcdef12.png", want: true},
		{name: "bundle-12345678.js", want: true},
		{name: "plain.js", want: false},
		{name: "logo-xyz.png", want: false},
	}

	for _, tt := range cases {
		got := isHashedAsset(tt.name)
		if got != tt.want {
			t.Fatalf("hashed %q: want %t, got %t", tt.name, tt.want, got)
		}
	}
}

func TestDefaultRootAndJoinPaths(t *testing.T) {
	if got := defaultRootID("pages/home.tsx"); got != "home-root" {
		t.Fatalf("default root: want home-root, got %s", got)
	}
	if got := defaultRootID(""); got != "root" {
		t.Fatalf("default root fallback: want root, got %s", got)
	}

	paths := joinPaths("dist", []string{"a.js", "b.js"})
	if len(paths) != 2 {
		t.Fatalf("join paths length: want 2, got %d", len(paths))
	}
	if paths[0] != path.Join("dist", "a.js") || paths[1] != path.Join("dist", "b.js") {
		t.Fatalf("join paths mismatch: %v", paths)
	}
}

func TestResolvePrebuiltFilesReadsManifest(t *testing.T) {
	dir := t.TempDir()
	distDir := filepath.Join(dir, "dist", "alloy")
	if err := os.MkdirAll(distDir, 0755); err != nil {
		t.Fatalf("create dist dir: %v", err)
	}

	manifestPath := filepath.Join(distDir, "manifest.json")
	entry := map[string]manifestEntry{
		"home": {
			Server: "home-aaaa1111-server.js",
			Client: "home-bbbb2222-client.js",
			Chunks: []string{"chunk-123.js"},
			CSS:    "home-cccc3333.css",
		},
	}
	data, _ := json.Marshal(entry)
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	page := Page{
		Route:     "/",
		Component: "pages/home.tsx",
		Name:      "home",
		DistDir:   "dist/alloy",
	}

	files, err := resolvePrebuiltFiles(os.DirFS(dir), page)
	if err != nil {
		t.Fatalf("resolve files: %v", err)
	}

	wantServer := filepath.Join("dist/alloy", "home-aaaa1111-server.js")
	if files.Server != wantServer {
		t.Fatalf("server path: want %s, got %s", wantServer, files.Server)
	}
	wantClient := filepath.Join("dist/alloy", "home-bbbb2222-client.js")
	if files.Client != wantClient {
		t.Fatalf("client path: want %s, got %s", wantClient, files.Client)
	}
	if len(files.ClientChunks) != 1 || files.ClientChunks[0] != filepath.Join("dist/alloy", "chunk-123.js") {
		t.Fatalf("client chunks: %v", files.ClientChunks)
	}
	wantCSS := filepath.Join("dist/alloy", "home-cccc3333.css")
	if files.CSS != wantCSS {
		t.Fatalf("css path: want %s, got %s", wantCSS, files.CSS)
	}
}

func BenchmarkRenderStages(b *testing.B) {
	dir, err := os.MkdirTemp(".", "alloy-bench-")
	if err != nil {
		b.Fatalf("create temp dir: %v", err)
	}
	b.Setenv("ALLOY_DEV", "1")
	b.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	componentPath := filepath.Join(dir, "component.tsx")
	cssPath := filepath.Join(dir, "app.css")

	component := `
import { useState } from 'react';

export default function Page({ title }: { title: string }) {
	const [count, setCount] = useState(0);
	return (
		<div className="p-4">
			<h1>{title} {count}</h1>
			<button onClick={() => setCount(count + 1)}>inc</button>
		</div>
	);
}
`
	if err := os.WriteFile(componentPath, []byte(component), 0644); err != nil {
		b.Fatalf("write component: %v", err)
	}
	if err := os.WriteFile(cssPath, []byte(`@import "tailwindcss";`), 0644); err != nil {
		b.Fatalf("write css: %v", err)
	}

	props := map[string]any{"title": "bench"}

	b.Run("bundle_server", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, _, err := bundleTSXFile(componentPath); err != nil {
				b.Fatalf("bundle server: %v", err)
			}
		}
	})

	b.Run("bundle_client", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, _, err := bundleClientJS(componentPath, "root"); err != nil {
				b.Fatalf("bundle client: %v", err)
			}
		}
	})

	b.Run("build_css", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, _, err := buildTailwindCSS(componentPath); err != nil {
				b.Fatalf("build css: %v", err)
			}
		}
	})

	serverJS, _, err := bundleTSXFile(componentPath)
	if err != nil {
		b.Fatalf("prep server bundle: %v", err)
	}
	b.Run("execute_ssr", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := executeSSR(context.Background(), componentPath, serverJS, props); err != nil {
				b.Fatalf("execute ssr: %v", err)
			}
		}
	})

	b.Run("full_cold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resetBundleCache()
			if _, err := RenderTSXFileWithHydration(componentPath, props, "root"); err != nil {
				b.Fatalf("full cold: %v", err)
			}
		}
	})

	if _, err := RenderTSXFileWithHydration(componentPath, props, "root"); err != nil {
		b.Fatalf("prime cache: %v", err)
	}
	b.Run("full_warm", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := RenderTSXFileWithHydration(componentPath, props, "root"); err != nil {
				b.Fatalf("full warm: %v", err)
			}
		}
	})

	resetBundleCache()
	serverJS = `var __Component = { default: function(props) { return "<div>"+props.msg+"</div>"; } };`
	if err := RegisterPrebuiltBundle(componentPath, "root", serverJS, "client", "css"); err != nil {
		b.Fatalf("register bundle: %v", err)
	}

	files := PrebuiltFiles{
		Server: filepath.Join("dist", "page-server.js"),
		Client: filepath.Join("dist", "page-client.js"),
		CSS:    filepath.Join("dist", "page.css"),
	}

	b.Run("render_prebuilt", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := RenderPrebuilt(componentPath, props, "root", files); err != nil {
				b.Fatalf("render prebuilt: %v", err)
			}
		}
	})
}

func BenchmarkPagesHandlerPrebuilt(b *testing.B) {
	resetBundleCache()
	sampleDir := filepath.Join("cmd", "sample")
	filesystem := os.DirFS(sampleDir)

	pages := []Page{
		{
			Route:     "/",
			Component: "pages/home.tsx",
			Name:      "home",
			DistDir:   "dist/alloy",
			Props: func(r *http.Request) map[string]any {
				return map[string]any{"title": "Alloy sample", "items": []string{"First", "Second"}}
			},
		},
	}

	handler, err := PagesHandler(filesystem, pages)
	if err != nil {
		b.Fatalf("pages handler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		handler(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatalf("status: want 200, got %d", rr.Code)
		}
	}
}

func resetBundleCache() {
	bundleCache.Lock()
	bundleCache.entries = make(map[string]*bundleCacheEntry)
	bundleCache.Unlock()
}
