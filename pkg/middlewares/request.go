package middlewares

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

const requestIDHeader = "X-Request-Id"

// requestID is the id the client gave the request, or a new one. An id from the client is only taken when it is
// short and made of safe characters, as it ends up in the logs and in a response header.
func requestID(r *http.Request) string {
	if id := r.Header.Get(requestIDHeader); validRequestID(id) {
		return id
	}

	b := make([]byte, 8)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}

	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}

	return true
}

// Device classes of the clients, the labels of the metrics.
const (
	DeviceBot     = "bot"
	DeviceMobile  = "mobile"
	DeviceTablet  = "tablet"
	DeviceDesktop = "desktop"
	DeviceUnknown = "unknown"
)

// DeviceClass tells what kind of client sent the request, from its user agent. It is a rough guess meant for a
// metric with few values, not for telling clients apart.
func DeviceClass(userAgent string) string {
	ua := strings.ToLower(userAgent)

	switch {
	case ua == "":
		return DeviceUnknown
	case strings.Contains(ua, "bot"), strings.Contains(ua, "crawl"), strings.Contains(ua, "spider"),
		strings.Contains(ua, "curl/"), strings.Contains(ua, "wget"), strings.Contains(ua, "python-requests"),
		strings.Contains(ua, "go-http-client"), strings.Contains(ua, "prometheus"), strings.Contains(ua, "kube-probe"):
		return DeviceBot
	case strings.Contains(ua, "ipad"), strings.Contains(ua, "tablet"):
		return DeviceTablet
	case strings.Contains(ua, "mobile"), strings.Contains(ua, "iphone"), strings.Contains(ua, "android"):
		return DeviceMobile
	default:
		return DeviceDesktop
	}
}

// isHTMX reports whether htmx made the request, as opposed to the browser loading a page.
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
