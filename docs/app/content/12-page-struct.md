# Page Struct

Complete reference for `alloy.Page` configuration.

## Definition

```go
type Page struct {
	Route     string
	Component string
	Props     func(*http.Request) map[string]any
	Ctx       func(*http.Request) context.Context
}
```

## Fields

### Route

**Type:** `string` (required)

URL pattern to match. Supports static routes and dynamic segments.

**Static:**
```go
Route: "/"
Route: "/about"
Route: "/blog/latest"
```

**Dynamic:**
```go
Route: "/blog/:slug"
Route: "/user/:id"
Route: "/store/:store/product/:product"
```

**Matching:**
- First matching route wins
- Place specific routes before generic ones
- Dynamic segments match any non-slash characters

**Examples:**

| Route | Matches | Params |
|-------|---------|--------|
| `/` | `/` only | none |
| `/about` | `/about` only | none |
| `/:slug` | `/anything` | `slug=anything` |
| `/blog/:id` | `/blog/123` | `id=123` |
| `/a/:b/c/:d` | `/a/X/c/Y` | `b=X, d=Y` |

### Component

**Type:** `string` (required)

Path to `.tsx` file, relative to working directory.

```go
Component: "app/pages/home.tsx"
Component: "pages/blog.tsx"
Component: "src/views/product.tsx"
```

**Requirements:**
- Must be `.tsx` extension
- Must export default function component
- File must exist at build time

**Example component:**
```tsx
// app/pages/home.tsx
export default function Home({ title }) {
	return <h1>{title}</h1>;
}
```

### Props

**Type:** `func(*http.Request) map[string]any` (optional)

Function called on each request to generate component props.

**Signature:**
```go
func(r *http.Request) map[string]any
```

**Example:**
```go
func Home(r *http.Request) map[string]any {
	return map[string]any{
		"title": "Welcome",
		"count": 42,
		"meta": map[string]any{
			"title": "Home Page",
		},
	}
}
```

**Access request data:**
```go
func Blog(r *http.Request) map[string]any {
	// Route params
	params := alloy.RouteParams(r)
	slug := params["slug"]

	// Query params
	page := r.URL.Query().Get("page")

	// Headers
	userAgent := r.Header.Get("User-Agent")

	// Cookies
	cookie, _ := r.Cookie("session")

	return map[string]any{
		"slug": slug,
		"page": page,
	}
}
```

**If omitted:** Component receives `{}`.

### Ctx

**Type:** `func(*http.Request) context.Context` (optional)

Function to provide custom context for the request.

**Signature:**
```go
func(r *http.Request) context.Context
```

**Example:**
```go
func withAuth(r *http.Request) context.Context {
	userID := getUserFromCookie(r)
	return context.WithValue(r.Context(), "userID", userID)
}

pages := []alloy.Page{
	{
		Route:     "/dashboard",
		Component: "app/pages/dashboard.tsx",
		Props:     loader.Dashboard,
		Ctx:       withAuth,
	},
}
```

**Use in Props:**
```go
func Dashboard(r *http.Request) map[string]any {
	userID := r.Context().Value("userID").(string)

	if userID == "" {
		return map[string]any{"error": "Not authenticated"}
	}

	data := fetchUserData(userID)
	return map[string]any{"data": data}
}
```

**If omitted:** Uses request's default context.

## Complete example

```go
package main

import (
	"context"
	"embed"
	"log"
	"net/http"

	"github.com/3-lines-studio/alloy"
)

//go:embed app/dist/alloy/* public/*
var dist embed.FS

func main() {
	pages := []alloy.Page{
		{
			Route:     "/",
			Component: "app/pages/home.tsx",
			Props:     homeProps,
		},
		{
			Route:     "/blog/:slug",
			Component: "app/pages/blog.tsx",
			Props:     blogProps,
		},
		{
			Route:     "/admin",
			Component: "app/pages/admin.tsx",
			Props:     adminProps,
			Ctx:       adminContext,
		},
	}

	handler, err := alloy.Handler(dist, pages)
	if err != nil {
		log.Fatal(err)
	}

	http.ListenAndServe(":8080", handler)
}

func homeProps(r *http.Request) map[string]any {
	return map[string]any{
		"title": "Home",
		"meta": map[string]any{
			"title": "My Site",
		},
	}
}

func blogProps(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	post := fetchPost(params["slug"])

	return map[string]any{
		"post": post,
		"meta": map[string]any{
			"title": post.Title,
		},
	}
}

func adminProps(r *http.Request) map[string]any {
	userID := r.Context().Value("userID").(string)
	data := fetchAdminData(userID)

	return map[string]any{"data": data}
}

func adminContext(r *http.Request) context.Context {
	userID := getAuthUser(r)
	return context.WithValue(r.Context(), "userID", userID)
}
```

## Common patterns

### Redirect logic

```go
{
	Route: "/old-path",
	Component: "app/pages/redirect.tsx",
	Props: func(r *http.Request) map[string]any {
		return map[string]any{
			"redirect": "/new-path",
		}
	},
}
```

### Error pages

```go
{
	Route: "/:path",
	Component: "app/pages/404.tsx",
	Props: func(r *http.Request) map[string]any {
		return map[string]any{
			"path": r.URL.Path,
		}
	},
}
```

### Conditional rendering

```go
func Dashboard(r *http.Request) map[string]any {
	user, authenticated := getUser(r)

	if !authenticated {
		return map[string]any{
			"authenticated": false,
		}
	}

	return map[string]any{
		"authenticated": true,
		"user":          user,
	}
}
```

## Next steps

- [Pages and routing](/03-pages-and-routing) - Routing guide
- [Route params](/13-route-params) - Extract route parameters
- [Handler](/11-handler) - Handler API reference
