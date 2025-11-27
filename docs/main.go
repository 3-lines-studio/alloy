package main

import (
	"embed"
	"log"
	"net/http"

	"github.com/3-lines-studio/alloy"
	"github.com/3-lines-studio/alloy/docs/loader"
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
			Route:     "/:slug",
			Component: "app/pages/docs.tsx",
			Props:     loader.Docs,
		},
	}

	handler, err := alloy.Handler(embeddedDist, pages)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", handler)

	log.Println("Running @ http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
