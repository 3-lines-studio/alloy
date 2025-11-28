package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/3-lines-studio/alloy"
	"github.com/evanw/esbuild/pkg/api"
	"golang.org/x/sync/errgroup"
)

type pageSpec struct {
	component string
	name      string
	rootID    string
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcommand := os.Args[1]
	args := os.Args[2:]

	switch subcommand {
	case "build":
		runBuild(args)
	case "dev":
		runDev(args)
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: alloy <command> [flags]

Commands:
  build    Build production bundles with content hashes
  dev      Watch and rebuild bundles on changes (no hashes)
  watch    Run dev + air in parallel for complete hot reload

Flags:
  -pages string
        Directory containing page components (.tsx)
        Auto-discovers: app/pages or pages
  -out string
        Output directory for bundles
        Default: {pages_parent}/dist/alloy

Examples:
  alloy build
  alloy build -pages app/pages -out app/dist
  alloy dev
  alloy dev -pages app/pages -out app/dist
  alloy watch
`)
}

func runBuild(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	var pagesDir string
	var distDir string

	fs.StringVar(&pagesDir, "pages", "", "directory containing page components (.tsx)")
	fs.StringVar(&distDir, "out", "", "output directory for prebuilt bundles")
	fs.Parse(args)

	pagesDir = defaultPagesDir(pagesDir)
	if pagesDir == "" {
		log.Fatal("pages dir required")
	}
	distDir = defaultDistDir(distDir)
	if distDir == "" {
		log.Fatal("out dir required")
	}

	cleanDist := filepath.Clean(distDir)
	if cleanDist == "." || cleanDist == string(filepath.Separator) {
		log.Fatalf("refusing to remove dist dir %q", distDir)
	}
	if err := os.RemoveAll(cleanDist); err != nil {
		log.Fatal(err)
	}

	pages, err := discoverPages(pagesDir)
	if err != nil {
		log.Fatal(err)
	}
	if len(pages) == 0 {
		log.Fatalf("no pages found in %s", pagesDir)
	}

	cssPath := filepath.Join(alloy.DefaultAppDir, "app.css")
	sharedCSS, err := alloy.RunTailwind(cssPath, filepath.Dir(pagesDir))
	if err != nil {
		log.Fatal(err)
	}
	sharedCSSPath, err := alloy.SaveCSS(sharedCSS, distDir, "shared")
	if err != nil {
		log.Fatal(err)
	}

	clientInputs := make([]alloy.ClientEntry, 0, len(pages))
	for _, page := range pages {
		clientInputs = append(clientInputs, alloy.ClientEntry{
			Name:      page.name,
			Component: page.component,
			RootID:    page.rootID,
		})
	}

	clientAssets, err := alloy.BuildClientBundles(clientInputs, distDir)
	if err != nil {
		log.Fatal(err)
	}

	for _, page := range pages {
		if err := buildPage(page, distDir, clientAssets[page.name], sharedCSSPath); err != nil {
			log.Fatal(err)
		}
	}

	log.Printf("build complete: %d pages -> %s", len(pages), distDir)
}

func runDev(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	var pagesDir string
	var distDir string

	fs.StringVar(&pagesDir, "pages", "", "directory containing page components (.tsx)")
	fs.StringVar(&distDir, "out", "", "output directory for bundles")
	fs.Parse(args)

	pagesDir = defaultPagesDir(pagesDir)
	distDir = defaultDistDir(distDir)

	if err := os.MkdirAll(distDir, 0755); err != nil {
		log.Fatalf("create dist dir: %v", err)
	}

	pages, err := discoverPages(pagesDir)
	if err != nil {
		log.Fatal(err)
	}
	if len(pages) == 0 {
		log.Fatalf("no pages found in %s", pagesDir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("shutting down...")
		cancel()
	}()

	initialBuildDone := make(chan struct{})

	var g errgroup.Group

	g.Go(func() error {
		log.Printf("starting asset watcher: %d pages in %s", len(pages), pagesDir)
		err := watchAndBuildWithSignal(ctx, pages, distDir, initialBuildDone)
		if err == context.Canceled {
			return nil
		}
		return err
	})

	g.Go(func() error {
		select {
		case <-initialBuildDone:
		case <-ctx.Done():
			return nil
		}

		airPath, err := exec.LookPath("air")
		if err != nil {
			return fmt.Errorf("air not found in PATH")
		}

		configPath := ".air.toml"
		if !fileExists(configPath) {
			tmpConfig, err := os.CreateTemp("", "air-*.toml")
			if err != nil {
				return fmt.Errorf("create temp air config: %w", err)
			}
			configPath = tmpConfig.Name()
			defer os.Remove(configPath)

			defaultConfig := `root = "."
tmp_dir = ".air"

[build]
cmd = "go build -o .air/server main.go"
bin = ".air/server"
include_ext = ["go", "json", "js", "css"]
exclude_dir = ["node_modules", ".git"]
kill_delay = 300

[proxy]
enabled = true
proxy_port = 3000
app_port = 8080
`
			if _, err := tmpConfig.WriteString(defaultConfig); err != nil {
				return fmt.Errorf("write air config: %w", err)
			}
			tmpConfig.Close()
		}

		cmd := exec.Command(airPath, "-c", configPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		log.Println("starting air for Go hot reload")
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start air: %w", err)
		}

		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()

		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			if cmd.Process != nil {
				pgid, err := syscall.Getpgid(cmd.Process.Pid)
				if err == nil {
					syscall.Kill(-pgid, syscall.SIGTERM)
				}
			}
			return nil
		}
	})

	if err := g.Wait(); err != nil {
		log.Fatal(err)
	}
}

func watchAndBuildWithSignal(ctx context.Context, pages []pageSpec, distDir string, buildDone chan<- struct{}) error {
	cssPath := filepath.Join(alloy.DefaultAppDir, "app.css")
	cwd, _ := os.Getwd()

	if err := buildAllDev(pages, distDir, cwd); err != nil {
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
		absComponent, err := filepath.Abs(page.component)
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

		entryPath := filepath.Join(serverTmpDir, page.name+"-entry.tsx")
		if err := os.WriteFile(entryPath, []byte(entryCode), 0644); err != nil {
			return fmt.Errorf("write entry: %w", err)
		}

		outPath := filepath.Join(distDir, fmt.Sprintf("%s-server.js", page.name))

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
			return fmt.Errorf("create server context %s: %v", page.name, err)
		}

		serverCtxs = append(serverCtxs, serverCtxInfo{ctx: buildCtx, tmp: entryPath})

		watchCtx := buildCtx
		pageName := page.name
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
		absComponent, err := filepath.Abs(page.component)
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
		`, absComponent, page.rootID, page.rootID)

		entryPath := filepath.Join(tmpClientDir, page.name+".tsx")
		if err := os.WriteFile(entryPath, []byte(wrapperCode), 0644); err != nil {
			return fmt.Errorf("write client entry: %w", err)
		}

		clientEntries = append(clientEntries, api.EntryPoint{
			InputPath:  entryPath,
			OutputPath: page.name,
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
		cmd := watchTailwind(ctx, cssPath, filepath.Join(distDir, "shared.css"), cwd)
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

	log.Println("watching for changes...")

	return g.Wait()
}

func buildAllDev(pages []pageSpec, distDir, cwd string) error {
	for _, page := range pages {
		serverJS, _, err := alloy.BuildServerBundle(page.component)
		if err != nil {
			return fmt.Errorf("build server %s: %w", page.name, err)
		}

		serverPath := filepath.Join(distDir, fmt.Sprintf("%s-server.js", page.name))
		if err := os.WriteFile(serverPath, []byte(serverJS), 0644); err != nil {
			return fmt.Errorf("write server %s: %w", page.name, err)
		}
	}

	tmpClientDir, err := os.MkdirTemp("", "alloy-initial-client-")
	if err != nil {
		return fmt.Errorf("create temp client dir: %w", err)
	}
	defer os.RemoveAll(tmpClientDir)

	clientEntries := make([]api.EntryPoint, 0, len(pages))
	for _, page := range pages {
		absComponent, err := filepath.Abs(page.component)
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
		`, absComponent, page.rootID, page.rootID)

		entryPath := filepath.Join(tmpClientDir, page.name+".tsx")
		if err := os.WriteFile(entryPath, []byte(wrapperCode), 0644); err != nil {
			return fmt.Errorf("write client entry: %w", err)
		}

		clientEntries = append(clientEntries, api.EntryPoint{
			InputPath:  entryPath,
			OutputPath: page.name,
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

	log.Println("initial build complete")
	return nil
}

func writeDevManifest(pages []pageSpec, distDir string) error {
	manifestPath := filepath.Join(distDir, "manifest.json")
	manifest := make(map[string]map[string]any)

	for _, page := range pages {
		manifest[page.name] = map[string]any{
			"server": fmt.Sprintf("%s-server.js", page.name),
			"client": fmt.Sprintf("%s-client.js", page.name),
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

func watchTailwind(ctx context.Context, inputPath, outputPath, cwd string) *exec.Cmd {
	runner, baseArgs := alloy.ResolveTailwindRunner(cwd)
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

func discoverPages(dir string) ([]pageSpec, error) {
	pattern := filepath.Join(dir, "*.tsx")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("find pages: %w", err)
	}

	var pages []pageSpec
	for _, match := range matches {
		base := filepath.Base(match)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		if name == "" {
			continue
		}
		pages = append(pages, pageSpec{
			component: match,
			name:      name,
			rootID:    defaultRootID(name),
		})
	}

	return pages, nil
}

func buildPage(page pageSpec, distDir string, client alloy.ClientAssets, sharedCSSPath string) error {
	if distDir == "" {
		return fmt.Errorf("out dir required")
	}

	serverJS, _, err := alloy.BuildServerBundle(page.component)
	if err != nil {
		return fmt.Errorf("build server %s: %w", page.component, err)
	}

	files, err := alloy.SaveServerBundle(serverJS, distDir, page.name)
	if err != nil {
		return fmt.Errorf("save server %s: %w", page.component, err)
	}

	files.Client = client.Entry
	files.ClientChunks = client.Chunks
	files.CSS = sharedCSSPath

	if err := alloy.WriteManifest(distDir, page.name, *files); err != nil {
		return fmt.Errorf("write manifest %s: %w", page.component, err)
	}

	log.Printf("built %s -> %s (client: %s)", page.component, distDir, client.Entry)
	return nil
}

func defaultRootID(name string) string {
	if name == "" {
		return "root"
	}
	return name + "-root"
}

func defaultPagesDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}

	return alloy.DefaultPagesDir
}

func defaultDistDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}

	return alloy.DefaultDistDir
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
