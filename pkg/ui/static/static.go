package static

import "embed"

// FS holds the static assets (htmx, css, fonts, favicon) served under /static.
//
//go:embed htmx.min.js app.css fonts
var FS embed.FS
