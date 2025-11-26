package main

import (
	"embed"
	"log"
	"net/http"
	"time"

	"alloy"
)

//go:embed dist/alloy/* public/*
var embeddedDist embed.FS

func main() {
	pages := []alloy.Page{
		{
			Route:     "/",
			Component: "pages/home.tsx",
			Props: func(r *http.Request) map[string]any {
				return map[string]any{
					"title":     "Alloy sample",
					"items":     []string{"First", "Second", "Third"},
					"timestamp": time.Now().Format(time.RFC3339),
					"meta": map[string]any{
						"title":       "Alloy sample storefront",
						"description": "Sample SSR storefront page rendered with Alloy.",
						"url":         "http://localhost:8080/",
						"image":       "http://localhost:8080/favicon.ico",
						"ogType":      "website",
					},
				}
			},
		},
		{
			Route:     "/blog/:slug",
			Component: "pages/home.tsx",
			Props: func(r *http.Request) map[string]any {
				params := alloy.RouteParams(r)
				slug := params["slug"]
				title := "Alloy blog"
				if slug != "" {
					title = "Blog: " + slug
				}

				return map[string]any{
					"title":     title,
					"items":     []string{"First", "Second", "Third"},
					"timestamp": time.Now().Format(time.RFC3339),
					"slug":      slug,
					"meta": map[string]any{
						"title":       title,
						"description": "Blog detail page rendered with Alloy.",
						"url":         "http://localhost:8080/blog/" + slug,
						"image":       "http://localhost:8080/favicon.ico",
						"ogType":      "article",
					},
				}
			},
		},
		{
			Route:     "/store/:store-slug/product/:product-slug",
			Component: "pages/product.tsx",
			Props: func(r *http.Request) map[string]any {
				params := alloy.RouteParams(r)
				store := params["store-slug"]
				product := params["product-slug"]
				title := "Product detail"
				if store != "" && product != "" {
					title = store + " / " + product
				}

				return map[string]any{
					"title":        title,
					"store":        store,
					"product":      product,
					"timestamp":    time.Now().Format(time.RFC3339),
					"availability": "In stock",
					"price":        "$39.00",
					"meta": map[string]any{
						"title":       title,
						"description": "Product detail page rendered with Alloy.",
						"url":         "http://localhost:8080/store/" + store + "/product/" + product,
						"image":       "http://localhost:8080/favicon.ico",
						"ogType":      "product",
					},
				}
			},
		},
		{
			Route:     "/about",
			Component: "pages/about.tsx",
		},
	}

	handler, err := alloy.Handler(embeddedDist, pages)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", handler)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	log.Println("Server running at http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
