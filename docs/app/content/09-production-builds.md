# Production Builds

Use the `alloy` CLI to precompile assets for production deployment.

## Overview

The `alloy` command:
1. Discovers `.tsx` pages in your pages directory
2. Builds server bundles (for QuickJS rendering)
3. Builds client bundles (for hydration)
4. Compiles Tailwind CSS
5. Generates `manifest.json` mapping pages to assets

## Basic usage

```sh
alloy
```

With default structure (`app/pages/`), this outputs to `app/dist/alloy/`.

## CLI options

### `-pages`

Specify pages directory:

```sh
alloy --pages pages
```

**Default behavior:**
- Tries `app/pages/` first
- Falls back to `pages/`
- Errors if neither exists

### `-out`

Specify output directory:

```sh
alloy --out dist/production
```

**Default behavior:**
- If pages dir is `app/pages` ➡️ outputs to `app/dist/alloy`
- If pages dir is `pages` ➡️ outputs to `dist/alloy`

### Full example

```sh
alloy --pages src/pages --out build/alloy
```

## Output structure

After running `alloy`, your dist directory contains:

```
app/dist/alloy/
├── home-server.js          # Server bundle for home.tsx
├── home-client.js          # Client hydration for home.tsx
├── about-server.js         # Server bundle for about.tsx
├── about-client.js         # Client hydration for about.tsx
├── shared.css              # Compiled Tailwind CSS
└── manifest.json           # Asset mappings
```

## Manifest format

The manifest maps page names to their assets:

```json
{
  "home": {
    "server": "app/dist/alloy/home-server.js",
    "client": "app/dist/alloy/home-client.js",
    "chunks": [],
    "css": "app/dist/alloy/shared.css",
    "sharedCSS": "app/dist/alloy/shared.css"
  },
  "about": {
    "server": "app/dist/alloy/about-server.js",
    "client": "app/dist/alloy/about-client.js",
    "chunks": [],
    "css": "app/dist/alloy/shared.css",
    "sharedCSS": "app/dist/alloy/shared.css"
  }
}
```

## Page discovery

The CLI finds all `.tsx` files in your pages directory:

```
app/pages/
├── home.tsx       ➡️ Becomes "home" in manifest
├── about.tsx      ➡️ Becomes "about"
├── blog.tsx       ➡️ Becomes "blog"
└── app.css        ➡️ Used for Tailwind compilation
```

**Page name** = filename without extension.

**Note:** The CLI doesn't understand route mappings. You map page names to routes in your Go code.

## Server bundles

Server bundles (`*-server.js`) are executed by QuickJS to render HTML.

- Contain your React component and dependencies
- Optimized for QuickJS (not browser)
- Include polyfills for Node.js APIs (console, TextEncoder, etc.)

## Client bundles

Client bundles (`*-client.js`) run in the browser for hydration.

- Only components using hooks (useState, useEffect) are hydrated
- Code-split automatically
- esbuild handles bundling and minification

**Shared chunks** are extracted to reduce duplication when multiple pages use the same components.

## CSS compilation

The CLI looks for `app/pages/app.css` and compiles it with Tailwind v4:

```css
/* app/pages/app.css */
@import "tailwindcss";
```

Output: `dist/alloy/shared.css`

**Requirements:**
- Tailwind must be installed (`npm install tailwindcss`)
- Package manager detected automatically (pnpm, yarn, npm, bun, or npx)

## Build workflow

Complete production build workflow:

```sh
# 1. Install JS dependencies (one-time)
npm install

# 2. Build alloy assets
alloy

# 3. Build Go binary with embedded assets
go build -o myapp
```

Your Go code should use `//go:embed` to include the dist:

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

When `ALLOY_DEV` is unset, `alloy.Handler` uses prebuilt assets from the embedded filesystem.

## Incremental builds

The CLI removes and recreates the output directory on each run. There's no incremental build support—every invocation is a full rebuild.

For fast iteration, use [dev workflow](/08-dev-workflow) with `ALLOY_DEV=1`.

## Build errors

### No pages found

```
no pages found in app/pages
```

**Fix:** Ensure `.tsx` files exist in your pages directory.

### Tailwind not found

```
error building CSS: tailwindcss not found
```

**Fix:** Run `npm install tailwindcss` (or pnpm/yarn/bun).

### Component build errors

```
build server app/pages/home.tsx: ...
```

Check your TypeScript/JSX syntax. esbuild errors are passed through.

## CI/CD integration

Example GitHub Actions workflow:

```yaml
name: Build
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.25'
      - uses: actions/setup-node@v3
        with:
          node-version: '20'
      - run: npm install
      - run: go install github.com/3-lines-studio/alloy/cmd/alloy@latest
      - run: alloy
      - run: go build -o app
      - run: ./app &  # Start server
      - run: curl http://localhost:8080  # Smoke test
```

## Production optimizations

The CLI automatically:
- Minifies JavaScript (esbuild default)
- Removes development-only code
- Tree-shakes unused imports
- Generates source maps (for debugging)

No additional flags required.

## Asset fingerprinting

Currently, alloy does not fingerprint assets (e.g., `home-abc123.js`). All bundles use static names.

For cache busting, configure your reverse proxy or CDN to set appropriate `Cache-Control` headers based on file paths.

## Multiple builds

Run `alloy` multiple times with different `-out` directories for multi-tenant or staged deployments:

```sh
alloy --pages app/pages --out dist/staging
alloy --pages app/pages --out dist/production
```

Then select the appropriate embedded filesystem in your Go code based on environment.

## Next steps

- [Deployment](/10-deployment) - Docker, systemd, cloud platforms
- [Dev workflow](/08-dev-workflow) - ALLOY_DEV mode
- [CLI reference](/14-cli-reference) - Complete flag documentation
