# Quick Start

Get alloy running in under 5 minutes.

## Prerequisites

- Go 1.25 or later
- Node.js toolchain (pnpm, yarn, npm, or bun) for Tailwind CSS

## Install

```sh
go get github.com/3-lines-studio/alloy@latest
go install github.com/3-lines-studio/alloy/cmd/alloy@latest
```

Verify installation:

```sh
alloy -h
```

## Create your first project

### 1. Initialize project structure

```sh
mkdir myapp && cd myapp
go mod init myapp
go get github.com/3-lines-studio/alloy@latest
```

Create directories:

```sh
mkdir -p app/pages loader public
```

### 2. Create a page component

Create `app/pages/home.tsx`:

```tsx
export default function Home({ title, message }) {
  return (
    <div className="p-8">
      <h1 className="text-4xl font-bold">{title}</h1>
      <p className="mt-4 text-lg">{message}</p>
    </div>
  );
}
```

### 3. Add Tailwind CSS

Create `app/pages/app.css`:

```css
@import "tailwindcss";
```

Install Tailwind:

```sh
npm install tailwindcss
```

Or with your preferred package manager:

```sh
pnpm add tailwindcss  # or yarn add, or bun add
```

### 4. Create props loader

Create `loader/home.go`:

```go
package loader

import "net/http"

func Home(r *http.Request) map[string]any {
	return map[string]any{
		"title":   "Welcome to Alloy",
		"message": "React SSR for Go. No Node.js runtime.",
		"meta": map[string]any{
			"title":       "Alloy Quick Start",
			"description": "Getting started with Alloy",
		},
	}
}
```

### 5. Create server

Create `main.go`:

```go
package main

import (
	"log"

	"github.com/3-lines-studio/alloy"
	"myapp/loader"
)

func main() {
	pages := []alloy.Page{
		{
			Route:     "/",
			Component: "app/pages/home.tsx",
			Props:     loader.Home,
		},
	}

	log.Println("Server running at http://localhost:8080")
	if err := alloy.ListenAndServe(":8080", nil, pages); err != nil {
		log.Fatal(err)
	}
}
```

### 6. Run development server

```sh
ALLOY_DEV=1 go run .
```

Open http://localhost:8080

## Development mode

With `ALLOY_DEV=1`, alloy rebuilds pages and Tailwind on every request. Changes to `.tsx` files or `app.css` are reflected immediately—just refresh your browser.

## Build for production

Generate prebuilt assets:

```sh
alloy -pages app/pages -out app/dist/alloy
```

This creates:
- `app/dist/alloy/manifest.json` - Asset manifest
- `app/dist/alloy/*-server.js` - Server bundles
- `app/dist/alloy/*-client.js` - Client hydration bundles
- `app/dist/alloy/shared.css` - Compiled Tailwind CSS

Update `main.go` to embed assets:

```go
package main

import (
	"embed"
	"log"

	"github.com/3-lines-studio/alloy"
	"myapp/loader"
)

//go:embed app/dist/alloy/* public/*
var dist embed.FS

func main() {
	pages := []alloy.Page{
		{
			Route:     "/",
			Component: "app/pages/home.tsx",
			Props:     loader.Home,
		},
	}

	handler, err := alloy.Handler(dist, pages)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Server running at http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}
```

Build and run:

```sh
alloy                  # Build assets
go build -o myapp      # Compile binary
./myapp                # Run (ALLOY_DEV unset = use prebuilt assets)
```

You now have a single binary with embedded assets. No Node.js required.

## Next steps

- [Pages and routing](/03-pages-and-routing) - Dynamic routes, route params
- [Production builds](/09-production-builds) - alloy CLI options
- [Deployment](/10-deployment) - Docker, systemd, cloud platforms
- [Client hydration](/05-client-hydration) - Add interactivity
