# Repository Guidelines

## Project Structure & Module Organization
- Core Go package lives at `render.go` with tests in `render_test.go`.
- CLI builder sits in `cmd/alloy`; it discovers `app/pages/*.tsx`, builds server/client bundles, and writes `dist/alloy` plus a manifest.
- Example app is under `cmd/sample` (`app/pages`, `app/components`, `public`, `loader`); use it as the integration reference.

## Build, Test, and Development Commands
- `go test ./...` — runs the Go test suite.
- `go build ./cmd/alloy` — builds the CLI locally; use `go install github.com/3-lines-studio/alloy/cmd/alloy@latest` to install it.
- `alloy -pages app/pages -out app/dist/alloy` — generates prebuilt server/client/CSS assets and `manifest.json` for a page set.
- Set `ALLOY_DEV=1` when running the handler in development to rebuild on each request; leave it unset for production to serve prebuilt assets.

## Coding Style & Naming Conventions
- Go 1.25; run `gofmt` (tabs, stdlib-first imports) and keep dependencies minimal.
- Prefer early returns over nested conditionals; keep boolean conditions named for clarity.
- Exported types/functions use PascalCase; unexported identifiers use lowerCamelCase. Tests end with `_test.go`.
- TSX components and pages mirror the sample: page files use lowercase names (e.g., `home.tsx`) and render a root with `defaultRootID(name)`.

## Testing Guidelines
- Use `go test ./...` for unit/integration coverage; tests rely on sample fixtures under `cmd/sample`.
- New tests should follow `TestXxx` naming and clean up temp files/dirs.
- When adding route or asset behavior, cover both dev (`ALLOY_DEV=1`) and prebuilt flows where practical.

## Commit & Pull Request Guidelines
- Follow the existing conventional style: `feat:`, `refactor:`, `docs:` prefixes (see `git log`).
- PRs should state the problem, the approach, and any follow-up work; link issues when relevant.
- Include test results and reproduction steps for page/asset changes; note any new env vars or build outputs (`dist/alloy/*`, `manifest.json`).

## Security & Configuration Notes
- Never commit generated secrets or production paths; `dist/alloy` artifacts are build outputs and should be regenerated, not hand-edited.
- Ensure a Node toolchain with `tailwindcss` is available (`pnpm`, `yarn`, `npm`, or `bun`) before running the CLI or sample app.
