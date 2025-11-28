# Server Rendering

How alloy renders React components on the server using QuickJS.

## Overview

Server-side rendering (SSR) flow:

1. HTTP request arrives
2. Route matched to a `Page`
3. Props function called with `*http.Request`
4. Server bundle loaded into QuickJS runtime
5. React component executed with props
6. HTML string returned
7. Document assembled with meta tags, CSS, hydration script
8. Response sent to client

## QuickJS runtime

Alloy uses [QuickJS](https://bellard.org/quickjs/), a lightweight JavaScript engine written in C.

**Why QuickJS?**
- Embedded in Go binary (no external Node.js process)
- Fast startup time
- Small memory footprint
- Supports ES2020 syntax

**Not V8/Node.js.** Some Node.js APIs unavailable. Alloy provides polyfills for common globals (console, TextEncoder, etc.).

## Rendering pipeline

### 1. Request routing

```go
pages := []alloy.Page{
	{Route: "/", Component: "app/pages/home.tsx", Props: loader.Home},
}
handler, _ := alloy.Handler(dist, pages)
```

When `/` is requested, alloy:
- Finds matching Page
- Calls `loader.Home(r)` to get props

### 2. Props serialization

Props are serialized to JSON:

```go
func Home(r *http.Request) map[string]any {
	return map[string]any{
		"title": "Hello",
		"count": 42,
		"items": []string{"a", "b"},
	}
}
```

Becomes:

```json
{
  "title": "Hello",
  "count": 42,
  "items": ["a", "b"]
}
```

Passed to React component as props.

### 3. Server bundle execution

In dev mode (`ALLOY_DEV=1`), alloy builds `home-server.js` on-the-fly.

In production, alloy loads prebuilt `app/dist/alloy/home-server.js` from embedded filesystem.

Server bundle contains:
- Your React component
- react and react-dom/server.edge
- Dependencies imported by your component
- Polyfills for QuickJS environment

### 4. React rendering

QuickJS executes:

```js
import { renderToString } from 'react-dom/server.edge';
import Component from './home';

const props = JSON.parse(propsJSON);
const html = renderToString(<Component {...props} />);
```

Returns HTML string like:

```html
<div class="p-8"><h1>Hello</h1></div>
```

### 5. Document assembly

Alloy wraps HTML in full document:

```html
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Hello</title>
  <link rel="stylesheet" href="/app/dist/alloy/shared.css">
</head>
<body>
  <div id="home-root"><div class="p-8"><h1>Hello</h1></div></div>
  <script src="/app/dist/alloy/home-client.js"></script>
</body>
</html>
```

## Meta tags

Control `<head>` content via `meta` prop:

```go
return map[string]any{
	"title": "Welcome",
	"meta": map[string]any{
		"title":       "My Page Title",
		"description": "SEO description",
		"og:image":    "https://example.com/image.png",
		"og:type":     "website",
		"canonical":   "https://example.com/page",
	},
}
```

Generated:

```html
<head>
  <title>My Page Title</title>
  <meta name="description" content="SEO description">
  <meta property="og:image" content="https://example.com/image.png">
  <meta property="og:type" content="website">
  <link rel="canonical" href="https://example.com/page">
</head>
```

**Keys starting with `og:` or `twitter:`** become `<meta property="...">`.

**Other keys** become `<meta name="...">`.

**Special key `canonical`** becomes `<link rel="canonical">`.

## Development vs production

### Development (`ALLOY_DEV=1`)

- Server bundles built on every request
- Changes to `.tsx` files reflected immediately
- Slower (esbuild runs per-request)

### Production

- Prebuilt bundles loaded from embedded FS
- Bundles cached in memory
- Fast (no build step)

## Polyfills

QuickJS doesn't include Node.js or browser APIs. Alloy provides:

| API | Support |
|-----|---------|
| `console.log/error/warn` | ✓ Forwarded to Go logger |
| `TextEncoder/TextDecoder` | ✓ Polyfilled |
| `performance.now()` | ✓ Polyfilled |
| `setTimeout/setInterval` | ✗ Not available |
| `fetch` | ✗ Not available (use props for data) |
| `localStorage` | ✗ Not available (server-side) |

**Server bundles run once per request.** No persistent state or timers.

## Error handling

### Build errors

If server bundle fails to compile (TypeScript/JSX errors), alloy returns HTTP 500 with error details in dev mode.

In production, ensure `alloy` CLI succeeded before deploying.

### Runtime errors

If QuickJS execution throws (e.g., undefined variable), alloy returns HTTP 500.

Check logs for stack traces.

### Props errors

If props function panics, request fails. Recover in your props function:

```go
func Home(r *http.Request) map[string]any {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("props panic: %v", r)
		}
	}()

	data := fetchData()  // May panic

	return map[string]any{"data": data}
}
```

## Performance considerations

### Render timeout

QuickJS execution has a timeout (default: 5 seconds). Long-running components timeout.

**Fix:** Move heavy computation to props function (Go is faster than JS).

### Memory usage

Each request creates a QuickJS context. Large components or deep object trees consume memory.

**Tip:** Keep props minimal. Don't pass entire database tables—select only needed fields.

### Concurrency

Alloy uses a runtime pool for concurrent requests. Default pool size: number of CPU cores.

Multiple requests render in parallel. No locking or state sharing between requests.

## Static HTML (no props)

Pages without props still render server-side:

```go
{Route: "/about", Component: "app/pages/about.tsx"}
```

Component receives `{}` (empty props object).

## Common patterns

### Conditional rendering

```tsx
export default function Page({ user, isLoggedIn }) {
	if (!isLoggedIn) {
		return <div>Please log in</div>;
	}

	return <div>Welcome, {user.name}</div>;
}
```

Props function determines login state:

```go
func Page(r *http.Request) map[string]any {
	user, loggedIn := getUser(r)
	return map[string]any{
		"user":       user,
		"isLoggedIn": loggedIn,
	}
}
```

### Markdown rendering

Use goldmark in props, not React:

```go
import "github.com/yuin/goldmark"

func Blog(r *http.Request) map[string]any {
	post := fetchPost(...)
	var buf bytes.Buffer
	md := goldmark.New()
	md.Convert([]byte(post.Markdown), &buf)

	return map[string]any{
		"html": buf.String(),
	}
}
```

```tsx
export default function Blog({ html }) {
	return <div dangerouslySetInnerHTML={{__html: html}} />;
}
```

**Why?** Go markdown parsing is faster than JS.

### Database queries

Fetch data in props, not component:

```go
func Products(r *http.Request) map[string]any {
	rows, _ := db.Query("SELECT id, name, price FROM products")
	defer rows.Close()

	var products []Product
	for rows.Next() {
		var p Product
		rows.Scan(&p.ID, &p.Name, &p.Price)
		products = append(products, p)
	}

	return map[string]any{"products": products}
}
```

```tsx
export default function Products({ products }) {
	return (
		<ul>
			{products.map(p => <li key={p.id}>{p.name}: ${p.price}</li>)}
		</ul>
	);
}
```

**Components are pure presentation.** All I/O happens in Go.

## Next steps

- [Client hydration](/05-client-hydration) - Add interactivity
- [Dynamic data](/07-dynamic-data) - Data fetching patterns
- [Performance](/17-performance) - Optimization techniques
