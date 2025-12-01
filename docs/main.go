package main

import (
	"embed"
	"log"
	"net/http"

	"github.com/3-lines-studio/alloy"
	"github.com/3-lines-studio/alloy/docs/loader"
)

//go:embed .alloy/dist/* public/*
var dist embed.FS

func main() {
	alloy.Init(dist)

	mux := http.NewServeMux()
	mux.Handle("/", alloy.NewPage("app/pages/home.tsx").WithLoader(loader.Home))
	mux.Handle("/{slug}", alloy.NewPage("app/pages/docs.tsx").WithLoader(loader.Docs))

	log.Println("Running @ http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
