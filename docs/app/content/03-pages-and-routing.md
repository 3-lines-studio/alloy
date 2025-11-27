# Pages and Routing

Define routes and map them to React components with server-side props.

## Page struct

Every route is defined by an `alloy.Page`:

```go
type Page struct {
	Route     string                               // URL pattern
	Component string                               // Path to .tsx file
	Props     func(*http.Request) map[string]any   // Data loader (optional)
	Ctx       func(*http.Request) context.Context  // Custom context (optional)
}
```

## Static routes

Map exact URLs to components:

```go
pages := []alloy.Page{
	{Route: "/", Component: "app/pages/home.tsx"},
	{Route: "/about", Component: "app/pages/about.tsx"},
	{Route: "/contact", Component: "app/pages/contact.tsx"},
}
```

## Dynamic routes

Use `:name` syntax for path segments:

```go
pages := []alloy.Page{
	{Route: "/blog/:slug", Component: "app/pages/blog.tsx", Props: loader.Blog},
	{Route: "/user/:id", Component: "app/pages/user.tsx", Props: loader.User},
	{Route: "/store/:store/product/:product", Component: "app/pages/product.tsx", Props: loader.Product},
}
```

### Extracting route params

Use `alloy.RouteParams()` in your props function:

```go
// loader/blog.go
package loader

import (
	"net/http"
	"github.com/3-lines-studio/alloy"
)

func Blog(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	slug := params["slug"]

	// Fetch blog post from database
	post := fetchPost(slug)

	return map[string]any{
		"title":   post.Title,
		"content": post.Content,
		"author":  post.Author,
		"meta": map[string]any{
			"title":       post.Title,
			"description": post.Summary,
		},
	}
}
```

### Multiple params

```go
func Product(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	storeSlug := params["store"]
	productSlug := params["product"]

	product := db.FindProduct(storeSlug, productSlug)

	return map[string]any{
		"product": product,
		"store":   storeSlug,
	}
}
```

## Route matching

Routes are matched in order. Place specific routes before generic ones:

```go
pages := []alloy.Page{
	{Route: "/blog/latest", Component: "app/pages/latest.tsx"},      // Specific
	{Route: "/blog/:slug", Component: "app/pages/blog.tsx"},         // Generic
	{Route: "/", Component: "app/pages/home.tsx"},                   // Catch root
}
```

**Order matters.** The first matching route wins.

## Props functions

Props functions receive `*http.Request` and return `map[string]any`:

```go
func MyPage(r *http.Request) map[string]any {
	// Access query params
	search := r.URL.Query().Get("q")

	// Access cookies
	cookie, _ := r.Cookie("session")

	// Access headers
	userAgent := r.Header.Get("User-Agent")

	// Route params
	params := alloy.RouteParams(r)

	return map[string]any{
		"search": search,
		"data":   fetchData(search),
	}
}
```

Props are serialized to JSON and passed to your React component:

```tsx
// app/pages/search.tsx
export default function Search({ search, data }) {
	return (
		<div>
			<h1>Results for: {search}</h1>
			{data.map(item => <p key={item.id}>{item.title}</p>)}
		</div>
	);
}
```

## Meta tags

Set page metadata via the `meta` key in props:

```go
return map[string]any{
	"content": "...",
	"meta": map[string]any{
		"title":       "My Page Title",
		"description": "Page description for SEO",
		"og:image":    "https://example.com/image.png",
		"canonical":   "https://example.com/page",
	},
}
```

Alloy injects these as `<meta>` and `<link>` tags in the document `<head>`.

## Static pages (no props)

Omit the `Props` field for static pages:

```go
{Route: "/about", Component: "app/pages/about.tsx"}
```

Your component receives an empty props object `{}`.

## Component paths

Component paths are relative to your working directory:

```go
{Component: "app/pages/home.tsx"}      // ./app/pages/home.tsx
{Component: "pages/blog/post.tsx"}     // ./pages/blog/post.tsx
```

**Must be `.tsx` files.** Alloy uses esbuild to bundle TypeScript and JSX.

## Context function

Use the `Ctx` field to provide custom `context.Context`:

```go
pages := []alloy.Page{
	{
		Route:     "/admin",
		Component: "app/pages/admin.tsx",
		Props:     loader.Admin,
		Ctx: func(r *http.Request) context.Context {
			// Add auth context
			userID := getUserIDFromSession(r)
			return context.WithValue(r.Context(), "userID", userID)
		},
	},
}
```

Access context in your props function:

```go
func Admin(r *http.Request) map[string]any {
	userID := r.Context().Value("userID").(string)

	if userID == "" {
		// Redirect or return error page data
		return map[string]any{"error": "Unauthorized"}
	}

	return map[string]any{
		"userID": userID,
		"data":   fetchAdminData(userID),
	}
}
```

## Common patterns

### Catch-all / 404

Place a wildcard route last:

```go
pages := []alloy.Page{
	{Route: "/", Component: "app/pages/home.tsx"},
	{Route: "/:path", Component: "app/pages/404.tsx"},  // Catches everything else
}
```

### Nested routes

Alloy doesn't support nested route definitions. Flatten your routes:

```go
// Instead of nesting, define explicitly:
pages := []alloy.Page{
	{Route: "/blog", Component: "app/pages/blog/index.tsx"},
	{Route: "/blog/:slug", Component: "app/pages/blog/post.tsx"},
	{Route: "/blog/category/:cat", Component: "app/pages/blog/category.tsx"},
}
```

### Programmatic redirects

Return redirect data in props and handle in component:

```go
func OldPage(r *http.Request) map[string]any {
	return map[string]any{
		"redirect": "/new-page",
	}
}
```

```tsx
export default function OldPage({ redirect }) {
	if (redirect) {
		if (typeof window !== 'undefined') {
			window.location.href = redirect;
		}
		return <p>Redirecting...</p>;
	}
	return <div>Content</div>;
}
```

Or use HTTP redirects in props (not recommended—props should return data, not side-effect):

Better: use middleware or handle redirects before alloy.Handler.

## Next steps

- [Server rendering](/04-server-rendering) - How SSR works
- [Dynamic data](/07-dynamic-data) - Database queries, API calls
- [Route params](/13-route-params) - Full API reference
- [Page struct](/12-page-struct) - Complete field reference
