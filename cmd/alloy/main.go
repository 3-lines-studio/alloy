package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/3-lines-studio/alloy"
)

type pageSpec struct {
	component string
	name      string
	rootID    string
}

func main() {
	var pagesDir string
	var distDir string

	flag.StringVar(&pagesDir, "pages", "", "directory containing page components (.tsx)")
	flag.StringVar(&distDir, "out", "", "output directory for prebuilt bundles")
	flag.Parse()

	pagesDir = defaultPagesDir(pagesDir)
	if pagesDir == "" {
		log.Fatal("pages dir required")
	}
	distDir = defaultDistDir(distDir, pagesDir)
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

	cssPath := filepath.Join(pagesDir, "app.css")
	sharedCSS, err := alloy.BuildSharedCSS(cssPath)
	if err != nil {
		log.Fatal(err)
	}
	sharedCSSPath, err := alloy.SaveSharedCSS(sharedCSS, distDir, "shared")
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
}

func discoverPages(dir string) ([]pageSpec, error) {
	if dir == "" {
		return nil, fmt.Errorf("pages dir required")
	}

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
	files.SharedCSS = sharedCSSPath

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

	candidates := []string{
		filepath.Join("app", "pages"),
		"pages",
	}
	for _, candidate := range candidates {
		if dirExists(candidate) {
			return candidate
		}
	}

	return ""
}

func defaultDistDir(flagValue, pagesDir string) string {
	if flagValue != "" {
		return flagValue
	}

	if pagesDir == "" {
		return ""
	}

	cleanPages := filepath.Clean(pagesDir)
	baseDir := filepath.Dir(cleanPages)
	if baseDir == "." {
		return "dist/alloy"
	}

	return filepath.Join(baseDir, "dist/alloy")
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
