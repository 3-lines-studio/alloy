package loader

import (
	"net/http"
	"time"

	"github.com/3-lines-studio/alloy"
)

func Home(r *http.Request) map[string]any {
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
}

func Blog(r *http.Request) map[string]any {
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
}

func Product(r *http.Request) map[string]any {
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
}
