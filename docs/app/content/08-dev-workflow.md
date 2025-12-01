# Dev Workflow

Fast iteration with ALLOY_DEV mode.

## Enable dev mode

```sh
ALLOY_DEV=1 go run .
```

With dev mode enabled:
- Assets rebuild on every request
- Changes to `.tsx` files reflected immediately
- Tailwind CSS recompiles on each request
- No need to run `alloy` CLI manually

## How it works

### Without ALLOY_DEV (production)

1. Run `alloy` CLI to prebuild assets
2. Go binary loads prebuilt bundles from embedded FS
3. Fast serving, no rebuild

### With ALLOY_DEV=1 (development)

1. Skip `alloy` CLI
2. Each request triggers esbuild + Tailwind
3. Bundles generated on-the-fly
4. Changes reflected on browser refresh

## Live reload

Dev mode includes automatic browser refresh.

When you save a `.tsx` or `.css` file, the browser reloads.

**How:** Alloy injects a script that listens to `/__alloy__/live` (SSE endpoint).

## File watching

Alloy doesn't watch files itself. The live reload triggers on request.

**Workflow:**
1. Edit `app/pages/home.tsx`
2. Save file
3. Refresh browser
4. Alloy detects change, rebuilds
5. Browser receives updated page

## Hot module replacement

Not supported. Full page refresh only.

For instant feedback, keep browser DevTools open with "Disable cache" enabled.

## Build errors

Syntax errors in `.tsx` files display in browser:

```
Build error in app/pages/home.tsx:
ERROR: Expected ">" but found "div"
  3 │   return <div<div>Hello</div>;
    │               ^
```

Fix the error, refresh browser, continue.

## Faster iteration

### Use air for auto-restart

Install [air](https://github.com/cosmtrek/air):

```sh
go install github.com/cosmtrek/air@latest
```

Create `.air.toml`:

```toml
[build]
  cmd = "go build -o tmp/main ."
  entrypoint = ["tmp/main"]
  include_ext = ["go"]
  exclude_dir = ["tmp", "node_modules", "app/dist"]

[build.env]
  ALLOY_DEV = "1"
```

Run:

```sh
air
```

Now changes to `.go` files also restart the server automatically.

## Debugging

### Server logs

Alloy logs to stderr:

```
2025/01/15 10:30:45 Building app/pages/home.tsx
2025/01/15 10:30:46 Rendered / in 120ms
```

### React errors

Client-side errors appear in browser console.

Server-side errors (during SSR) appear in terminal.

### Component inspection

Use [React DevTools](https://react.dev/learn/react-developer-tools) to inspect component tree and props.

## Performance

Dev mode is slower than production:
- Each request runs esbuild (~200-500ms)
- Tailwind compiles on every request (~100-300ms)
- No caching

**Acceptable for dev. Use production builds for performance testing.**

## Switching modes

### Dev to production

1. Stop dev server
2. Run `alloy` CLI
3. Run `go run .` (without ALLOY_DEV)

### Production to dev

1. Stop server
2. Run `ALLOY_DEV=1 go run .`

## Troubleshooting

### Changes not reflected

- Hard refresh browser (Cmd+Shift+R or Ctrl+F5)
- Check file path in error message
- Verify `ALLOY_DEV=1` is set

### Slow builds

- Reduce number of pages
- Minimize Tailwind usage
- Check for large dependencies in components

### Live reload not working

- Check browser console for connection errors
- Verify `/__alloy__/live` endpoint responds
- Disable browser extensions blocking SSE

## Next steps

- [Production builds](/09-production-builds) - Optimize for deployment
- [Quick start](/01-quick-start) - Basic setup
- [Error handling](/16-error-handling) - Debug issues
