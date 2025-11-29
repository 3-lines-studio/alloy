package main

import (
	"context"
	"flag"
	"fmt"
	"log"
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

	pages, err := alloy.DiscoverPages(pagesDir)
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
			Name:      page.Name,
			Component: page.Component,
			RootID:    page.RootID,
		})
	}

	clientAssets, err := alloy.BuildClientBundles(clientInputs, distDir)
	if err != nil {
		log.Fatal(err)
	}

	for _, page := range pages {
		if err := buildPage(page, distDir, clientAssets[page.Name], sharedCSSPath); err != nil {
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

	pages, err := alloy.DiscoverPages(pagesDir)
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

func buildPage(page alloy.PageSpec, distDir string, client alloy.ClientAssets, sharedCSSPath string) error {
	if distDir == "" {
		return fmt.Errorf("out dir required")
	}

	serverJS, _, err := alloy.BuildServerBundle(page.Component)
	if err != nil {
		return fmt.Errorf("build server %s: %w", page.Component, err)
	}

	files, err := alloy.SaveServerBundle(serverJS, distDir, page.Name)
	if err != nil {
		return fmt.Errorf("save server %s: %w", page.Component, err)
	}

	files.Client = client.Entry
	files.ClientChunks = client.Chunks
	files.CSS = sharedCSSPath

	if err := alloy.WriteManifest(distDir, page.Name, *files); err != nil {
		return fmt.Errorf("write manifest %s: %w", page.Component, err)
	}

	log.Printf("built %s -> %s (client: %s)", page.Component, distDir, client.Entry)
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
