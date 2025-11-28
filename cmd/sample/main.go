package main

import (
	"embed"
	"log"
	"net/http"

	"github.com/3-lines-studio/alloy"
	"github.com/3-lines-studio/alloy/cmd/sample/loader"
)

//go:embed dist/* public/*
var dist embed.FS

func main() {
	alloy.Init(dist)

	mux := http.NewServeMux()
	mux.Handle("/", alloy.NewPage("app/pages/home.tsx").WithLoader(loader.Home))
	mux.Handle("/blog/{slug}", alloy.NewPage("app/pages/home.tsx").WithLoader(loader.Blog))
	mux.Handle("/store/{store-slug}/product/{product-slug}", alloy.NewPage("app/pages/product.tsx").WithLoader(loader.Product))
	mux.Handle("/about", alloy.NewPage("app/pages/about.tsx"))
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	log.Println("Server running at http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
