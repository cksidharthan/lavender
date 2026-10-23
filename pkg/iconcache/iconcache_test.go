package iconcache

import (
	"bytes"
	"context"
	"image"
	stdpng "image/png"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// a 1x1 png
var png = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89}

func newCache(t *testing.T, srv *httptest.Server) *Cache {
	t.Helper()

	c, err := New(t.TempDir())
	require.NoError(t, err)
	c.client = srv.Client() // the default client refuses the loopback address of the test server

	return c
}

func TestDownloadsOnceAndServesFromDisk(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream") // what the server says does not matter
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	c := newCache(t, srv)
	for range 3 {
		icon, err := c.Get(context.Background(), srv.URL+"/icon")
		require.NoError(t, err)
		require.Equal(t, "image/png", icon.ContentType)
		require.Equal(t, png, icon.Data)
	}
	require.EqualValues(t, 1, hits.Load())

	// a new cache on the same directory still has it
	c2, err := New(c.dir)
	require.NoError(t, err)
	c2.client = c.client
	_, err = c2.Get(context.Background(), srv.URL+"/icon")
	require.NoError(t, err)
	require.EqualValues(t, 1, hits.Load())
}

func TestRefreshesAfterAWeekAndKeepsTheOldIconWhenItFails(t *testing.T) {
	var hits atomic.Int32
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "gone", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	c := newCache(t, srv)
	_, err := c.Get(context.Background(), srv.URL)
	require.NoError(t, err)

	c.now = func() time.Time { return time.Now().Add(freshFor + time.Hour) }
	_, err = c.Get(context.Background(), srv.URL)
	require.NoError(t, err)
	require.EqualValues(t, 2, hits.Load(), "downloaded again after a week")

	fail.Store(true)
	c.now = func() time.Time { return time.Now().Add(3 * freshFor) }
	icon, err := c.Get(context.Background(), srv.URL)
	require.NoError(t, err, "the old icon is better than none")
	require.Equal(t, png, icon.Data)
}

func TestRejectsWhatIsNotAnImageAndDoesNotRetryAtOnce(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png") // lies
		_, _ = w.Write([]byte("<html><script>alert(1)</script></html>"))
	}))
	defer srv.Close()

	c := newCache(t, srv)
	for range 3 {
		_, err := c.Get(context.Background(), srv.URL)
		require.ErrorIs(t, err, ErrUnavailable)
	}
	require.EqualValues(t, 1, hits.Load(), "a failure is remembered for an hour")

	c.now = func() time.Time { return time.Now().Add(failedFor + time.Minute) }
	_, err := c.Get(context.Background(), srv.URL)
	require.ErrorIs(t, err, ErrUnavailable)
	require.EqualValues(t, 2, hits.Load())
}

func TestAcceptsSvgOnlyWhenItIsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		if r.URL.Path == "/fake" {
			_, _ = w.Write([]byte("<html>not an svg</html>"))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	}))
	defer srv.Close()

	c := newCache(t, srv)
	icon, err := c.Get(context.Background(), srv.URL+"/real")
	require.NoError(t, err)
	require.Equal(t, "image/svg+xml", icon.ContentType)

	_, err = c.Get(context.Background(), srv.URL+"/fake")
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestTooBigAndBadUrls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(append(append([]byte{}, png...), make([]byte, maxIconBytes)...))
	}))
	defer srv.Close()

	c := newCache(t, srv)
	_, err := c.Get(context.Background(), srv.URL)
	require.ErrorIs(t, err, ErrUnavailable)

	for _, bad := range []string{"", "file:///etc/passwd", "ftp://x/icon", "/relative"} {
		_, err := c.Get(context.Background(), bad)
		require.ErrorIs(t, err, ErrUnavailable, bad)
	}
}

func TestDefaultClientRefusesPrivateAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(png) }))
	defer srv.Close()

	c, err := New(t.TempDir())
	require.NoError(t, err)

	_, err = c.Get(context.Background(), srv.URL)
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestGetFirstFallsBackToTheNextIcon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/good.png" {
			_, _ = w.Write(png)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := newCache(t, srv)
	icon, err := c.GetFirst(context.Background(), srv.URL+"/missing.png/", srv.URL+"/missing.png", srv.URL+"/good.png")
	require.NoError(t, err)
	require.Equal(t, png, icon.Data)

	_, err = c.GetFirst(context.Background(), srv.URL+"/a", srv.URL+"/b")
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = c.GetFirst(context.Background())
	require.ErrorIs(t, err, ErrUnavailable)
}

func pngOfSize(t *testing.T, w, h int) []byte {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, stdpng.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))))

	return buf.Bytes()
}

func TestGetFirstPrefersASquareIconOverAWideLogo(t *testing.T) {
	wide, square := pngOfSize(t, 300, 60), pngOfSize(t, 64, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wide.png":
			_, _ = w.Write(wide)
		case "/square.png":
			_, _ = w.Write(square)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newCache(t, srv)

	icon, err := c.GetFirst(context.Background(), srv.URL+"/wide.png", srv.URL+"/square.png")
	require.NoError(t, err)
	require.Equal(t, square, icon.Data, "the wide logo is passed over")

	icon, err = c.GetFirst(context.Background(), srv.URL+"/wide.png", srv.URL+"/missing.png")
	require.NoError(t, err)
	require.Equal(t, wide, icon.Data, "a wide logo is better than no icon")
}
