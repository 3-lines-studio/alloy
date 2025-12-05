package main

import (
	"embed"
	"fmt"
	"net/http"

	"github.com/3-lines-studio/alloy"
	"github.com/3-lines-studio/alloy/docs/loader"
)

//go:embed dist/build/* public/*
var dist embed.FS

func main() {
	alloy.Init(dist)

	mux := http.NewServeMux()
	mux.Handle("/", alloy.NewPage("app/pages/home.tsx").WithLoader(loader.Home))
	mux.Handle("/{slug}", alloy.NewPage("app/pages/docs.tsx").WithLoader(loader.Docs))

	handler := alloy.AssetsMiddleware()(mux)
	http.ListenAndServe(":8080", handler)

	fmt.Println("Running @ http://localhost:8080")
}
