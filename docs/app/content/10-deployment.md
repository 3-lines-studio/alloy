# Deployment

Deploy alloy apps as single binaries to any platform.

## Build for production

```sh
# Build alloy assets
alloy

# Compile Go binary with embedded assets
go build -o myapp

# Run
./myapp
```

The binary includes:
- Go HTTP server
- React components (server bundles)
- Client JavaScript (hydration)
- Compiled CSS
- Static assets from `public/`

**No external dependencies.** No Node.js runtime required.

## Dockerfile

Multistage build for minimal image size:

```dockerfile
# Build stage
FROM golang:1.25-alpine AS build

# Install Node.js for Tailwind
RUN apk add --no-cache nodejs npm

WORKDIR /app

# Copy Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy app source
COPY . .

# Install JS dependencies and build alloy assets
RUN npm install
RUN go install github.com/3-lines-studio/alloy/cmd/alloy@latest
RUN alloy

# Build Go binary
RUN CGO_ENABLED=0 GOOS=linux go build -o myapp .

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates
WORKDIR /root/

# Copy binary from build stage
COPY --from=build /app/myapp .

EXPOSE 8080
CMD ["./myapp"]
```

Build and run:

```sh
docker build -t myapp .
docker run -p 8080:8080 myapp
```

## systemd service

Create `/etc/systemd/system/myapp.service`:

```ini
[Unit]
Description=My Alloy App
After=network.target

[Service]
Type=simple
User=www-data
WorkingDirectory=/var/www/myapp
ExecStart=/var/www/myapp/myapp
Restart=on-failure
RestartSec=5s

# Environment variables
Environment="PORT=8080"

# Security
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/www/myapp/data

[Install]
WantedBy=multi-user.target
```

Deploy:

```sh
# Copy binary to server
scp myapp user@server:/var/www/myapp/

# Enable and start service
sudo systemctl enable myapp
sudo systemctl start myapp
sudo systemctl status myapp
```

## Reverse proxy (nginx)

Serve alloy behind nginx for TLS termination and caching:

```nginx
server {
    listen 80;
    server_name example.com;

    # Redirect HTTP to HTTPS
    return 301 https://$server_name$request_uri;
}

server {
    listen 443 ssl http2;
    server_name example.com;

    ssl_certificate /etc/letsencrypt/live/example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/example.com/privkey.pem;

    # Security headers
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-XSS-Protection "1; mode=block" always;

    # Cache static assets
    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2)$ {
        proxy_pass http://localhost:8080;
        proxy_cache_valid 200 1y;
        add_header Cache-Control "public, immutable";
    }

    # Proxy to alloy app
    location / {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## Caddy (simpler alternative)

```caddyfile
example.com {
    reverse_proxy localhost:8080

    # Automatic HTTPS via Let's Encrypt
    # No TLS config needed

    # Cache static assets
    @static path *.js *.css *.png *.jpg *.svg
    header @static Cache-Control "public, max-age=31536000, immutable"
}
```

## Cloud platforms

### Fly.io

Create `fly.toml`:

```toml
app = "myapp"

[build]
  builder = "paketobuildpacks/builder:base"

[[services]]
  internal_port = 8080
  protocol = "tcp"

  [[services.ports]]
    handlers = ["http"]
    port = 80
    force_https = true

  [[services.ports]]
    handlers = ["tls", "http"]
    port = 443
```

Deploy:

```sh
fly launch
fly deploy
```

### Railway

Create `railway.toml`:

```toml
[build]
builder = "NIXPACKS"

[deploy]
startCommand = "./myapp"
```

Push to Git and Railway auto-deploys.

### Render

Create `render.yaml`:

```yaml
services:
  - type: web
    name: myapp
    env: go
    buildCommand: |
      npm install
      go install github.com/3-lines-studio/alloy/cmd/alloy@latest
      alloy
      go build -o myapp
    startCommand: ./myapp
```

Connect Git repo in Render dashboard.

### Google Cloud Run

```dockerfile
# Same Dockerfile as above
```

Build and deploy:

```sh
gcloud builds submit --tag gcr.io/PROJECT_ID/myapp
gcloud run deploy myapp \
  --image gcr.io/PROJECT_ID/myapp \
  --platform managed \
  --region us-central1 \
  --allow-unauthenticated
```

## Environment variables

Configure via environment:

```go
package main

import (
	"log"
	"os"

	"github.com/3-lines-studio/alloy"
	"myapp/loader"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	pages := []alloy.Page{
		{Route: "/", Component: "app/pages/home.tsx", Props: loader.Home},
	}

	addr := ":" + port
	log.Printf("Server running on %s", addr)
	if err := alloy.ListenAndServe(addr, nil, pages); err != nil {
		log.Fatal(err)
	}
}
```

Set in deployment:

```sh
# Docker
docker run -e PORT=3000 -p 3000:3000 myapp

# systemd
Environment="PORT=3000"

# Fly.io
fly secrets set PORT=3000
```

## Database connections

Use environment variables for connection strings:

```go
func initDB() *sql.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL not set")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatal(err)
	}

	return db
}
```

**Note:** Don't embed sensitive credentials in your binary. Use environment variables or secret management systems.

## Health checks

Add a health endpoint:

```go
func main() {
	mux := http.NewServeMux()

	// Alloy handler
	handler, _ := alloy.Handler(dist, pages)
	mux.Handle("/", handler)

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	http.ListenAndServe(":8080", mux)
}
```

Use in Kubernetes liveness/readiness probes, load balancers, or monitoring.

## Graceful shutdown

Handle SIGTERM for zero-downtime deployments:

```go
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	handler, _ := alloy.Handler(dist, pages)
	srv := &http.Server{
		Addr:    ":8080",
		Handler: handler,
	}

	// Start server in goroutine
	go func() {
		log.Println("Server running on :8080")
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	// Wait for SIGTERM
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	// Graceful shutdown with 30s timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Println("Shutting down gracefully...")
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}
	log.Println("Server stopped")
}
```

## Binary size optimization

Reduce binary size with build flags:

```sh
go build -ldflags="-s -w" -o myapp
```

- `-s`: Strip symbol table
- `-w`: Strip DWARF debugging info

**Result:** ~30-50% size reduction.

Further optimization with UPX:

```sh
upx --best --lzma myapp
```

## Static file serving

Serve additional static files from `public/`:

```
public/
├── favicon.ico
├── robots.txt
└── images/
    └── logo.png
```

Embed with `go:embed`:

```go
//go:embed app/dist/alloy/* public/*
var dist embed.FS
```

Alloy serves files from `public/` automatically at their paths (e.g., `/favicon.ico`, `/images/logo.png`).

## Monitoring

Log requests for observability:

```go
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %v", r.Method, r.URL.Path, time.Since(start))
	})
}

func main() {
	handler, _ := alloy.Handler(dist, pages)
	http.ListenAndServe(":8080", loggingMiddleware(handler))
}
```

Integrate with structured logging (zerolog, slog) or APM tools (Datadog, New Relic).

## Next steps

- [Production builds](/09-production-builds) - alloy CLI
- [Performance](/17-performance) - Optimization techniques
- [Error handling](/16-error-handling) - Production error patterns
