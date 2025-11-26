package main

import (
	"embed"
	"log"
	"net/http"

	"github.com/3-lines-studio/alloy"
	"github.com/3-lines-studio/alloy/cmd/sample/loader"
)

//go:embed app/dist/alloy/* public/*
var embeddedDist embed.FS

func main() {
	pages := []alloy.Page{
		{
			Route:     "/",
			Component: "app/pages/home.tsx",
			Props:     loader.Home,
		},
		{
			Route:     "/blog/:slug",
			Component: "app/pages/home.tsx",
			Props:     loader.Blog,
		},
		{
			Route:     "/store/:store-slug/product/:product-slug",
			Component: "app/pages/product.tsx",
			Props:     loader.Product,
		},
		{
			Route:     "/about",
			Component: "app/pages/about.tsx",
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
