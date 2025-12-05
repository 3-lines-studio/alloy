package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/3-lines-studio/alloy"
	"golang.org/x/sync/errgroup"
)

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
  dev      Run with live reload

Flags:
  --pages string
        Directory containing page components (.tsx)
        Auto-discovers: app/pages or pages
  --out string
        Output directory for bundles
        Default: {pages_parent}/dist/alloy

Examples:
  alloy build
  alloy build --pages app/pages --out app/dist
  alloy dev
  alloy dev --pages app/pages --out app/dist
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
		fmt.Fprintf(os.Stderr, "🔴 pages dir required\n")
		os.Exit(1)
	}
	distDir = defaultDistDir(distDir)
	if distDir == "" {
		fmt.Fprintf(os.Stderr, "🔴 out dir required\n")
		os.Exit(1)
	}

	cleanDist := filepath.Clean(distDir)
	if cleanDist == "." || cleanDist == string(filepath.Separator) {
		fmt.Fprintf(os.Stderr, "🔴 refusing to remove dist dir %q\n", distDir)
		os.Exit(1)
	}
	if err := os.RemoveAll(cleanDist); err != nil {
		fmt.Fprintf(os.Stderr, "🔴 %v\n", err)
		os.Exit(1)
	}

	pages, err := alloy.DiscoverPages(pagesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "🔴 %v\n", err)
		os.Exit(1)
	}
	if len(pages) == 0 {
		fmt.Fprintf(os.Stderr, "🔴 no pages found in %s\n", pagesDir)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stdout, "\n🔨 Building production bundles\n")

	cssPath := filepath.Join(alloy.DefaultAppDir, "app.css")
	sharedCSS, err := alloy.RunTailwind(cssPath, filepath.Dir(pagesDir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "🔴 %v\n", err)
		os.Exit(1)
	}
	sharedCSSPath, err := alloy.SaveCSS(sharedCSS, distDir, "shared")
	if err != nil {
		fmt.Fprintf(os.Stderr, "🔴 %v\n", err)
		os.Exit(1)
	}

	clientInputs := make([]alloy.ClientEntry, 0, len(pages))
	for _, page := range pages {
		clientInputs = append(clientInputs, alloy.ClientEntry{
			Name:      page.Name,
			Component: page.Component,
			RootID:    page.RootID,
		})
	}

	clientAssets, err := alloy.BuildClientBundles(clientInputs, distDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "🔴 %v\n", err)
		os.Exit(1)
	}

	for _, page := range pages {
		if err := buildPage(page, distDir, clientAssets[page.Name], sharedCSSPath); err != nil {
			fmt.Fprintf(os.Stderr, "🔴 %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stdout, "✅ Build complete: %d pages ➡️ %s\n", len(pages), alloy.FormatPath(distDir))
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
		fmt.Fprintf(os.Stderr, "🔴 create dist dir: %v\n", err)
		os.Exit(1)
	}

	pages, err := alloy.DiscoverPages(pagesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "🔴 %v\n", err)
		os.Exit(1)
	}
	if len(pages) == 0 {
		fmt.Fprintf(os.Stderr, "🔴 no pages found in %s\n", pagesDir)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n➡️ Shutting down...")
		cancel()
	}()

	initialBuildDone := make(chan struct{})

	var g errgroup.Group

	g.Go(func() error {
		fmt.Fprintf(os.Stdout, "\n👀 Watching %d pages in %s\n", len(pages), alloy.FormatPath(pagesDir))
		err := alloy.WatchAndBuild(ctx, pages, distDir, initialBuildDone)
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
			return fmt.Errorf("🔴 air not found in PATH")
		}

		configPath := ".air.toml"
		if !fileExists(configPath) {
			tmpConfig, err := os.CreateTemp("", "air-*.toml")
			if err != nil {
				return fmt.Errorf("🔴 create temp air config: %w", err)
			}
			configPath = tmpConfig.Name()
			defer os.Remove(configPath)

			defaultConfig := `
root = "."
tmp_dir = ".alloy/tmp"

[build]
cmd = "go build -o .alloy/tmp/server main.go"
entrypoint = [".alloy/tmp/server"]
include_ext = ["go", "json", "js", "css"]
exclude_dir = ["node_modules", ".git", ".alloy"]

[log]
time = false
main_only = true
silent = true

[proxy]
enabled = true
proxy_port = 3000
app_port = 8080
`
			if _, err := tmpConfig.WriteString(defaultConfig); err != nil {
				return fmt.Errorf("🔴 write air config: %w", err)
			}
			tmpConfig.Close()
		}

		cmd := exec.Command(airPath, "-c", configPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		fmt.Fprintf(os.Stdout, "🔄 Starting live reload\n\n")
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("🔴 start air: %w", err)
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
		fmt.Fprintf(os.Stderr, "🔴 %v\n", err)
		os.Exit(1)
	}
}

func buildPage(page alloy.PageSpec, distDir string, client alloy.ClientAssets, sharedCSSPath string) error {
	if distDir == "" {
		return fmt.Errorf("🔴 out dir required")
	}

	serverJS, _, err := alloy.BuildServerBundle(page.Component)
	if err != nil {
		return fmt.Errorf("🔴 build server %s: %w", page.Component, err)
	}

	files, err := alloy.SaveServerBundle(serverJS, distDir, page.Name)
	if err != nil {
		return fmt.Errorf("🔴 save server %s: %w", page.Component, err)
	}

	files.Client = client.Entry
	files.ClientChunks = client.Chunks
	files.CSS = sharedCSSPath

	if err := alloy.WriteManifest(distDir, page.Name, *files); err != nil {
		return fmt.Errorf("🔴 write manifest %s: %w", page.Component, err)
	}

	return nil
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
