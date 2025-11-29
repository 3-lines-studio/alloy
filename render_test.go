package alloy

import (
	"context"
	"encoding/json"
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
	componentPath := filepath.Join(sampleDir, "app", "pages", "home.tsx")
	rootID := defaultRootID(componentPath)

	files, err := resolvePrebuiltFiles(os.DirFS(sampleDir), "app/pages/home.tsx", "", "home")
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


func TestRenderTSXFileWithHydrationRequiresCSS(t *testing.T) {
	resetBundleCache()
	t.Cleanup(resetBundleCache)

	dir := t.TempDir()
	componentPath := filepath.Join(dir, "component.tsx")

	serverBundle := `var __Component = { default: function() { return "<div>test</div>"; } };`
	if err := os.WriteFile(filepath.Join(dir, "server.js"), []byte(serverBundle), 0644); err != nil {
		t.Fatalf("write server bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.js"), []byte("console.log('test')"), 0644); err != nil {
		t.Fatalf("write client bundle: %v", err)
	}

	files := PrebuiltFiles{
		Server: "server.js",
		Client: "client.js",
		CSS:    "",
	}
	err := RegisterPrebuiltBundleFromFS(componentPath, "root", os.DirFS(dir), files)
	if err == nil {
		t.Fatalf("expected error when css file is missing")
	}
	if !strings.Contains(err.Error(), "prebuilt file paths required") {
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
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unexpected error: %v", err)
	}
}



func TestRenderResultToHTMLWithAssets(t *testing.T) {
	result := RenderResult{
		HTML:        "<div>ok</div>",
		ClientPaths: []string{"/static/a.js", "/static/b.js"},
		CSSPath:     "/styles/app.css",
		Props: map[string]any{
			"title": "Docs",
			"note":  "n",
			"meta": []map[string]any{
				{"title": "Docs page"},
				{"name": "description", "content": "Docs description"},
				{"tagName": "link", "rel": "canonical", "href": "https://example.com/docs"},
				{"property": "og:url", "content": "https://example.com/docs"},
				{"property": "og:image", "content": "https://cdn.example.com/hero.png"},
				{"property": "og:type", "content": "product"},
			},
		},
	}

	html := result.ToHTML("app-root")

	if !strings.Contains(html, `<link rel="stylesheet" href="/styles/app.css?v=`) {
		t.Fatalf("missing css link: %s", html)
	}
	if !strings.Contains(html, `<script type="module" src="/static/a.js?v=`) {
		t.Fatalf("missing first client script: %s", html)
	}
	if !strings.Contains(html, `<script type="module" src="/static/b.js?v=`) {
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
	if !strings.Contains(html, `<link href="https://example.com/docs" rel="canonical">`) {
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

func TestRenderResultHashedAssetsNoTimestamp(t *testing.T) {
	result := RenderResult{
		HTML:        "<div>prod</div>",
		ClientPaths: []string{"/static/app-abc12345.js", "/static/chunk-def67890.js"},
		CSSPath:     "/static/shared-894b8266.css",
		Props:       map[string]any{},
	}

	html := result.ToHTML("app-root")

	if strings.Contains(html, "?v=") {
		t.Fatalf("hashed assets should not have timestamp query params: %s", html)
	}
	if !strings.Contains(html, `src="/static/app-abc12345.js"`) {
		t.Fatalf("missing hashed script without timestamp: %s", html)
	}
	if !strings.Contains(html, `href="/static/shared-894b8266.css"`) {
		t.Fatalf("missing hashed css without timestamp: %s", html)
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

	files.Client = filepath.Join(dir, "home-client.js")
	files.CSS = filepath.Join(dir, "home.css")
	files.ClientChunks = []string{filepath.Join(dir, "chunk-a.js")}

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
		{name: "client-44QNSIEN.js", want: true},
		{name: "chunk-UE5HRNN5.js", want: true},
		{name: "plain.js", want: false},
		{name: "logo-xyz.png", want: false},
		{name: "home-client.js", want: false},
	}

	for _, tt := range cases {
		got := isHashedAsset(tt.name)
		if got != tt.want {
			t.Fatalf("hashed %q: want %t, got %t", tt.name, tt.want, got)
		}
	}
}

func TestDefaultRootAndJoinPaths(t *testing.T) {
	if got := defaultRootID("app/pages/home.tsx"); got != "home-root" {
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
	distDir := filepath.Join(dir, "dist")
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

	files, err := resolvePrebuiltFiles(os.DirFS(dir), "pages/home.tsx", "dist", "home")
	if err != nil {
		t.Fatalf("resolve files: %v", err)
	}

	wantServer := filepath.Join("dist", "home-aaaa1111-server.js")
	if files.Server != wantServer {
		t.Fatalf("server path: want %s, got %s", wantServer, files.Server)
	}
	wantClient := filepath.Join("dist", "home-bbbb2222-client.js")
	if files.Client != wantClient {
		t.Fatalf("client path: want %s, got %s", wantClient, files.Client)
	}
	if len(files.ClientChunks) != 1 || files.ClientChunks[0] != filepath.Join("dist", "chunk-123.js") {
		t.Fatalf("client chunks: %v", files.ClientChunks)
	}
	wantCSS := filepath.Join("dist", "home-cccc3333.css")
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
			if _, _, err := BuildServerBundle(componentPath); err != nil {
				b.Fatalf("bundle server: %v", err)
			}
		}
	})

	b.Run("build_css", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := RunTailwind(cssPath, filepath.Dir(cssPath)); err != nil {
				b.Fatalf("build css: %v", err)
			}
		}
	})

	serverJS, _, err := BuildServerBundle(componentPath)
	if err != nil {
		b.Fatalf("prep server bundle: %v", err)
	}
	b.Run("execute_ssr", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := executeSSR(context.Background(), serverJS, props); err != nil {
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


func TestMetaTagsArrayFormat(t *testing.T) {
	tests := []struct {
		name        string
		props       map[string]any
		contains    []string
		notContains []string
	}{
		{
			name: "basic meta array",
			props: map[string]any{
				"meta": []map[string]any{
					{"title": "Test Page"},
					{"name": "description", "content": "Test description"},
				},
			},
			contains: []string{
				"<title>Test Page</title>",
				`<meta name="description" content="Test description">`,
			},
		},
		{
			name: "open graph tags",
			props: map[string]any{
				"meta": []map[string]any{
					{"property": "og:title", "content": "OG Title"},
					{"property": "og:type", "content": "website"},
				},
			},
			contains: []string{
				`<meta property="og:title" content="OG Title">`,
				`<meta property="og:type" content="website">`,
			},
		},
		{
			name: "link tags",
			props: map[string]any{
				"meta": []map[string]any{
					{"tagName": "link", "rel": "canonical", "href": "https://example.com/page"},
				},
			},
			contains: []string{
				`<link href="https://example.com/page" rel="canonical">`,
			},
		},
		{
			name: "html escaping",
			props: map[string]any{
				"meta": []map[string]any{
					{"title": `Test "quoted" <tag>`},
					{"name": "description", "content": `Test & "special" <chars>`},
				},
			},
			contains: []string{
				"<title>Test &#34;quoted&#34; &lt;tag&gt;</title>",
				`content="Test &amp; &#34;special&#34; &lt;chars&gt;"`,
			},
			notContains: []string{
				`<tag>`,
			},
		},
		{
			name: "empty meta array",
			props: map[string]any{
				"meta": []map[string]any{},
			},
			contains: []string{
				"<title>Alloy</title>",
				`<meta name="viewport"`,
			},
		},
		{
			name: "invalid tags skipped",
			props: map[string]any{
				"meta": []map[string]any{
					{"name": "valid", "content": "Valid tag"},
					{"name": "invalid"},
					{"content": "orphan"},
				},
			},
			contains: []string{
				`<meta name="valid" content="Valid tag">`,
			},
			notContains: []string{
				`name="invalid"`,
				`content="orphan"`,
			},
		},
		{
			name: "robots and keywords",
			props: map[string]any{
				"meta": []map[string]any{
					{"name": "robots", "content": "index, follow"},
					{"name": "keywords", "content": "go, react, ssr"},
				},
			},
			contains: []string{
				`<meta name="robots" content="index, follow">`,
				`<meta name="keywords" content="go, react, ssr">`,
			},
		},
		{
			name: "twitter card",
			props: map[string]any{
				"meta": []map[string]any{
					{"name": "twitter:card", "content": "summary_large_image"},
					{"name": "twitter:site", "content": "@example"},
				},
			},
			contains: []string{
				`<meta name="twitter:card" content="summary_large_image">`,
				`<meta name="twitter:site" content="@example">`,
			},
		},
		{
			name: "complete example with all tag types",
			props: map[string]any{
				"meta": []map[string]any{
					{"title": "Complete Page"},
					{"name": "description", "content": "Page description"},
					{"property": "og:title", "content": "OG Title"},
					{"property": "og:description", "content": "OG Desc"},
					{"property": "og:url", "content": "https://example.com"},
					{"property": "og:image", "content": "https://example.com/og.png"},
					{"property": "og:type", "content": "website"},
					{"property": "og:locale", "content": "es_AR"},
					{"name": "twitter:card", "content": "summary_large_image"},
					{"name": "robots", "content": "index, follow"},
					{"tagName": "link", "rel": "canonical", "href": "https://example.com"},
				},
			},
			contains: []string{
				"<title>Complete Page</title>",
				`<meta name="description" content="Page description">`,
				`<meta property="og:title" content="OG Title">`,
				`<meta property="og:locale" content="es_AR">`,
				`<meta name="twitter:card" content="summary_large_image">`,
				`<meta name="robots" content="index, follow">`,
				`<link href="https://example.com" rel="canonical">`,
			},
		},
		{
			name: "missing meta prop uses defaults",
			props: map[string]any{
				"title": "Fallback Title",
			},
			contains: []string{
				"<title>Fallback Title</title>",
				`<meta name="viewport"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := metaTagsFromProps(tt.props)
			html := buildHead(tags)

			for _, expected := range tt.contains {
				if !strings.Contains(html, expected) {
					t.Errorf("expected HTML to contain %q, got:\n%s", expected, html)
				}
			}

			for _, unexpected := range tt.notContains {
				if strings.Contains(html, unexpected) {
					t.Errorf("expected HTML NOT to contain %q, got:\n%s", unexpected, html)
				}
			}
		})
	}
}

func TestBuildServerBundle(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	defer os.Chdir(cwd)

	sampleDir := filepath.Join(cwd, "cmd", "sample")
	if err := os.Chdir(sampleDir); err != nil {
		t.Fatalf("chdir to sample: %v", err)
	}

	componentPath := filepath.Join(sampleDir, "app", "pages", "home.tsx")

	js, deps, err := BuildServerBundle(componentPath)
	if err != nil {
		t.Fatalf("BuildServerBundle failed: %v", err)
	}

	if js == "" {
		t.Error("expected non-empty JS output")
	}
	if !strings.Contains(js, "renderToString") {
		t.Error("expected JS to contain renderToString")
	}
	if len(deps) == 0 {
		t.Error("expected at least one dependency")
	}

	hasReact := false
	for _, dep := range deps {
		if strings.Contains(dep, "react") {
			hasReact = true
			break
		}
	}
	if !hasReact {
		t.Errorf("expected deps to contain react, got: %v", deps)
	}
}

func TestBuildServerBundleNonExistent(t *testing.T) {
	_, _, err := BuildServerBundle("/nonexistent/file.tsx")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildClientBundlesSingle(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	defer os.Chdir(cwd)

	sampleDir := filepath.Join(cwd, "cmd", "sample")
	if err := os.Chdir(sampleDir); err != nil {
		t.Fatalf("chdir to sample: %v", err)
	}

	outDir := t.TempDir()
	componentPath := filepath.Join(sampleDir, "app", "pages", "home.tsx")

	entries := []ClientEntry{
		{Name: "home", Component: componentPath, RootID: "home-root"},
	}

	assets, err := BuildClientBundles(entries, outDir)
	if err != nil {
		t.Fatalf("BuildClientBundles failed: %v", err)
	}

	if len(assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(assets))
	}

	homeAsset, ok := assets["home"]
	if !ok {
		t.Fatal("expected 'home' key in assets map")
	}

	if homeAsset.Entry == "" {
		t.Error("expected non-empty Entry field")
	}

	files, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read out dir: %v", err)
	}
	if len(files) == 0 {
		t.Error("expected at least one file written to disk")
	}

	foundEntry := false
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "client-home-") && strings.HasSuffix(f.Name(), ".js") {
			foundEntry = true
			break
		}
	}
	if !foundEntry {
		t.Error("expected client-home-*.js file in output directory")
	}
}

func TestBuildClientBundlesMultiple(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	defer os.Chdir(cwd)

	sampleDir := filepath.Join(cwd, "cmd", "sample")
	if err := os.Chdir(sampleDir); err != nil {
		t.Fatalf("chdir to sample: %v", err)
	}

	outDir := t.TempDir()
	homePath := filepath.Join(sampleDir, "app", "pages", "home.tsx")
	aboutPath := filepath.Join(sampleDir, "app", "pages", "about.tsx")

	entries := []ClientEntry{
		{Name: "home", Component: homePath, RootID: "home-root"},
		{Name: "about", Component: aboutPath, RootID: "about-root"},
	}

	assets, err := BuildClientBundles(entries, outDir)
	if err != nil {
		t.Fatalf("BuildClientBundles failed: %v", err)
	}

	if len(assets) != 2 {
		t.Fatalf("expected 2 assets, got %d", len(assets))
	}

	if _, ok := assets["home"]; !ok {
		t.Error("expected 'home' in assets map")
	}
	if _, ok := assets["about"]; !ok {
		t.Error("expected 'about' in assets map")
	}

	if assets["home"].Entry == assets["about"].Entry {
		t.Error("expected distinct entry files for different components")
	}
}

func TestBuildClientBundlesEmptyEntries(t *testing.T) {
	outDir := t.TempDir()

	_, err := BuildClientBundles([]ClientEntry{}, outDir)
	if err == nil {
		t.Fatal("expected error for empty entries")
	}
	if !strings.Contains(err.Error(), "entries required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildClientBundlesMissingFields(t *testing.T) {
	outDir := t.TempDir()

	entries := []ClientEntry{
		{Name: "", Component: "test.tsx", RootID: "root"},
	}

	_, err := BuildClientBundles(entries, outDir)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name and component required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildClientBundlesInvalidOutDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}

	componentPath := filepath.Join(cwd, "cmd", "sample", "app", "pages", "home.tsx")
	entries := []ClientEntry{
		{Name: "home", Component: componentPath},
	}

	_, err = BuildClientBundles(entries, "")
	if err == nil {
		t.Fatal("expected error for empty outDir")
	}
	if !strings.Contains(err.Error(), "out dir required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunTailwind(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}

	cssPath := filepath.Join(cwd, "cmd", "sample", "app", "app.css")

	css, err := RunTailwind(cssPath, cwd)
	if err != nil {
		t.Fatalf("RunTailwind failed: %v", err)
	}

	if css == "" {
		t.Error("expected non-empty CSS output")
	}
}

func TestRunTailwindNonExistent(t *testing.T) {
	_, err := RunTailwind("/nonexistent/app.css", ".")
	if err == nil {
		t.Fatal("expected error for non-existent CSS file")
	}
}

func TestRunTailwindMinimal(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}

	sampleDir := filepath.Join(cwd, "cmd", "sample")
	cssPath := filepath.Join(sampleDir, "minimal.css")

	minimal := `body { margin: 0; }`
	if err := os.WriteFile(cssPath, []byte(minimal), 0644); err != nil {
		t.Fatalf("write minimal css: %v", err)
	}
	defer os.Remove(cssPath)

	css, err := RunTailwind(cssPath, sampleDir)
	if err != nil {
		t.Fatalf("RunTailwind failed for minimal file: %v", err)
	}

	if css == "" {
		t.Error("expected some CSS output")
	}
	if !strings.Contains(css, "margin") {
		t.Error("expected CSS output to contain margin declaration")
	}
}

func resetBundleCache() {
	bundleCache.Lock()
	bundleCache.entries = make(map[string]*bundleCacheEntry)
	bundleCache.Unlock()
}
