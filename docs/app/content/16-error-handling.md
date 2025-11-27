# Error Handling

Handle errors in alloy applications.

## Props function errors

### Return error data

```go
func Blog(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	slug := params["slug"]

	post, err := fetchBlogPost(slug)
	if err != nil {
		log.Printf("fetch error: %v", err)
		return map[string]any{
			"error": "Failed to load blog post",
			"slug":  slug,
		}
	}

	return map[string]any{"post": post}
}
```

Component handles error:

```tsx
export default function Blog({ post, error, slug }) {
	if (error) {
		return (
			<div>
				<h1>Error</h1>
				<p>{error}</p>
			</div>
		);
	}

	return <article>{post.content}</article>;
}
```

### Panic recovery

```go
func SafeLoader(r *http.Request) (result map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("props panic: %v", r)
			result = map[string]any{
				"error": "Internal error",
			}
		}
	}()

	data := riskyOperation()  // May panic

	return map[string]any{"data": data}
}
```

## 404 handling

### Not found props

```go
func Product(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	id := params["id"]

	product, found := findProduct(id)
	if !found {
		return map[string]any{
			"notFound": true,
			"id":       id,
		}
	}

	return map[string]any{
		"product": product,
	}
}
```

```tsx
export default function Product({ product, notFound, id }) {
	if (notFound) {
		return (
			<div>
				<h1>404 - Product Not Found</h1>
				<p>No product with ID: {id}</p>
			</div>
		);
	}

	return <div>{product.name}</div>;
}
```

### Catch-all route

```go
pages := []alloy.Page{
	{Route: "/", Component: "app/pages/home.tsx"},
	{Route: "/about", Component: "app/pages/about.tsx"},
	{Route: "/:path", Component: "app/pages/404.tsx"},  // Catches all
}
```

## Database errors

### Query errors

```go
func Users(r *http.Request) map[string]any {
	rows, err := db.Query("SELECT id, name FROM users")
	if err != nil {
		log.Printf("DB error: %v", err)
		return map[string]any{
			"error": "Database unavailable",
			"users": []any{},  // Empty array fallback
		}
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			log.Printf("scan error: %v", err)
			continue  // Skip malformed rows
		}
		users = append(users, u)
	}

	return map[string]any{"users": users}
}
```

### Connection errors

```go
var db *sql.DB

func init() {
	var err error
	db, err = sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	if err := db.Ping(); err != nil {
		log.Fatal("Database unreachable:", err)
	}
}
```

## API errors

### HTTP client errors

```go
func Weather(r *http.Request) map[string]any {
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get("https://api.weather.com/data")
	if err != nil {
		log.Printf("API error: %v", err)
		return map[string]any{
			"error": "Weather service unavailable",
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return map[string]any{
			"error": fmt.Sprintf("API returned %d", resp.StatusCode),
		}
	}

	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return map[string]any{
			"error": "Invalid API response",
		}
	}

	return map[string]any{"weather": data}
}
```

## QuickJS runtime errors

### Component syntax errors

Dev mode shows build errors in browser.

Production: ensure `alloy` CLI succeeds before deploying.

### Runtime execution errors

If component throws during render:

```tsx
export default function Broken() {
	throw new Error("Something broke");
	return <div>Never reached</div>;
}
```

Alloy returns HTTP 500. Check server logs for stack trace.

**Fix:** Validate props and handle edge cases in component.

## Validation errors

### Input validation

```go
func CreateUser(r *http.Request) map[string]any {
	email := r.FormValue("email")

	if !isValidEmail(email) {
		return map[string]any{
			"error":      "Invalid email format",
			"fieldError": "email",
		}
	}

	user := createUser(email)
	return map[string]any{
		"user":    user,
		"success": true,
	}
}
```

```tsx
export default function CreateUser({ error, fieldError, success, user }) {
	if (success) {
		return <p>User created: {user.email}</p>;
	}

	return (
		<form>
			<input name="email" />
			{fieldError === 'email' && <span className="text-red-500">{error}</span>}
			<button>Submit</button>
		</form>
	);
}
```

## Logging

### Structured logging

```go
import "log/slog"

func Blog(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	slug := params["slug"]

	post, err := fetchPost(slug)
	if err != nil {
		slog.Error("Failed to fetch post",
			"slug", slug,
			"error", err,
			"path", r.URL.Path,
		)
		return map[string]any{"error": "Post not found"}
	}

	slog.Info("Post loaded",
		"slug", slug,
		"title", post.Title,
	)

	return map[string]any{"post": post}
}
```

## Error pages

### Custom error component

```tsx
// app/pages/error.tsx
export default function ErrorPage({ message, code }) {
	return (
		<div className="min-h-screen flex items-center justify-center">
			<div>
				<h1 className="text-6xl font-bold">{code || 500}</h1>
				<p className="text-xl mt-4">{message || "Something went wrong"}</p>
				<a href="/" className="mt-6 text-blue-500">Go home</a>
			</div>
		</div>
	);
}
```

Use in props:

```go
func Risky(r *http.Request) map[string]any {
	data, err := fetchData()
	if err != nil {
		return map[string]any{
			"message": "Failed to load data",
			"code":    500,
		}
	}

	return map[string]any{"data": data}
}
```

## Next steps

- [Authentication](/15-authentication) - Handle auth errors
- [Testing](/18-testing) - Test error scenarios
- [Troubleshooting](/21-troubleshooting) - Common issues
