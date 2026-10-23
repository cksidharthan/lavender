// Package worker fetches the articles of the feeds in the background: a pool of workers that handles a job per
// feed, and a scheduler that queues the feeds that are due for a refresh.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/pocketbase/pocketbase/core"
)

const (
	queueSize     = 512
	jobTimeout    = 30 * time.Second
	dueBatchLimit = 500
	// checkEvery is how often the scheduler looks for feeds that are due.
	checkEvery = time.Minute
)

// Pool fetches feeds. Enqueue a feed id to have its articles fetched and stored.
type Pool struct {
	store    *store.Store
	logger   *slog.Logger
	fetch    func(ctx context.Context, rawURL string) (feedfetch.Result, error)
	workers  int
	interval time.Duration
	check    time.Duration

	jobs chan string
	mu   sync.Mutex
	// queued holds the feeds that are in the queue or being fetched, so a feed is never fetched twice at once.
	queued  map[string]struct{}
	waiters map[string][]chan struct{}
	wg      sync.WaitGroup
}

// New creates a pool of workers feeds are refreshed by when they were not fetched for interval.
func New(app core.App, logger *slog.Logger, workers int, interval time.Duration) *Pool {
	return &Pool{
		store:    store.New(app),
		logger:   logger,
		fetch:    feedfetch.Fetch,
		workers:  max(workers, 1),
		interval: interval,
		check:    checkEvery,
		jobs:     make(chan string, queueSize),
		queued:   make(map[string]struct{}),
		waiters:  make(map[string][]chan struct{}),
	}
}

// Start runs the workers and the scheduler until ctx is done.
func (p *Pool) Start(ctx context.Context) {
	for i := 1; i <= p.workers; i++ {
		p.wg.Add(1)
		go p.work(ctx, i)
	}

	p.wg.Add(1)
	go p.schedule(ctx)

	p.logger.Info("feed workers started", slog.Int("workers", p.workers), slog.Duration("refresh_interval", p.interval))
}

// Wait returns when all the workers and the scheduler stopped, after ctx of Start is done.
func (p *Pool) Wait() {
	p.wg.Wait()
}

// Enqueue queues the feed to be fetched. It returns false when the feed is already queued, or the queue is full,
// in which case the scheduler picks the feed up when it is due.
func (p *Pool) Enqueue(feedID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.queued[feedID]; ok {
		return false
	}

	select {
	case p.jobs <- feedID:
		p.queued[feedID] = struct{}{}
		return true
	default:
		p.logger.Warn("feed queue is full", slog.String("feed_id", feedID))
		return false
	}
}

// Done returns a channel that is closed when the feed that is queued or being fetched is done. It is closed
// already when the feed is not queued.
func (p *Pool) Done(feedID string) <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	done := make(chan struct{})
	if _, ok := p.queued[feedID]; !ok {
		close(done)
		return done
	}

	p.waiters[feedID] = append(p.waiters[feedID], done)

	return done
}

func (p *Pool) work(ctx context.Context, id int) {
	defer p.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case feedID := <-p.jobs:
			p.process(ctx, feedID, id)

			p.mu.Lock()
			delete(p.queued, feedID)
			for _, waiter := range p.waiters[feedID] {
				close(waiter)
			}
			delete(p.waiters, feedID)
			p.mu.Unlock()
		}
	}
}

func (p *Pool) process(ctx context.Context, feedID string, workerID int) {
	logger := p.logger.With(slog.String("feed_id", feedID), slog.Int("worker", workerID))

	feedURL, err := p.store.FeedURL(feedID)
	if errors.Is(err, store.ErrNotFound) {
		return // deleted while it was queued
	}
	if err != nil {
		logger.Error("error finding feed", slog.Any("error", err))
		return
	}

	fetchCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	result, err := p.fetch(fetchCtx, feedURL)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down, the feed is fetched on the next start
		}

		logger.Info("error fetching feed", slog.String("url", feedURL), slog.Any("error", err))
		if err := p.store.SaveFetchError(feedID, err); err != nil && !errors.Is(err, store.ErrNotFound) {
			logger.Error("error storing the fetch error", slog.Any("error", err))
		}

		return
	}

	added, err := p.store.SaveFetch(feedID, result)
	if err != nil {
		logger.Error("error storing the feed", slog.Any("error", err))
		return
	}

	logger.Info("feed fetched", slog.String("url", feedURL), slog.Int("articles", len(result.Items)), slog.Int("new_articles", added))
}

// schedule queues the feeds that were not fetched for their refresh interval, and the feeds that were never fetched.
func (p *Pool) schedule(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.check)
	defer ticker.Stop()

	for {
		p.queueDue()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Pool) queueDue() {
	ids, err := p.store.DueFeeds(time.Now(), p.interval, dueBatchLimit)
	if err != nil {
		p.logger.Error("error finding the feeds that are due", slog.Any("error", err))
		return
	}

	queued := 0
	for _, id := range ids {
		if p.Enqueue(id) {
			queued++
		}
	}

	if queued > 0 {
		p.logger.Info("feeds queued for refresh", slog.Int("count", queued))
	}
}
