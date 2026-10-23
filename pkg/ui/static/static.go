package static

import "embed"

// FS holds the static assets (htmx, css, fonts, favicon) served under /static.
//
//go:embed htmx.min.js app.css fonts favicon.svg manifest.webmanifest icon-192.png icon-512.png apple-touch-icon.png
var FS embed.FS
