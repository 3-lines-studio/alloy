# Authentication

Implement authentication and authorization in alloy apps.

## Session cookies

### Basic session auth

```go
package loader

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
)

var sessions = struct {
	sync.RWMutex
	data map[string]string  // sessionID -> userID
}{data: make(map[string]string)}

func Login(w http.ResponseWriter, r *http.Request) {
	// Validate credentials (omitted)
	userID := "user123"

	// Create session
	sessionID := generateSessionID()
	sessions.Lock()
	sessions.data[sessionID] = userID
	sessions.Unlock()

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,  // HTTPS only
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400 * 7,  // 7 days
	})

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func getAuthenticatedUser(r *http.Request) (string, bool) {
	cookie, err := r.Cookie("session")
	if err != nil {
		return "", false
	}

	sessions.RLock()
	userID, exists := sessions.data[cookie.Value]
	sessions.RUnlock()

	return userID, exists
}

func Dashboard(r *http.Request) map[string]any {
	userID, authenticated := getAuthenticatedUser(r)
	if !authenticated {
		return map[string]any{
			"authenticated": false,
			"redirect":      "/login",
		}
	}

	user := fetchUser(userID)
	return map[string]any{
		"authenticated": true,
		"user":          user,
	}
}

func generateSessionID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
```

### Component with redirect

```tsx
export default function Dashboard({ authenticated, redirect, user }) {
	if (!authenticated && redirect) {
		if (typeof window !== 'undefined') {
			window.location.href = redirect;
		}
		return <p>Redirecting to login...</p>;
	}

	return (
		<div>
			<h1>Dashboard</h1>
			<p>Welcome, {user.name}</p>
		</div>
	);
}
```

## JWT tokens

### Generate and verify JWT

```go
import "github.com/golang-jwt/jwt/v5"

var jwtSecret = []byte(os.Getenv("JWT_SECRET"))

type Claims struct {
	UserID string `json:"userId"`
	jwt.RegisteredClaims
}

func createJWT(userID string) (string, error) {
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func verifyJWT(tokenString string) (string, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		return jwtSecret, nil
	})

	if err != nil {
		return "", err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims.UserID, nil
	}

	return "", fmt.Errorf("🔴 invalid token")
}
```

### Use JWT in props

```go
func ProtectedPage(r *http.Request) map[string]any {
	cookie, err := r.Cookie("auth_token")
	if err != nil {
		return map[string]any{"error": "Not authenticated"}
	}

	userID, err := verifyJWT(cookie.Value)
	if err != nil {
		return map[string]any{"error": "Invalid token"}
	}

	data := fetchUserData(userID)
	return map[string]any{
		"user": data,
	}
}
```

## Context-based auth

### Auth context function

```go
func withAuth(r *http.Request) context.Context {
	userID, authenticated := getAuthenticatedUser(r)

	ctx := context.WithValue(r.Context(), "authenticated", authenticated)
	ctx = context.WithValue(ctx, "userID", userID)

	return ctx
}

pages := []alloy.Page{
	{
		Route:     "/dashboard",
		Component: "app/pages/dashboard.tsx",
		Props:     Dashboard,
		Ctx:       withAuth,
	},
}
```

### Access in props

```go
func Dashboard(r *http.Request) map[string]any {
	authenticated := r.Context().Value("authenticated").(bool)
	if !authenticated {
		return map[string]any{
			"authenticated": false,
		}
	}

	userID := r.Context().Value("userID").(string)
	user := fetchUser(userID)

	return map[string]any{
		"authenticated": true,
		"user":          user,
	}
}
```

## Middleware auth

### Auth middleware

```go
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for public routes
		if r.URL.Path == "/login" || r.URL.Path == "/" {
			next.ServeHTTP(w, r)
			return
		}

		_, authenticated := getAuthenticatedUser(r)
		if !authenticated {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	handler, _ := alloy.Handler(dist, pages)
	authed := authMiddleware(handler)

	http.ListenAndServe(":8080", authed)
}
```

## OAuth 2.0

### Google OAuth example

```go
import "golang.org/x/oauth2"
import "golang.org/x/oauth2/google"

var googleOAuth = &oauth2.Config{
	ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
	ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
	RedirectURL:  "http://localhost:8080/auth/callback",
	Scopes:       []string{"profile", "email"},
	Endpoint:     google.Endpoint,
}

func GoogleLogin(w http.ResponseWriter, r *http.Request) {
	url := googleOAuth.AuthCodeURL("state", oauth2.AccessTypeOffline)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func GoogleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")

	token, err := googleOAuth.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "Failed to exchange token", 500)
		return
	}

	// Fetch user info
	client := googleOAuth.Client(r.Context(), token)
	resp, _ := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	defer resp.Body.Close()

	var userInfo struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&userInfo)

	// Create session
	sessionID := createSession(userInfo.ID)

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    sessionID,
		HttpOnly: true,
		Secure:   true,
	})

	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}
```

## Password hashing

```go
import "golang.org/x/crypto/bcrypt"

func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

func checkPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func Register(w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	password := r.FormValue("password")

	hash, _ := hashPassword(password)

	// Save to database
	db.Exec("INSERT INTO users (email, password_hash) VALUES ($1, $2)", email, hash)

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func Login(w http.ResponseWriter, r *http.Request) {
	email := r.FormValue("email")
	password := r.FormValue("password")

	var userID string
	var hash string
	err := db.QueryRow("SELECT id, password_hash FROM users WHERE email = $1", email).Scan(&userID, &hash)

	if err != nil || !checkPassword(password, hash) {
		http.Error(w, "Invalid credentials", 401)
		return
	}

	// Create session...
}
```

## Role-based access

```go
type User struct {
	ID    string
	Email string
	Role  string  // "admin", "editor", "viewer"
}

func AdminOnly(r *http.Request) map[string]any {
	userID, authenticated := getAuthenticatedUser(r)
	if !authenticated {
		return map[string]any{"error": "Not authenticated"}
	}

	user := fetchUser(userID)
	if user.Role != "admin" {
		return map[string]any{"error": "Admin access required"}
	}

	data := fetchAdminData()
	return map[string]any{
		"user": user,
		"data": data,
	}
}
```

## Logout

```go
func Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err == nil {
		// Remove session
		sessions.Lock()
		delete(sessions.data, cookie.Value)
		sessions.Unlock()
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:   "session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}
```

## Next steps

- [Error handling](/16-error-handling) - Handle auth errors
- [Dynamic data](/07-dynamic-data) - Database queries
- [Page struct](/12-page-struct) - Context functions
