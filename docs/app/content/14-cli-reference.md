# CLI Reference

Complete reference for the `alloy` command-line tool.

## Synopsis

```
alloy [OPTIONS]
```

## Description

Build precompiled assets for production deployment:
- Server bundles (QuickJS JavaScript)
- Client bundles (browser JavaScript with hydration)
- Compiled CSS (Tailwind)
- Asset manifest (JSON mapping)

## Options

### -pages

Directory containing `.tsx` page components.

```sh
alloy -pages app/pages
alloy -pages pages
alloy -pages src/views
```

**Default:**
- Tries `app/pages/` first
- Falls back to `pages/`
- Errors if neither exists

### -out

Output directory for built assets.

```sh
alloy -out dist/production
alloy -out build/alloy
alloy -out app/dist/alloy
```

**Default:**
- If `-pages` is `app/pages` → `app/dist/alloy`
- If `-pages` is `pages` → `dist/alloy`
- Otherwise: `<pages-parent>/dist/alloy`

## Examples

### Default build

```sh
alloy
```

Discovers pages in `app/pages/`, outputs to `app/dist/alloy/`.

### Custom paths

```sh
alloy -pages src/pages -out build/assets
```

### CI/CD

```sh
#!/bin/bash
set -e

# Install dependencies
npm install

# Install alloy CLI
go install github.com/3-lines-studio/alloy/cmd/alloy@latest

# Build assets
alloy

# Build Go binary
go build -o app

# Deploy
./deploy.sh
```

## Build process

The CLI performs these steps:

1. **Discover pages**: Find all `.tsx` files in pages directory
2. **Compile CSS**: Run Tailwind on `app/pages/app.css` → `shared.css`
3. **Build client bundles**: Generate hydration JavaScript for browser
4. **Build server bundles**: Generate rendering JavaScript for QuickJS
5. **Write manifest**: Create `manifest.json` mapping pages to assets

## Output files

```
app/dist/alloy/
├── manifest.json              # Asset mappings
├── home-server.js             # Server bundle for home.tsx
├── home-client.js             # Client hydration for home.tsx
├── about-server.js
├── about-client.js
├── blog-server.js
├── blog-client.js
└── shared.css                 # Compiled Tailwind CSS
```

## Manifest structure

```json
{
  "home": {
    "server": "app/dist/alloy/home-server.js",
    "client": "app/dist/alloy/home-client.js",
    "chunks": [],
    "css": "app/dist/alloy/shared.css",
    "sharedCSS": "app/dist/alloy/shared.css"
  }
}
```

## CSS compilation

Requires `app/pages/app.css`:

```css
@import "tailwindcss";
```

**Dependencies:** Tailwind must be installed via npm/pnpm/yarn/bun.

**Detection:** CLI auto-detects your package manager:
- Checks for `pnpm-lock.yaml` → uses `pnpm`
- Checks for `yarn.lock` → uses `yarn`
- Checks for `package-lock.json` → uses `npm`
- Checks for `bun.lockb` → uses `bun`
- Falls back to `npx`

## Error handling

### No pages found

```
no pages found in app/pages
```

**Fix:** Create `.tsx` files in pages directory or specify correct path with `-pages`.

### CSS build failure

```
error building CSS: tailwindcss not found
```

**Fix:** Run `npm install tailwindcss`.

### Component syntax errors

```
build server app/pages/home.tsx: ERROR: Expected ">" but found "div"
```

**Fix:** Check TypeScript/JSX syntax in the component file.

## Environment variables

None. The CLI uses flags only.

(Runtime behavior like `ALLOY_DEV` affects Go server, not CLI.)

## Exit codes

- `0`: Success
- `1`: Build error

## Performance

Typical build times:
- 3 pages: ~1-2 seconds
- 10 pages: ~2-4 seconds
- 50 pages: ~10-15 seconds

Parallel builds not supported. Each page builds sequentially.

## Incremental builds

Not supported. Each run:
1. Deletes output directory
2. Rebuilds all assets from scratch

For fast iteration, use [dev workflow](/08-dev-workflow) with `ALLOY_DEV=1`.

## Glob patterns

Not supported. All `.tsx` files in pages directory are built.

To exclude files, move them outside pages directory:

```
app/
├── pages/          # Built by CLI
│   ├── home.tsx
│   └── blog.tsx
└── components/     # Not built
    └── Header.tsx
```

## Version

```sh
alloy -h
```

Displays usage. No `-version` flag (yet).

## Next steps

- [Production builds](/09-production-builds) - Build workflow guide
- [Deployment](/10-deployment) - Deploy prebuilt assets
- [Dev workflow](/08-dev-workflow) - Hot reload with ALLOY_DEV
