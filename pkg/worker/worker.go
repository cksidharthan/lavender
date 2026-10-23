// Package worker fetches the articles of the feeds in the background: a pool of workers that handles a job per
// feed, and a scheduler that queues the feeds that are due for a refresh.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/cksidharthan/lavender/pkg/feedfetch"
	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	lavenderOTEL "github.com/cksidharthan/lavender/pkg/otel"
	"github.com/cksidharthan/lavender/pkg/store"
	"github.com/pocketbase/pocketbase/core"
	"go.opentelemetry.io/otel/attribute"
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
	queued map[string]struct{}
	// queuedAt is when the feeds in the queue were queued, to measure how long they wait
	queuedAt map[string]time.Time
	waiters  map[string][]chan struct{}
	wg       sync.WaitGroup
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
		queuedAt: make(map[string]time.Time),
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

	if err := lavenderOTEL.RegisterWorkerGauges(func() int64 { return int64(len(p.jobs)) }, p.workers); err != nil {
		p.logger.Error("error registering the worker metrics", slog.Any(lavenderLogger.ErrorKey, err))
	}

	p.logger.Info("feed workers started", slog.Int("workers", p.workers), slog.Duration("refresh_interval", p.interval))
}

// Wait returns when all the workers and the scheduler stopped, after ctx of Start is done.
func (p *Pool) Wait() {
	p.wg.Wait()
}

// Enqueue queues the feed to be fetched. It returns false when the feed is already queued, or the queue is full,
// in which case the scheduler picks the feed up when it is due.
func (p *Pool) Enqueue(feedID string) bool {
	return p.enqueue(feedID, lavenderOTEL.SourceUser)
}

// enqueue queues the feed, the source (user or scheduler) is the label of the metrics.
func (p *Pool) enqueue(feedID, source string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.queued[feedID]; ok {
		lavenderOTEL.RecordFeedEnqueue(context.Background(), source, "duplicate")
		return false
	}

	select {
	case p.jobs <- feedID:
		p.queued[feedID] = struct{}{}
		p.queuedAt[feedID] = time.Now()
		lavenderOTEL.RecordFeedEnqueue(context.Background(), source, "queued")
		return true
	default:
		lavenderOTEL.RecordFeedEnqueue(context.Background(), source, "full")
		p.logger.Warn("feed queue is full", slog.String("feed_id", feedID), slog.Int("queue_size", queueSize))
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
			delete(p.queuedAt, feedID)
			for _, waiter := range p.waiters[feedID] {
				close(waiter)
			}
			delete(p.waiters, feedID)
			p.mu.Unlock()
		}
	}
}

// queueWait is how long the feed waited in the queue, zero when it was not queued.
func (p *Pool) queueWait(feedID string) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	if queuedAt, ok := p.queuedAt[feedID]; ok {
		delete(p.queuedAt, feedID)
		return time.Since(queuedAt)
	}

	return 0
}

func (p *Pool) process(ctx context.Context, feedID string, workerID int) {
	// every job is a trace of its own, nothing started it that is still waiting for it
	ctx, span := lavenderOTEL.StartNewSpanWithName(ctx, "feed.refresh")
	defer span.End()
	span.SetAttributes(attribute.String("feed.id", feedID), attribute.Int("worker.id", workerID))

	started := time.Now()
	wait := p.queueWait(feedID)
	finished := lavenderOTEL.FeedJobStarted(ctx)
	defer finished()

	// the site of the feed, known once the url is read
	var host string
	result, added, items := "success", 0, 0
	defer func() {
		span.SetAttributes(
			attribute.String("feed.refresh.result", result),
			attribute.Int("feed.articles.new", added),
			attribute.Float64("feed.queue.wait_seconds", wait.Seconds()),
		)
		lavenderOTEL.RecordFeedJob(ctx, lavenderOTEL.FeedJob{
			Result: result, Host: host, Duration: time.Since(started), QueueWait: wait, NewArticles: added,
		})
	}()

	logger := p.logger.With(slog.String("feed_id", feedID), slog.Int("worker", workerID), slog.Duration("queue_wait", wait))

	feedURL, err := p.store.FeedURL(feedID)
	if errors.Is(err, store.ErrNotFound) {
		result = "not_found"
		return // deleted while it was queued
	}
	if err != nil {
		result = "store_error"
		lavenderOTEL.RecordError(ctx, err)
		logger.ErrorContext(ctx, "error finding feed", slog.Any(lavenderLogger.ErrorKey, err))
		return
	}

	if u, err := url.Parse(feedURL); err == nil {
		host = u.Hostname()
		logger = logger.With(slog.String("server.address", host))
		span.SetAttributes(attribute.String("server.address", host))
	}

	fetchCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	fetched, err := p.fetch(fetchCtx, feedURL)
	if err != nil {
		if ctx.Err() != nil {
			result = "cancelled"
			return // shutting down, the feed is fetched on the next start
		}

		result = "fetch_error"
		lavenderOTEL.RecordError(ctx, err)
		logger.InfoContext(ctx, "error fetching feed", slog.String("url", feedURL), slog.Any(lavenderLogger.ErrorKey, err))
		if err := p.store.SaveFetchError(feedID, err); err != nil && !errors.Is(err, store.ErrNotFound) {
			logger.ErrorContext(ctx, "error storing the fetch error", slog.Any(lavenderLogger.ErrorKey, err))
		}

		return
	}

	items = len(fetched.Items)
	added, err = p.store.SaveFetch(feedID, fetched)
	if err != nil {
		result = "store_error"
		added = 0
		lavenderOTEL.RecordError(ctx, err)
		logger.ErrorContext(ctx, "error storing the feed", slog.Any(lavenderLogger.ErrorKey, err))
		return
	}

	logger.InfoContext(ctx, "feed fetched", slog.String("url", feedURL), slog.Int("articles", items), slog.Int("new_articles", added),
		slog.Duration(lavenderLogger.DurationKey, time.Since(started)))
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
	ctx, span := lavenderOTEL.StartNewSpanWithName(context.Background(), "feed.schedule")
	defer span.End()

	ids, err := p.store.DueFeeds(time.Now(), p.interval, dueBatchLimit)
	if err != nil {
		lavenderOTEL.RecordError(ctx, err)
		p.logger.ErrorContext(ctx, "error finding the feeds that are due", slog.Any(lavenderLogger.ErrorKey, err))
		return
	}

	queued := 0
	for _, id := range ids {
		if p.enqueue(id, lavenderOTEL.SourceScheduler) {
			queued++
		}
	}

	span.SetAttributes(attribute.Int("feeds.due", len(ids)), attribute.Int("feeds.queued", queued))

	if queued > 0 {
		p.logger.InfoContext(ctx, "feeds queued for refresh", slog.Int("count", queued), slog.Int("due", len(ids)))
	}
}
