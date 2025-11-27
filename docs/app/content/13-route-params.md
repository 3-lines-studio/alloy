# Route Params

Extract dynamic route segments with `alloy.RouteParams()`.

## Function signature

```go
func RouteParams(r *http.Request) map[string]string
```

### Parameters

**r** (`*http.Request`): The HTTP request

### Returns

`map[string]string`: Route parameters as key-value pairs

## Usage

Extract params in props functions:

```go
import "github.com/3-lines-studio/alloy"

func Blog(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	slug := params["slug"]

	post := fetchBlogPost(slug)

	return map[string]any{
		"post": post,
		"slug": slug,
	}
}
```

## Route patterns

### Single parameter

**Route:** `/blog/:slug`

**Request:** `/blog/hello-world`

**Params:**
```go
params := alloy.RouteParams(r)
// params["slug"] == "hello-world"
```

### Multiple parameters

**Route:** `/store/:store/product/:product`

**Request:** `/store/electronics/product/laptop`

**Params:**
```go
params := alloy.RouteParams(r)
// params["store"] == "electronics"
// params["product"] == "laptop"
```

### Numeric IDs

**Route:** `/user/:id`

**Request:** `/user/123`

**Params:**
```go
params := alloy.RouteParams(r)
id := params["id"]  // "123" (string)

// Convert to int
userID, err := strconv.Atoi(id)
if err != nil {
	return map[string]any{"error": "Invalid user ID"}
}

user := fetchUser(userID)
```

## Missing parameters

Static routes have no params:

```go
// Route: "/about"
params := alloy.RouteParams(r)
// params is empty map: map[string]string{}
```

Accessing non-existent keys returns empty string:

```go
slug := params["slug"]  // "" if not defined
```

## Safe access

Check before using:

```go
params := alloy.RouteParams(r)
slug, ok := params["slug"]
if !ok || slug == "" {
	return map[string]any{
		"error": "Slug required",
	}
}

post := fetchPost(slug)
return map[string]any{"post": post}
```

## Validation

Validate params in props function:

```go
func Product(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	productID := params["id"]

	// Validate format
	if !isValidProductID(productID) {
		return map[string]any{
			"error": "Invalid product ID",
			"id":    productID,
		}
	}

	product := fetchProduct(productID)
	if product == nil {
		return map[string]any{
			"error":    "Product not found",
			"id":       productID,
			"notFound": true,
		}
	}

	return map[string]any{
		"product": product,
	}
}
```

## Combined with query params

Use both route and query parameters:

```go
// Route: /search/:category
// Request: /search/books?q=golang&page=2

func Search(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	category := params["category"]

	query := r.URL.Query()
	searchQuery := query.Get("q")
	page := query.Get("page")

	results := performSearch(category, searchQuery, page)

	return map[string]any{
		"category": category,
		"query":    searchQuery,
		"results":  results,
	}
}
```

## Database queries

Use params in SQL safely:

```go
func User(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	userID := params["id"]

	var name, email string
	err := db.QueryRow(
		"SELECT name, email FROM users WHERE id = $1",
		userID,
	).Scan(&name, &email)

	if err == sql.ErrNoRows {
		return map[string]any{
			"error":    "User not found",
			"notFound": true,
		}
	}

	if err != nil {
		log.Printf("DB error: %v", err)
		return map[string]any{
			"error": "Database error",
		}
	}

	return map[string]any{
		"user": map[string]string{
			"id":    userID,
			"name":  name,
			"email": email,
		},
	}
}
```

**Always use parameterized queries** to prevent SQL injection.

## Slugs vs IDs

### URL-friendly slugs

```go
// Route: /blog/:slug
// Request: /blog/react-ssr-with-go

params := alloy.RouteParams(r)
slug := params["slug"]

// Fetch by slug
post := db.QueryRow("SELECT * FROM posts WHERE slug = $1", slug)
```

### Numeric IDs

```go
// Route: /api/posts/:id
// Request: /api/posts/42

params := alloy.RouteParams(r)
idStr := params["id"]

id, err := strconv.ParseInt(idStr, 10, 64)
if err != nil {
	return map[string]any{"error": "Invalid ID"}
}

post := fetchPostByID(id)
```

### Composite keys

```go
// Route: /docs/:version/:page
// Request: /docs/v2/quickstart

params := alloy.RouteParams(r)
version := params["version"]  // "v2"
page := params["page"]        // "quickstart"

content := fetchDoc(version, page)
```

## Type conversions

```go
import "strconv"

params := alloy.RouteParams(r)

// String to int
id, _ := strconv.Atoi(params["id"])

// String to int64
userID, _ := strconv.ParseInt(params["userId"], 10, 64)

// String to float
price, _ := strconv.ParseFloat(params["price"], 64)

// String to bool
enabled := params["enabled"] == "true"
```

## Next steps

- [Pages and routing](/03-pages-and-routing) - Route definition
- [Page struct](/12-page-struct) - Complete Page reference
- [Dynamic data](/07-dynamic-data) - Data fetching patterns
