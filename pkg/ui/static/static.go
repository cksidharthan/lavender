package static

import "embed"

// FS holds the static assets (htmx, css, favicon) served under /static.
//
//go:embed htmx.min.js app.css
var FS embed.FS
