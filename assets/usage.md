Usage: alloy <command> [flags]

Commands:
  build    Build production bundles with content hashes
  dev      Run with live reload

Flags:
  --pages string
        Directory containing page components (.tsx)
        Auto-discovers: app/pages or pages
  --out string
        Output directory for bundles
        Default: {pages_parent}/dist/alloy

Examples:
  alloy build
  alloy build --pages app/pages --out app/dist
  alloy dev
  alloy dev --pages app/pages --out app/dist
  alloy watch
