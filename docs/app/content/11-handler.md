# Handler API

Reference for `alloy.Handler()` and `alloy.ListenAndServe()`.

## alloy.Handler

Create an HTTP handler for serving alloy pages.

```go
func Handler(filesystem fs.FS, pages []Page) (http.Handler, error)
```

### Parameters

**filesystem** (`fs.FS`): Embedded filesystem containing prebuilt assets.

- In production: use `//go:embed` to embed `app/dist/alloy/*`
- In development: pass `nil` to build on-demand when `ALLOY_DEV=1`

**pages** (`[]Page`): Slice of page definitions.

### Returns

- `http.Handler`: Handler serving all defined pages
- `error`: Build errors or configuration issues

### Example

```go
//go:embed app/dist/alloy/* public/*
var dist embed.FS

func main() {
	pages := []alloy.Page{
		{Route: "/", Component: "app/pages/home.tsx", Props: loader.Home},
		{Route: "/about", Component: "app/pages/about.tsx"},
	}

	handler, err := alloy.Handler(dist, pages)
	if err != nil {
		log.Fatal(err)
	}

	http.ListenAndServe(":8080", handler)
}
```

### Development mode

Pass `nil` filesystem for dev mode:

```go
handler, _ := alloy.Handler(nil, pages)
```

When `ALLOY_DEV=1` is set, alloy rebuilds assets on each request.

### Middleware

Chain with standard middleware:

```go
handler, _ := alloy.Handler(dist, pages)

logged := loggingMiddleware(handler)
authed := authMiddleware(logged)

http.ListenAndServe(":8080", authed)
```

### Multiple handlers

Serve different page sets at different paths:

```go
blog, _ := alloy.Handler(blogDist, blogPages)
docs, _ := alloy.Handler(docsDist, docsPages)

mux := http.NewServeMux()
mux.Handle("/blog/", http.StripPrefix("/blog", blog))
mux.Handle("/docs/", http.StripPrefix("/docs", docs))

http.ListenAndServe(":8080", mux)
```

## alloy.ListenAndServe

Convenience function combining Handler and http.ListenAndServe.

```go
func ListenAndServe(addr string, filesystem fs.FS, pages []Page) error
```

### Parameters

**addr** (`string`): Address to listen on (e.g., ":8080", "localhost:3000")

**filesystem** (`fs.FS`): Embedded filesystem (same as Handler)

**pages** (`[]Page`): Page definitions (same as Handler)

### Returns

- `error`: Server or build errors

### Example

```go
//go:embed app/dist/alloy/* public/*
var dist embed.FS

func main() {
	pages := []alloy.Page{
		{Route: "/", Component: "app/pages/home.tsx", Props: loader.Home},
	}

	log.Println("Server running on :8080")
	if err := alloy.ListenAndServe(":8080", dist, pages); err != nil {
		log.Fatal(err)
	}
}
```

Equivalent to:

```go
handler, err := alloy.Handler(dist, pages)
if err != nil {
	log.Fatal(err)
}
http.ListenAndServe(":8080", handler)
```

### When to use

Use `ListenAndServe` for simple apps.

Use `Handler` when you need:
- Custom server configuration (timeouts, TLS)
- Middleware
- Multiple handlers
- Graceful shutdown

## Static assets

Both functions serve files from `public/` automatically:

```
public/
├── favicon.ico    ➡️ http://localhost:8080/favicon.ico
└── images/
    └── logo.png   ➡️ http://localhost:8080/images/logo.png
```

Embed with:

```go
//go:embed app/dist/alloy/* public/*
var dist embed.FS
```

## Live reload

In dev mode (`ALLOY_DEV=1`), alloy serves a server-sent events endpoint at `/__alloy__/live` for browser refresh on file changes.

The dev handler automatically injects a live reload script into pages.

## Error handling

### Build errors

If a component fails to build, `Handler()` returns an error:

```go
handler, err := alloy.Handler(dist, pages)
if err != nil {
	log.Fatalf("Failed to create handler: %v", err)
}
```

### Runtime errors

If props function panics or QuickJS execution fails, alloy returns HTTP 500.

Error details logged to stderr.

## Next steps

- [Page struct](/12-page-struct) - Complete Page configuration
- [Quick start](/01-quick-start) - Basic usage
- [Deployment](/10-deployment) - Production setup
