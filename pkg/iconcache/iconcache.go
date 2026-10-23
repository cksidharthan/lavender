// Package iconcache downloads the icons of the feeds once and keeps them on disk, so the browser gets them from
// this server: they are cached however the site of the feed caches them, and the site does not see the readers.
package iconcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	_ "golang.org/x/image/webp"
)

const (
	maxIconBytes = 1 << 20
	// freshFor is how long a downloaded icon is used before it is downloaded again.
	freshFor = 7 * 24 * time.Hour
	// failedFor is how long an icon that could not be downloaded is not tried again.
	failedFor = time.Hour
)

// ErrUnavailable is returned when there is no usable icon at the url.
var ErrUnavailable = errors.New("icon unavailable")

// allowedTypes are the image types that are served, as sniffed from the content.
var allowedTypes = map[string]bool{
	"image/png":     true,
	"image/jpeg":    true,
	"image/gif":     true,
	"image/webp":    true,
	"image/x-icon":  true,
	"image/bmp":     true,
	"image/avif":    true,
	"image/svg+xml": true,
}

// Icon is a downloaded icon.
type Icon struct {
	Data        []byte
	ContentType string
	FetchedAt   time.Time
}

type meta struct {
	ContentType string    `json:"content_type,omitempty"` // empty when the download failed
	FetchedAt   time.Time `json:"fetched_at"`
}

type Cache struct {
	dir    string
	client *http.Client
	now    func() time.Time

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New creates a cache that keeps the icons in dir.
func New(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("error creating the icon cache directory: %w", err)
	}

	return &Cache{dir: dir, client: feedfetch.NewClient(), now: time.Now, locks: make(map[string]*sync.Mutex)}, nil
}

// GetFirst returns the first of the icons that can be downloaded and is about square, in the order of the urls.
// Feeds often give a wide logo with the name of the site as the icon, which is unreadable in the small square an icon is
// shown in. It is only used when none of the other icons is better.
func (c *Cache) GetFirst(ctx context.Context, iconURLs ...string) (Icon, error) {
	var wide *Icon

	for _, iconURL := range iconURLs {
		icon, err := c.Get(ctx, iconURL)
		if errors.Is(err, ErrUnavailable) {
			continue
		}
		if err != nil {
			return Icon{}, err
		}

		if isSquarish(icon) {
			return icon, nil
		}
		if wide == nil {
			wide = &icon
		}
	}

	if wide != nil {
		return *wide, nil
	}

	return Icon{}, ErrUnavailable
}

// isSquarish reports whether the icon is about as wide as it is high. Icons of a type that has no size to read are
// taken as square.
func isSquarish(icon Icon) bool {
	config, _, err := image.DecodeConfig(bytes.NewReader(icon.Data))
	if err != nil || config.Height == 0 {
		return true
	}

	ratio := float64(config.Width) / float64(config.Height)

	return ratio >= 0.67 && ratio <= 1.5
}

// Get returns the icon at the url, downloading it when it is not stored or too old. An icon that is too old is
// still returned when it can not be downloaded again.
func (c *Cache) Get(ctx context.Context, iconURL string) (Icon, error) {
	if _, err := feedfetch.ParseURL(iconURL); err != nil {
		return Icon{}, ErrUnavailable
	}

	key := sum(iconURL)

	// one download per icon at a time, the others use its result.
	unlock := c.lock(key)
	defer unlock()

	stored, hasStored := c.read(key)
	if hasStored {
		age := c.now().Sub(stored.meta.FetchedAt)
		switch {
		case stored.meta.ContentType == "" && age < failedFor:
			return Icon{}, ErrUnavailable
		case stored.meta.ContentType != "" && age < freshFor:
			return stored.icon(), nil
		}
	}

	icon, err := c.download(ctx, iconURL)
	if err != nil {
		if ctx.Err() != nil {
			return Icon{}, ctx.Err()
		}

		if hasStored && stored.meta.ContentType != "" {
			return stored.icon(), nil
		}

		c.write(key, meta{FetchedAt: c.now()}, nil)

		return Icon{}, ErrUnavailable
	}

	c.write(key, meta{ContentType: icon.ContentType, FetchedAt: icon.FetchedAt}, icon.Data)

	return icon, nil
}

func (c *Cache) download(ctx context.Context, iconURL string) (Icon, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, iconURL, nil)
	if err != nil {
		return Icon{}, err
	}
	req.Header.Set("Accept", "image/*")
	req.Header.Set("User-Agent", "Lavender feed reader")

	resp, err := c.client.Do(req)
	if err != nil {
		return Icon{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Icon{}, fmt.Errorf("the icon answered with status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return Icon{}, err
	}
	if len(data) == 0 || len(data) > maxIconBytes {
		return Icon{}, errors.New("the icon is empty or too big")
	}

	contentType, ok := imageType(data, resp.Header.Get("Content-Type"))
	if !ok {
		return Icon{}, errors.New("the url is not an image")
	}

	return Icon{Data: data, ContentType: contentType, FetchedAt: c.now()}, nil
}

// imageType decides the type from the content, never from what the server says, as that would let a page be
// served as an image. Svg has no signature, it is accepted when the server says so and the content looks like it.
func imageType(data []byte, declared string) (string, bool) {
	sniffed, _, _ := strings.Cut(http.DetectContentType(data), ";")
	if allowedTypes[sniffed] {
		return sniffed, true
	}

	if strings.HasPrefix(declared, "image/svg+xml") && bytes.Contains(bytes.ToLower(data[:min(len(data), 1024)]), []byte("<svg")) {
		return "image/svg+xml", true
	}

	return "", false
}

type stored struct {
	meta meta
	data []byte
}

func (s stored) icon() Icon {
	return Icon{Data: s.data, ContentType: s.meta.ContentType, FetchedAt: s.meta.FetchedAt}
}

func (c *Cache) read(key string) (stored, bool) {
	metaBytes, err := os.ReadFile(c.path(key, ".json"))
	if err != nil {
		return stored{}, false
	}

	var m meta
	if json.Unmarshal(metaBytes, &m) != nil {
		return stored{}, false
	}

	if m.ContentType == "" {
		return stored{meta: m}, true
	}

	data, err := os.ReadFile(c.path(key, ".img"))
	if err != nil {
		return stored{}, false
	}

	return stored{meta: m, data: data}, true
}

// write stores the icon, the data first, so a stored meta always has its data. A failed write is not an error, the
// icon is just downloaded again.
func (c *Cache) write(key string, m meta, data []byte) {
	if data != nil {
		if err := writeFile(c.path(key, ".img"), data); err != nil {
			return
		}
	}

	metaBytes, _ := json.Marshal(m)
	_ = writeFile(c.path(key, ".json"), metaBytes)
}

func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

func (c *Cache) path(key, ext string) string {
	return filepath.Join(c.dir, key+ext)
}

// lock locks the key, the returned func unlocks it.
func (c *Cache) lock(key string) func() {
	c.mu.Lock()
	l, ok := c.locks[key]
	if !ok {
		l = &sync.Mutex{}
		c.locks[key] = l
	}
	c.mu.Unlock()

	l.Lock()

	return l.Unlock
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))

	return hex.EncodeToString(h[:])
}

// Version identifies the url of an icon, it changes when the url does. It is part of the url the browser gets, so a
// new icon is not hidden by the old one in the browser cache.
func Version(iconURL string) string {
	// the number changes when the way an icon is picked changes, as the browsers keep the icons for a week.
	return sum("2|" + iconURL)[:8]
}

// Stats counts the icons that are kept, and the room they take on disk.
func (c *Cache) Stats() (icons int, bytes int64, err error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0, 0, fmt.Errorf("error reading the icon cache directory: %w", err)
	}

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || entry.IsDir() || strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}

		bytes += info.Size()
		if strings.HasSuffix(entry.Name(), ".img") {
			icons++
		}
	}

	return icons, bytes, nil
}

// File is a file of the cache, for a backup.
type File struct {
	Name string // as in the cache: a key and .img for the icon, a key and .json for what is known about it
	Data []byte
}

// Files returns the files of the icons at the urls that are kept. Icons that could not be downloaded, and urls that
// have no icon, are left out.
func (c *Cache) Files(iconURLs ...string) []File {
	var files []File

	seen := make(map[string]bool, len(iconURLs))
	for _, iconURL := range iconURLs {
		key := sum(iconURL)
		if seen[key] {
			continue
		}
		seen[key] = true

		stored, ok := c.read(key)
		if !ok || stored.meta.ContentType == "" {
			continue
		}

		metaBytes, err := os.ReadFile(c.path(key, ".json"))
		if err != nil {
			continue
		}

		files = append(files, File{Name: key + ".img", Data: stored.data}, File{Name: key + ".json", Data: metaBytes})
	}

	return files
}

var fileNamePattern = regexp.MustCompile(`^[0-9a-f]{64}\.(img|json)$`)

// IsFileName reports whether the name is one of a file of the cache. A backup is read by it, so a name that is
// anything else is never used for a path.
func IsFileName(name string) bool {
	return fileNamePattern.MatchString(name)
}

// Restore puts the icon of a backup back. The icon is checked again: it must be an image of an allowed type, of a
// size that is allowed, with what is known about it. It returns false when the icon was not valid.
func (c *Cache) Restore(key string, metaBytes, data []byte) bool {
	if !fileNamePattern.MatchString(key+".img") || len(data) == 0 || len(data) > maxIconBytes {
		return false
	}

	var m meta
	if json.Unmarshal(metaBytes, &m) != nil || m.ContentType == "" {
		return false
	}
	if contentType, ok := imageType(data, m.ContentType); !ok || contentType != m.ContentType {
		return false
	}
	if m.FetchedAt.IsZero() || m.FetchedAt.After(c.now()) {
		m.FetchedAt = c.now()
	}

	// the lock of the key, so it does not interfere with a download of the same icon
	unlock := c.lock(key)
	defer unlock()

	// an icon that is there already, and is not older than the one of the backup, stays
	if existing, ok := c.read(key); ok && existing.meta.ContentType != "" && !existing.meta.FetchedAt.Before(m.FetchedAt) {
		return false
	}

	c.write(key, m, data)

	return true
}
