package router

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cksidharthan/lavender/pkg/buildinfo"
	"github.com/cksidharthan/lavender/pkg/iconcache"
	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	"github.com/cksidharthan/lavender/pkg/otel"
	"github.com/cksidharthan/lavender/pkg/pocketbase/db"
	"github.com/pocketbase/dbx"
	pb "github.com/pocketbase/pocketbase"
)

// gaugeCacheFor is how long what the gauges read is kept. The metrics are exported every few seconds, the numbers of
// the database and the disk do not need to be that fresh, and counting them is not free.
const gaugeCacheFor = 30 * time.Second

// registerGauges registers the metrics that are read when they are exported: the build, the go runtime, the number
// of records and the state of the feeds in the database, and the use of the disk.
func registerGauges(logger *slog.Logger, pbApp *pb.PocketBase, icons *iconcache.Cache, dataDir string) {
	started := time.Now()
	otel.InitCounters(context.Background())
	failed := func(what string, err error) {
		if err != nil {
			logger.Error("error registering the "+what+" metrics", slog.Any(lavenderLogger.ErrorKey, err))
		}
	}

	failed("build", otel.RegisterBuildInfo(buildinfo.Version, buildinfo.Revision(), buildinfo.GoVersion()))
	failed("runtime", otel.RegisterRuntimeGauges(started))

	collections := []string{db.UsersCollection, db.FeedsCollection, db.FoldersCollection, db.ArticlesCollection, db.SavedArticlesCollection}
	totals := newCached(func() (map[string]int64, error) {
		counts := make(map[string]int64, len(collections))
		for _, name := range collections {
			n, err := pbApp.CountRecords(name)
			if err != nil {
				return nil, err
			}
			counts[name] = n
		}

		return counts, nil
	})
	failed("database", otel.RegisterTotalsGauges(collections, func(name string) (int64, error) {
		counts, err := totals.get()

		return counts[name], err
	}))

	feeds := newCached(func() (otel.FeedStates, error) { return feedStates(pbApp) })
	failed("feed state", otel.RegisterFeedGauges(feeds.get))

	storage := newCached(func() (otel.Storage, error) { return storageOf(dataDir, icons) })
	failed("storage", otel.RegisterStorageGauges(storage.get))
}

// feedStates counts the feeds by state.
func feedStates(pbApp *pb.PocketBase) (otel.FeedStates, error) {
	var s otel.FeedStates

	count := func(exprs ...dbx.Expression) (int64, error) { return pbApp.CountRecords(db.FeedsCollection, exprs...) }

	var err error
	if s.Total, err = count(); err != nil {
		return s, err
	}
	if s.Active, err = count(dbx.HashExp{"is_active": true}); err != nil {
		return s, err
	}
	s.Paused = s.Total - s.Active
	if s.Errored, err = count(dbx.NewExp("last_error != ''")); err != nil {
		return s, err
	}
	if s.Waiting, err = count(dbx.HashExp{"is_active": true}, dbx.NewExp("last_fetched_at = ''")); err != nil {
		return s, err
	}

	oldest, err := pbApp.FindRecordsByFilter(db.FeedsCollection, "is_active = true && last_fetched_at != ''", "last_fetched_at", 1, 0)
	if err != nil {
		return s, err
	}
	if len(oldest) == 1 {
		s.OldestFetchAge = time.Since(oldest[0].GetDateTime("last_fetched_at").Time())
	}

	return s, nil
}

// storageOf measures the files of the database and the icon cache.
func storageOf(dataDir string, icons *iconcache.Cache) (otel.Storage, error) {
	s := otel.Storage{Files: map[string]int64{}}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return s, err
	}

	for _, entry := range entries {
		// the sqlite files, and their write ahead log: that is what grows
		name := entry.Name()
		if entry.IsDir() || !(strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-shm")) {
			continue
		}

		if info, err := os.Stat(filepath.Join(dataDir, name)); err == nil {
			s.Files[name] = info.Size()
		}
	}

	if count, size, err := icons.Stats(); err == nil {
		s.Icons, s.IconBytes = int64(count), size
	}

	return s, nil
}

// cached keeps what read returns for gaugeCacheFor, a failed read is not kept.
type cached[T any] struct {
	read func() (T, error)

	mu    sync.Mutex
	value T
	at    time.Time
}

func newCached[T any](read func() (T, error)) *cached[T] {
	return &cached[T]{read: read}
}

func (c *cached[T]) get() (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.at.IsZero() && time.Since(c.at) < gaugeCacheFor {
		return c.value, nil
	}

	value, err := c.read()
	if err != nil {
		return value, err
	}

	c.value, c.at = value, time.Now()

	return value, nil
}
