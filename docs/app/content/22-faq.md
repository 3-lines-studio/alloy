# FAQ

Common questions about alloy.

## General

### What is alloy?

Alloy is a Go library for server-side rendering React components. It uses QuickJS to render React on the server, esbuild for bundling, and supports optional client-side hydration.

### Why use alloy over Next.js?

- **No Node.js in production:** Single Go binary deployment
- **Simpler infrastructure:** No need for Node runtime, PM2, or serverless functions
- **Go integration:** Direct access to Go's ecosystem (SQL, gRPC, etc.)
- **Optional hydration:** Ship less JavaScript by default

### Why use alloy over templ or Go templates?

- **React ecosystem:** Use existing React components and libraries
- **TypeScript support:** Type-safe component development
- **Client-side interactivity:** Hydration for dynamic UIs
- **Modern tooling:** esbuild, Tailwind CSS built-in

## Technical

### Does alloy require Node.js?

**Development:** Yes, for Tailwind CSS compilation.

**Production:** No. The compiled binary includes all assets. No Node.js runtime needed.

### What React version does alloy use?

React 18. Specified in your `package.json`:

```json
{
  "dependencies": {
    "react": "^18.3.0",
    "react-dom": "^18.3.0"
  }
}
```

### Can I use React libraries?

Yes, but with limitations:

- **Server-safe libraries:** Use any library without browser APIs
- **Client-only libraries:** Only in components with hooks (hydrated)
- **Browser APIs:** Not available during SSR (window, document, localStorage)

Example:

```tsx
import { useState, useEffect } from 'react';
import Chart from 'chart.js';  // Browser-only

export default function Dashboard({ data }) {
	const [chartReady, setChartReady] = useState(false);

	useEffect(() => {
		// Safe: runs only on client
		new Chart(ctx, config);
		setChartReady(true);
	}, []);

	return <canvas id="chart" />;
}
```

### What's the performance like?

**Server:** QuickJS renders faster than Node.js for simple components. Complex components may be slower than V8.

**Client:** Same as standard React—esbuild generates optimized bundles.

**Network:** Faster initial page load than client-side React (HTML sent immediately).

### Can I use CSS-in-JS?

Not recommended. Server bundles run in QuickJS, which may not support all CSS-in-JS libraries.

**Use Tailwind instead.** It's compiled at build time, works everywhere.

### Does alloy support TypeScript?

Yes. Components are `.tsx` files. esbuild handles TypeScript compilation.

**No `tsconfig.json` required** for builds (esbuild ignores it). Add `tsconfig.json` for IDE autocomplete only.

## Features

### Does alloy support streaming SSR?

No. Alloy renders to a complete HTML string before sending the response.

### Can I deploy to serverless platforms?

Yes. Alloy works on:
- AWS Lambda (with Lambda Web Adapter)
- Google Cloud Run
- Fly.io
- Railway
- Render

See [Deployment](/10-deployment) for examples.

### Does alloy support static site generation (SSG)?

Not built-in. Alloy is designed for dynamic SSR.

**Workaround:** Pre-render pages with a script and serve static HTML.

### Can I use alloy for APIs?

Alloy is for rendering pages. For JSON APIs, use standard Go `http.HandlerFunc`:

```go
mux := http.NewServeMux()

// Alloy pages
handler, _ := alloy.Handler(dist, pages)
mux.Handle("/", handler)

// JSON API
mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(users)
})
```

### Does alloy support i18n?

No built-in support. Implement in props functions:

```go
func Home(r *http.Request) map[string]any {
	lang := r.Header.Get("Accept-Language")
	translations := loadTranslations(lang)

	return map[string]any{
		"translations": translations,
	}
}
```

## Troubleshooting

### Why is my page blank?

Check:
1. Component exports default function
2. Component returns valid JSX
3. Props function returns `map[string]any`
4. Browser console for JavaScript errors
5. Server logs for build errors

### Why aren't my styles loading?

Check:
1. `app/pages/app.css` exists
2. Tailwind installed (`npm install tailwindcss`)
3. `alloy` CLI ran successfully
4. `shared.css` exists in `app/dist/alloy/`

### Why isn't hydration working?

Check:
1. Component uses hooks (useState, useEffect, etc.)
2. Client bundle loaded (check Network tab)
3. `window.__ALLOY_PROPS__` defined
4. No hydration mismatch errors in console

### Build is slow in dev mode

Dev mode rebuilds on every request. Normal behavior.

**Speed up:**
- Use fewer pages
- Reduce component complexity
- For production, run `alloy` CLI once

## Comparison

### Alloy vs Next.js

| Feature | Alloy | Next.js |
|---------|-------|---------|
| Runtime | Go + QuickJS | Node.js |
| Deployment | Single binary | Node server or serverless |
| Hydration | Optional | All pages |
| Backend integration | Go native | API routes or separate backend |
| Ecosystem | React + Go | React + JavaScript |

### Alloy vs Remix

Similar goals (SSR, data loading), different ecosystems.

**Choose Remix** if you want full JavaScript stack.

**Choose Alloy** if you want Go backend with React frontend.

### Alloy vs Astro

Both support islands/partial hydration.

**Astro:** Multi-framework (React, Vue, Svelte), static-first.

**Alloy:** React-only, dynamic SSR-first, Go backend.

## Next steps

- [Quick start](/01-quick-start) - Get started
- [Troubleshooting](/21-troubleshooting) - Solve issues
- [Deployment](/10-deployment) - Go to production
