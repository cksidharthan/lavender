package middlewares

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeviceClass(t *testing.T) {
	for ua, want := range map[string]string{
		"":           DeviceUnknown,
		"curl/8.7.1": DeviceBot,
		"Googlebot/2.1 (+http://www.google.com/bot.html)": DeviceBot,
		"kube-probe/1.30": DeviceBot,
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148":   DeviceMobile,
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/126 Mobile Safari/537.36": DeviceMobile,
		"Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148":            DeviceTablet,
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/126 Safari/537.36": DeviceDesktop,
	} {
		assert.Equal(t, want, DeviceClass(ua), ua)
	}
}

func TestRequestID(t *testing.T) {
	req := func(id string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if id != "" {
			r.Header.Set(requestIDHeader, id)
		}

		return r
	}

	assert.Equal(t, "abc-123_x.y", requestID(req("abc-123_x.y")), "a good id of the client is kept")

	for _, bad := range []string{"", "has space", "new\\nline", strings.Repeat("a", 65), "<script>"} {
		id := requestID(req(bad))
		assert.Len(t, id, 16, "a bad id is replaced: %q", bad)
		assert.NotEqual(t, bad, id)
	}
}
