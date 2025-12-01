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
```sh
# Terminal 1: Watch and rebuild assets
cd cmd/sample
alloy dev  # Auto-discovers app/pages/*.tsx

# Terminal 2: Run server with hot reload
air
```

**Run documentation site:**
```sh
# Terminal 1: Watch assets
cd docs
alloy dev

# Terminal 2: Run server
air
```

**Build for production:**
```sh
alloy build                                    # Auto-discovers app/pages/*.tsx
alloy build -pages app/pages  # Uses default .alloy/dist
```

## Core Architecture

### SSR Rendering Flow

- Components are bundled separately for server (CommonJS) and client (ESM)
- Server bundle uses `renderToString` from react-dom/server.edge
- Client bundle uses `hydrateRoot` from react-dom/client
- QuickJS runtime pool executes server bundles in isolated JavaScript environments
- Props are serialized as JSON and embedded in HTML for client hydration

**Key functions in `render.go`:**
- `RenderTSXFileWithHydration()`: Main rendering entry point (requires prebuilt bundles)
- `executeSSR()`: Runs JavaScript in QuickJS with timeout support
- `BuildServerBundle()`: Creates server bundle using esbuild (CLI use)
- `BuildClientBundles()`: Creates client bundles with code splitting (CLI use)
- `RunTailwind()`: Compiles CSS with Tailwind (CLI use)
- `SaveCSS()`: Writes compiled CSS to disk (CLI use)

### Runtime Pool Pattern

- Pre-allocated QuickJS runtimes (default: GOMAXPROCS * 2)
- Polyfills injected for browser APIs (TextEncoder, performance, etc.)
- Interrupt handlers for timeout enforcement
- Runtimes are disposed after each use (ephemeral pool)

### Build and Asset Strategy

**Development mode:**
- `alloy dev` runs native esbuild and tailwind watchers
- Watchers rebuild on file changes and write to disk
- Assets written without content hashes (e.g., `home-server.js`, `home-client.js`)
- Manifest.json updated on each rebuild
- Server reads bundles from disk via `RegisterPrebuiltBundleFromFS()`
- Air watches Go files and restarts server on changes

**Production mode:**
- `alloy build` creates optimized bundles with content hashes
- Assets embedded in Go binary using `//go:embed`
- Bundles registered via `RegisterPrebuiltBundleFromFS()` at startup
- Immutable caching with 1-year cache headers

**Key insight:** Both dev and prod use the same code path - bundles are always read from the cache after being registered. The only difference is dev reads from disk filesystem while prod reads from embedded filesystem.

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
- `.alloy/dist/`: Generated bundles

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
- Each page directory needs an `app.css` file for Tailwind

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

1. Run `alloy build` to create optimized bundles with content hashes
2. Embed assets with `//go:embed .alloy/dist/* public/*`
3. Set `PRODUCTION=1` environment variable
4. Server detects production mode and uses embedded filesystem
5. Deploy single binary

## Key Files

- `render.go`: SSR rendering, QuickJS execution, bundle registration
- `handler.go`: HTTP handler, routing, asset serving
- `cmd/alloy/main.go`: CLI tool with `build` and `dev` commands
