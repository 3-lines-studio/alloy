# Project Structure

Recommended directory layout for alloy projects.

## Standard structure

```
myapp/
├── main.go                 # Server entry point
├── go.mod                  # Go module definition
├── go.sum
│
├── app/
│   ├── pages/              # React page components
│   │   ├── home.tsx
│   │   ├── about.tsx
│   │   ├── blog.tsx
│   │   └── app.css         # Tailwind styles
│   │
│   ├── components/         # Shared React components
│   │   ├── Header.tsx
│   │   ├── Footer.tsx
│   │   └── Button.tsx
│   │
│   └── dist/alloy/         # Generated assets (git-ignored)
│       ├── manifest.json
│       ├── *-server.js
│       ├── *-client.js
│       └── shared.css
│
├── loader/                 # Props functions
│   └── loaders.go
│
├── public/                 # Static assets
│   ├── favicon.ico
│   ├── robots.txt
│   └── images/
│
├── package.json            # Node.js dependencies (Tailwind, React types)
├── package-lock.json
│
└── .gitignore
```

## Directory purposes

### `main.go`

Server entry point. Defines pages, creates handler, starts HTTP server.

```go
package main

import (
	"embed"
	"log"
	"net/http"

	"github.com/3-lines-studio/alloy"
	"myapp/loader"
)

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

### `app/pages/`

React page components. Each `.tsx` file can be mapped to a route.

**Example:** `app/pages/home.tsx`

```tsx
export default function Home({ title, items }) {
	return (
		<div className="p-8">
			<h1 className="text-3xl font-bold">{title}</h1>
			<ul>
				{items.map(item => <li key={item}>{item}</li>)}
			</ul>
		</div>
	);
}
```

**Naming:** Use descriptive names. The filename doesn't dictate the route—you map routes in `main.go`.

### `app/pages/app.css`

Tailwind CSS entry point.

```css
@import "tailwindcss";

/* Custom styles */
.custom-class {
	color: red;
}
```

Compiled to `app/dist/alloy/shared.css` by the `alloy` CLI.

### `app/components/`

Shared React components used across pages.

**Example:** `app/components/Header.tsx`

```tsx
export function Header({ title }: { title: string }) {
	return (
		<header className="bg-slate-900 p-4">
			<h1 className="text-white text-2xl">{title}</h1>
		</header>
	);
}
```

Import in pages:

```tsx
import { Header } from '../components/Header';

export default function Home({ title }) {
	return (
		<div>
			<Header title={title} />
			<main>...</main>
		</div>
	);
}
```

### `app/dist/alloy/`

Generated assets from `alloy` CLI. **Add to `.gitignore`.**

Contains:
- Server bundles (`*-server.js`)
- Client bundles (`*-client.js`)
- Compiled CSS (`shared.css`)
- Asset manifest (`manifest.json`)

Regenerated on each `alloy` build.

### `loader/`

Go package with props functions.

**Example:** `loader/loaders.go`

```go
package loader

import (
	"net/http"
	"github.com/3-lines-studio/alloy"
)

func Home(r *http.Request) map[string]any {
	return map[string]any{
		"title": "Welcome",
		"items": []string{"One", "Two", "Three"},
	}
}

func Blog(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	slug := params["slug"]

	post := fetchBlogPost(slug)

	return map[string]any{
		"title":   post.Title,
		"content": post.Content,
	}
}
```

Organization tip: Split loaders into multiple files for large projects:

```
loader/
├── home.go
├── blog.go
└── user.go
```

### `public/`

Static files served directly at root path.

```
public/
├── favicon.ico        ➡️ Served at /favicon.ico
├── robots.txt         ➡️ Served at /robots.txt
└── images/
    └── logo.png       ➡️ Served at /images/logo.png
```

Embed in binary with `//go:embed`:

```go
//go:embed app/dist/alloy/* public/*
var dist embed.FS
```

### `package.json`

Node.js dependencies for Tailwind and TypeScript types.

```json
{
  "dependencies": {
    "tailwindcss": "^4.1.0",
    "@types/react": "^18.3.0",
    "@types/react-dom": "^18.3.0",
    "react": "^18.3.0",
    "react-dom": "^18.3.0"
  }
}
```

**Note:** React is only needed for TypeScript types and development. The runtime is handled by alloy (QuickJS).

## Alternative structures

### Minimal (no components)

```
myapp/
├── main.go
├── pages/
│   ├── home.tsx
│   └── app.css
├── loader/
│   └── loaders.go
└── public/
```

Works with `alloy --pages pages`.

### Monorepo

```
workspace/
├── backend/            # Go API
│   └── main.go
│
├── frontend/           # Alloy app
│   ├── main.go
│   ├── app/pages/
│   └── loader/
│
└── shared/             # Shared Go packages
    └── models/
```

Each Go module has its own `go.mod`.

### Multi-app

Serve multiple alloy apps from one binary:

```go
pages1 := []alloy.Page{
	{Route: "/", Component: "app1/pages/home.tsx"},
}
handler1, _ := alloy.Handler(dist1, pages1)

pages2 := []alloy.Page{
	{Route: "/", Component: "app2/pages/home.tsx"},
}
handler2, _ := alloy.Handler(dist2, pages2)

mux := http.NewServeMux()
mux.Handle("/app1/", http.StripPrefix("/app1", handler1))
mux.Handle("/app2/", http.StripPrefix("/app2", handler2))

http.ListenAndServe(":8080", mux)
```

## Gitignore

Recommended `.gitignore`:

```
# Alloy generated assets
app/dist/

# Node modules
node_modules/
package-lock.json
pnpm-lock.yaml
yarn.lock

# Go build
*.exe
*.exe~
*.dll
*.so
*.dylib
*.test
*.out
vendor/

# IDE
.vscode/
.idea/
*.swp
*.swo

# OS
.DS_Store
Thumbs.db
```

## TypeScript configuration

Optional `tsconfig.json` for IDE support:

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "lib": ["ES2020", "DOM"],
    "jsx": "react",
    "module": "ESNext",
    "moduleResolution": "node",
    "strict": true,
    "skipLibCheck": true,
    "esModuleInterop": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true
  },
  "include": ["app/**/*.tsx", "app/**/*.ts"],
  "exclude": ["node_modules", "app/dist"]
}
```

**Note:** alloy uses esbuild, which ignores `tsconfig.json`. This is only for editor tooling (autocomplete, type checking).

## Build artifacts

Production binaries embed assets:

```sh
alloy                   # Generate app/dist/alloy/
go build -o myapp       # Binary includes embedded dist
```

The binary is self-contained. Ship `myapp` alone—no additional files needed.

## Next steps

- [Quick start](/01-quick-start) - Initialize a new project
- [Pages and routing](/03-pages-and-routing) - Define routes
- [Styling](/06-styling) - Tailwind setup
