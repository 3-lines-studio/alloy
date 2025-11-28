# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Alloy is a Go-based React/TSX SSR framework with optional hydration and asset serving. It uses QuickJS for server-side rendering, esbuild for bundling, and Tailwind CSS for styling. The framework enables writing React components that are rendered on the server (Go) and optionally hydrated on the client.

**Requirements:** Go 1.25+ and Node toolchain with `tailwindcss` via `pnpm`, `yarn`, `npm`, `bun`, or `npx`.

## Development Commands

**Run tests:**
```sh
go test ./...
```

**Development mode:**
Set `ALLOY_DEV=1` to rebuild on each request with live reload.

**Run sample app:**
```sh
cd cmd/sample
ALLOY_DEV=1 go run main.go
```

**Run documentation site:**
```sh
cd docs
ALLOY_DEV=1 go run main.go
```

**Build for production:**
```sh
alloy                                    # Auto-discovers app/pages/*.tsx
alloy -pages app/pages -out app/dist/alloy  # Explicit paths
```

## Core Architecture

### SSR Rendering Flow

- Components are bundled separately for server (CommonJS) and client (ESM)
- Server bundle uses `renderToString` from react-dom/server.edge
- Client bundle uses `hydrateRoot` from react-dom/client
- QuickJS runtime pool executes server bundles in isolated JavaScript environments
- Props are serialized as JSON and embedded in HTML for client hydration

**Key functions in `render.go`:**
- `RenderTSXFileWithHydration()`: Main rendering entry point
- `executeSSR()`: Runs JavaScript in QuickJS with timeout support
- `bundleTSXFile()`: Creates server bundle using esbuild
- `bundleClientJS()`: Creates client hydration bundle
- `buildTailwindCSS()`: Compiles CSS with Tailwind

### Runtime Pool Pattern

- Pre-allocated QuickJS runtimes (default: GOMAXPROCS * 2)
- Polyfills injected for browser APIs (TextEncoder, performance, etc.)
- Interrupt handlers for timeout enforcement
- Runtimes are disposed after each use (ephemeral pool)

Implementation in `quickjs.go`.

### Bundle Caching Strategy

**Development mode (`ALLOY_DEV=1`):**
- File-stamp based cache invalidation (tracks modification time + size)
- Tracks all dependencies for accurate invalidation
- Tailwind CSS watcher for fast rebuilds
- Live reload via SSE at `/__live`
- Dev CSS cached in `.alloy-cache/`

**Production mode:**
- Prebuilt bundles registered via `RegisterPrebuiltBundle()`
- Assets embedded in Go binary using `//go:embed`
- Immutable caching with content hashes in filenames

Implementation in `bundle.go`.

### Routing System

Simple path-based routing with named parameters (`:param`).

**Example route:**
```go
Page{
    Route:     "/blog/:slug",
    Component: "app/pages/blog.tsx",
    Props:     loaderFunction,
}
```

Extract parameters:
```go
params := alloy.RouteParams(r)
slug := params["slug"]
```

Implementation in `handler.go`.

### Loader Functions

Go functions that provide props to React components:

```go
func Blog(r *http.Request) map[string]any {
    params := alloy.RouteParams(r)
    return map[string]any{
        "slug": params["slug"],
        "meta": map[string]any{
            "title": "Blog post",
        },
    }
}
```

Props are passed to both SSR and client hydration.

### Asset Serving

Three asset root directories:
- `public/`: Public static assets (images, favicon, etc.)
- `app/dist/`: Generated bundles
- `dist/`: Alternative dist location

**Cache headers:**
- Hashed assets: 1 year immutable cache
- Non-hashed: 5 minute cache

Implementation in `handler.go`.

## Important Conventions

### Project Structure

```
myapp/
├── app/
│   ├── pages/           # React components (.tsx)
│   │   ├── home.tsx
│   │   └── app.css      # Required Tailwind config
│   └── dist/alloy/      # Generated bundles
├── loader/              # Go loader functions
│   └── loaders.go
├── public/              # Static assets
└── main.go              # Server entry point
```

### Component Requirements

- Must export default function
- Props typed as function parameters
- Each page needs sibling `app.css` in development mode

### RootID Convention

Auto-generated as `{component-name}-root`:
- `home.tsx` → `home-root`
- Used for hydration target and props script tag

### Meta Tag Support

Props can include `meta` object for SEO:

```go
map[string]any{
    "meta": map[string]any{
        "title":       "Page title",
        "description": "Page description",
        "url":         "Canonical URL",
        "image":       "OG image",
        "ogType":      "website",
    },
}
```

### Production Workflow

1. Run `alloy` CLI to prebuild bundles
2. Embed assets with `//go:embed app/dist/alloy/* public/*`
3. Call `alloy.Handler(dist, pages)` to create HTTP handler
4. Deploy single binary

## Key Files

- `render.go`: Main SSR rendering logic, bundling, QuickJS execution
- `handler.go`: HTTP handler, routing, asset serving
- `bundle.go`: esbuild integration, cache management
- `quickjs.go`: QuickJS runtime pool and execution
- `cmd/alloy/`: CLI tool for building bundles
