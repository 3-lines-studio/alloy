package loader

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
)

func Home(r *http.Request) map[string]any {
	return map[string]any{
		"meta": []map[string]any{
			{"title": "Alloy"},
			{"name": "description", "content": "Go + React"},
			{"property": "og:title", "content": "Alloy"},
			{"property": "og:description", "content": "Go + React"},
			{"property": "og:type", "content": "website"},
		},
	}
}

type DocEntry struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

func Docs(r *http.Request) map[string]any {
	slug := strings.TrimSpace(r.PathValue("slug"))
	contentDir := filepath.Join("app", "content")
	entries := listDocs(contentDir)
	if slug == "" && len(entries) > 0 {
		slug = entries[0].Slug
	}
	markdown := loadMarkdown(contentDir, slug)
	html := markdownToHTML(markdown)

	return map[string]any{
		"slug":    slug,
		"html":    html,
		"entries": entries,
		"title":   findTitle(entries, slug),
	}
}

func listDocs(dir string) []DocEntry {
	entries := []DocEntry{}
	items, err := os.ReadDir(dir)
	if err != nil {
		return entries
	}

	for _, item := range items {
		if item.IsDir() {
			continue
		}
		if filepath.Ext(item.Name()) != ".md" {
			continue
		}
		slug := strings.TrimSuffix(item.Name(), filepath.Ext(item.Name()))
		title := headingFromFile(filepath.Join(dir, item.Name()))
		if title == "" {
			title = slug
		}
		entries = append(entries, DocEntry{
			Slug:  slug,
			Title: title,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Slug < entries[j].Slug
	})

	return entries
}

func findTitle(entries []DocEntry, slug string) string {
	if slug == "" {
		return ""
	}
	for _, entry := range entries {
		if entry.Slug == slug {
			return entry.Title
		}
	}
	return slug
}

func loadMarkdown(dir string, slug string) string {
	if slug == "" {
		return ""
	}

	if strings.Contains(slug, "..") {
		return "# Invalid slug\n"
	}

	if data, err := os.ReadFile(filepath.Join(dir, slug+".md")); err == nil {
		return string(data)
	}

	return "# Not found\n"
}

func headingFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return firstHeading(string(data))
}

func firstHeading(content string) string {
	lines := strings.SplitSeq(content, "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
	}
	return ""
}

func markdownToHTML(source string) string {
	md := goldmark.New(
		goldmark.WithExtensions(
			highlighting.NewHighlighting(
				highlighting.WithStyle("onedark"),
				highlighting.WithFormatOptions(
					chromahtml.WithClasses(true),
				),
			),
		),
	)

	var buf bytes.Buffer
	if err := md.Convert([]byte(source), &buf); err != nil {
		return ""
	}

	return buf.String()
}
