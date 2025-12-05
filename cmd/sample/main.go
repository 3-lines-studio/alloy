package main

import (
	"embed"
	"fmt"
	"net/http"
	"os"

	"github.com/3-lines-studio/alloy"
	"github.com/3-lines-studio/alloy/cmd/sample/loader"
)

//go:embed .alloy/dist/* public/*
var dist embed.FS

func main() {
	alloy.Init(dist)

	mux := http.NewServeMux()
	mux.Handle("/", alloy.NewPage("app/pages/home.tsx").WithLoader(loader.Home))
	mux.Handle("/blog/{slug}", alloy.NewPage("app/pages/home.tsx").WithLoader(loader.Blog))
	mux.Handle("/store/{storeSlug}/product/{productSlug}", alloy.NewPage("app/pages/product.tsx").WithLoader(loader.Product))
	mux.Handle("/about", alloy.NewPage("app/pages/about.tsx"))
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	fmt.Println("✅ Server running at http://localhost:8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		fmt.Fprintf(os.Stderr, "🔴 %s\n", err)
		os.Exit(1)
	}
}
