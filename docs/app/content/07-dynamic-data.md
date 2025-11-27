# Dynamic Data

Fetch data from databases, APIs, and other sources in props loaders.

## Props loaders

Props functions run on every request, allowing dynamic data:

```go
func Blog(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	slug := params["slug"]

	post := fetchBlogPost(slug)

	return map[string]any{
		"title":   post.Title,
		"content": post.Content,
		"author":  post.Author,
	}
}
```

## Database queries

### PostgreSQL

```go
import (
	"database/sql"
	_ "github.com/lib/pq"
)

var db *sql.DB

func init() {
	var err error
	db, err = sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
}

func Products(r *http.Request) map[string]any {
	rows, err := db.Query("SELECT id, name, price FROM products ORDER BY name")
	if err != nil {
		log.Printf("query error: %v", err)
		return map[string]any{"error": "Database error"}
	}
	defer rows.Close()

	type Product struct {
		ID    int     `json:"id"`
		Name  string  `json:"name"`
		Price float64 `json:"price"`
	}

	var products []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Price); err != nil {
			log.Printf("scan error: %v", err)
			continue
		}
		products = append(products, p)
	}

	return map[string]any{
		"products": products,
		"meta": map[string]any{
			"title": "Products",
		},
	}
}
```

### SQLite

```go
import (
	"database/sql"
	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB

func init() {
	var err error
	db, err = sql.Open("sqlite3", "./data.db")
	if err != nil {
		log.Fatal(err)
	}
}

func Posts(r *http.Request) map[string]any {
	rows, err := db.Query("SELECT id, title, body FROM posts")
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer rows.Close()

	var posts []map[string]any
	for rows.Next() {
		var id int
		var title, body string
		rows.Scan(&id, &title, &body)
		posts = append(posts, map[string]any{
			"id":    id,
			"title": title,
			"body":  body,
		})
	}

	return map[string]any{"posts": posts}
}
```

### Prepared statements

For parameterized queries:

```go
func User(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	userID := params["id"]

	var name, email string
	err := db.QueryRow("SELECT name, email FROM users WHERE id = $1", userID).Scan(&name, &email)
	if err == sql.ErrNoRows {
		return map[string]any{"error": "User not found"}
	}
	if err != nil {
		log.Printf("query error: %v", err)
		return map[string]any{"error": "Database error"}
	}

	return map[string]any{
		"name":  name,
		"email": email,
		"id":    userID,
	}
}
```

## HTTP API calls

### Fetch external API

```go
import (
	"encoding/json"
	"net/http"
	"time"
)

func Weather(r *http.Request) map[string]any {
	city := r.URL.Query().Get("city")
	if city == "" {
		city = "London"
	}

	apiURL := "https://api.example.com/weather?city=" + city
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(apiURL)
	if err != nil {
		log.Printf("API error: %v", err)
		return map[string]any{"error": "Weather service unavailable"}
	}
	defer resp.Body.Close()

	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return map[string]any{"error": "Invalid response"}
	}

	return map[string]any{
		"weather": data,
		"city":    city,
	}
}
```

### POST requests

```go
func CreateOrder(r *http.Request) map[string]any {
	orderData := map[string]any{
		"product": "Widget",
		"quantity": 5,
	}

	body, _ := json.Marshal(orderData)

	req, _ := http.NewRequest("POST", "https://api.example.com/orders", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("API_KEY"))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	return map[string]any{"order": result}
}
```

## Context usage

Share database connections or clients via context:

```go
type contextKey string

const dbKey contextKey = "db"

func withDB(r *http.Request) context.Context {
	return context.WithValue(r.Context(), dbKey, db)
}

pages := []alloy.Page{
	{
		Route:     "/products",
		Component: "app/pages/products.tsx",
		Props:     Products,
		Ctx:       withDB,
	},
}

func Products(r *http.Request) map[string]any {
	db := r.Context().Value(dbKey).(*sql.DB)

	rows, _ := db.QueryContext(r.Context(), "SELECT * FROM products")
	defer rows.Close()

	// ... process rows
}
```

## Request data

### Query parameters

```go
func Search(r *http.Request) map[string]any {
	query := r.URL.Query()
	q := query.Get("q")
	page := query.Get("page")
	if page == "" {
		page = "1"
	}

	results := performSearch(q, page)

	return map[string]any{
		"query":   q,
		"results": results,
		"page":    page,
	}
}
```

### Cookies

```go
func Dashboard(r *http.Request) map[string]any {
	cookie, err := r.Cookie("session_id")
	if err != nil {
		return map[string]any{"error": "Not logged in"}
	}

	user := getUserBySession(cookie.Value)

	return map[string]any{
		"user": user,
		"meta": map[string]any{
			"title": "Dashboard - " + user.Name,
		},
	}
}
```

### Headers

```go
func API(r *http.Request) map[string]any {
	userAgent := r.Header.Get("User-Agent")
	acceptLang := r.Header.Get("Accept-Language")

	return map[string]any{
		"userAgent": userAgent,
		"language":  acceptLang,
	}
}
```

### POST body

Alloy is GET-focused (SSR). For forms, create a separate handler:

```go
func main() {
	mux := http.NewServeMux()

	// Alloy pages
	handler, _ := alloy.Handler(dist, pages)
	mux.Handle("/", handler)

	// Form submission
	mux.HandleFunc("/api/submit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", 405)
			return
		}

		var data map[string]string
		json.NewDecoder(r.Body).Decode(&data)

		// Process form
		saveToDatabase(data)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	http.ListenAndServe(":8080", mux)
}
```

Client-side form submission:

```tsx
const handleSubmit = async (e) => {
	e.preventDefault();
	const res = await fetch('/api/submit', {
		method: 'POST',
		headers: {'Content-Type': 'application/json'},
		body: JSON.stringify({name, email}),
	});
	const data = await res.json();
	console.log(data);
};
```

## Error handling

### Graceful degradation

```go
func Products(r *http.Request) map[string]any {
	rows, err := db.Query("SELECT * FROM products")
	if err != nil {
		log.Printf("DB error: %v", err)
		return map[string]any{
			"products": []any{},  // Empty array
			"error":    "Could not load products",
		}
	}
	defer rows.Close()

	// ... process rows

	return map[string]any{"products": products}
}
```

Component handles error:

```tsx
export default function Products({ products, error }) {
	if (error) {
		return <p className="text-red-500">{error}</p>;
	}

	return (
		<ul>
			{products.map(p => <li key={p.id}>{p.name}</li>)}
		</ul>
	);
}
```

### 404 pages

```go
func BlogPost(r *http.Request) map[string]any {
	params := alloy.RouteParams(r)
	slug := params["slug"]

	post, found := fetchPost(slug)
	if !found {
		return map[string]any{
			"notFound": true,
			"slug":     slug,
		}
	}

	return map[string]any{
		"post": post,
	}
}
```

```tsx
export default function BlogPost({ post, notFound, slug }) {
	if (notFound) {
		return (
			<div>
				<h1>Post not found</h1>
				<p>No post with slug: {slug}</p>
			</div>
		);
	}

	return (
		<article>
			<h1>{post.title}</h1>
			<div>{post.content}</div>
		</article>
	);
}
```

## Caching

### In-memory cache

```go
import "sync"

var cache = struct {
	sync.RWMutex
	data map[string]any
}{data: make(map[string]any)}

func CachedData(r *http.Request) map[string]any {
	cache.RLock()
	if val, ok := cache.data["products"]; ok {
		cache.RUnlock()
		return val.(map[string]any)
	}
	cache.RUnlock()

	// Fetch fresh data
	products := fetchProducts()
	result := map[string]any{"products": products}

	cache.Lock()
	cache.data["products"] = result
	cache.Unlock()

	return result
}
```

### Time-based invalidation

```go
import "time"

type cacheEntry struct {
	data      any
	expiresAt time.Time
}

var cache = struct {
	sync.RWMutex
	entries map[string]cacheEntry
}{entries: make(map[string]cacheEntry)}

func getCached(key string, ttl time.Duration, fetch func() any) any {
	cache.RLock()
	entry, ok := cache.entries[key]
	cache.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.data
	}

	data := fetch()

	cache.Lock()
	cache.entries[key] = cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(ttl),
	}
	cache.Unlock()

	return data
}

func Products(r *http.Request) map[string]any {
	products := getCached("products", 5*time.Minute, func() any {
		return fetchProducts()
	})

	return map[string]any{"products": products}
}
```

### External cache (Redis)

```go
import "github.com/redis/go-redis/v9"

var rdb = redis.NewClient(&redis.Options{
	Addr: "localhost:6379",
})

func Products(r *http.Request) map[string]any {
	ctx := r.Context()
	cached, err := rdb.Get(ctx, "products").Result()
	if err == nil {
		var products []Product
		json.Unmarshal([]byte(cached), &products)
		return map[string]any{"products": products}
	}

	products := fetchProducts()
	data, _ := json.Marshal(products)
	rdb.Set(ctx, "products", data, 10*time.Minute)

	return map[string]any{"products": products}
}
```

## Next steps

- [Pages and routing](/03-pages-and-routing) - Route params
- [Authentication](/15-authentication) - Session and auth patterns
- [Performance](/17-performance) - Query optimization
