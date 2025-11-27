![Alloy logo](docs/public/alloy.png)

# Alloy

React/TSX SSR with optional hydration and asset serving. Uses QuickJS for server rendering and esbuild/Tailwind for bundles.

## Install

```sh
go get github.com/3-lines-studio/alloy@latest
go install github.com/3-lines-studio/alloy/cmd/alloy@latest
```

Requires Go 1.25+ and a Node toolchain with `tailwindcss` available via `pnpm`, `yarn`, `npm`, `bun`, or `npx`.

## Build for production

1. Place pages in `app/pages/*.tsx` with a sibling `app/pages/app.css` for Tailwind.
2. Build bundles:

```sh
alloy

# for more options
alloy -pages app/pages -out app/dist/alloy
```

This writes server/client/CSS bundles and a `manifest.json` under `app/dist/alloy`.

3. Serve prebuilt bundles:

```go
//go:embed app/dist/alloy/* public/*
var dist embed.FS

pages := []alloy.Page{
	{Route: "/", Component: "app/pages/home.tsx", Name: "home"},
}

handler, err := alloy.Handler(dist, pages)
if err != nil {
	log.Fatal(err)
}
http.ListenAndServe(":8080", handler)
```

For dynamic props, set `Props` on each `Page`; `Handler` uses prebuilt assets when `ALLOY_DEV` is unset.

## Contributing / local dev

```sh
go test ./...
```

Set `ALLOY_DEV=1` to rebuild on each request. Tailwind is run via your package manager (`pnpm`, `yarn`, `npm`, `bun`, or `npx`).
