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
		"meta": []map[string]any{
			{"title": "Alloy sample storefront"},
			{"name": "description", "content": "Sample SSR storefront page rendered with Alloy."},
			{"name": "robots", "content": "index, follow"},
			{"property": "og:title", "content": "Alloy sample storefront"},
			{"property": "og:description", "content": "Sample SSR storefront page rendered with Alloy."},
			{"property": "og:url", "content": "http://localhost:8080/"},
			{"property": "og:image", "content": "http://localhost:8080/favicon.ico"},
			{"property": "og:type", "content": "website"},
			{"tagName": "link", "rel": "canonical", "href": "http://localhost:8080/"},
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

	url := "http://localhost:8080/blog/" + slug

	return map[string]any{
		"title":     title,
		"items":     []string{"First", "Second", "Third"},
		"timestamp": time.Now().Format(time.RFC3339),
		"slug":      slug,
		"meta": []map[string]any{
			{"title": title},
			{"name": "description", "content": "Blog detail page rendered with Alloy."},
			{"property": "og:title", "content": title},
			{"property": "og:description", "content": "Blog detail page rendered with Alloy."},
			{"property": "og:url", "content": url},
			{"property": "og:image", "content": "http://localhost:8080/favicon.ico"},
			{"property": "og:type", "content": "article"},
			{"tagName": "link", "rel": "canonical", "href": url},
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

	url := "http://localhost:8080/store/" + store + "/product/" + product

	return map[string]any{
		"title":        title,
		"store":        store,
		"product":      product,
		"timestamp":    time.Now().Format(time.RFC3339),
		"availability": "In stock",
		"price":        "$39.00",
		"meta": []map[string]any{
			{"title": title},
			{"name": "description", "content": "Product detail page rendered with Alloy."},
			{"property": "og:title", "content": title},
			{"property": "og:description", "content": "Product detail page rendered with Alloy."},
			{"property": "og:url", "content": url},
			{"property": "og:image", "content": "http://localhost:8080/favicon.ico"},
			{"property": "og:type", "content": "product"},
			{"tagName": "link", "rel": "canonical", "href": url},
		},
	}
}
