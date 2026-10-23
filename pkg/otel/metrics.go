package otel

import (
	"context"
	"runtime"
	"time"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/cksidharthan/lavender"

// the instruments come from the global meter provider. They can be created before otel.New sets the provider,
// the global meter hands them over to it when it is set, so the packages can record without being wired up.
//
// The attributes are the labels of the metrics. Every one of them has a small, fixed set of values, except
// server.address (the host of a feed) which has one value per site the users follow.
var (
	meter = otelapi.Meter(meterName)

	// http server, named after the opentelemetry semantic conventions
	httpRequestDuration = must(meter.Float64Histogram("http.server.request.duration",
		metric.WithUnit("s"),
		metric.WithDescription("duration of the http server requests"),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30)))
	httpActiveRequests = must(meter.Int64UpDownCounter("http.server.active_requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("http server requests that are being handled")))
	httpRequestSize = must(meter.Int64Histogram("http.server.request.body.size",
		metric.WithUnit("By"),
		metric.WithDescription("size of the http server request bodies"),
		metric.WithExplicitBucketBoundaries(0, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 8388608, 67108864)))
	httpResponseSize = must(meter.Int64Histogram("http.server.response.body.size",
		metric.WithUnit("By"),
		metric.WithDescription("size of the http server response bodies"),
		metric.WithExplicitBucketBoundaries(0, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 8388608, 67108864)))
	httpPanics = must(meter.Int64Counter("lavender.http.panics",
		metric.WithUnit("{panic}"),
		metric.WithDescription("panics recovered while handling a request")))
	httpUnauthorized = must(meter.Int64Counter("lavender.http.unauthorized",
		metric.WithUnit("{request}"),
		metric.WithDescription("requests of the api that were turned away for lack of a valid session")))

	// authentication
	authLogins = must(meter.Int64Counter("lavender.auth.logins",
		metric.WithUnit("{login}"),
		metric.WithDescription("login attempts by step (password or two_factor) and result")))
	authPasswordDuration = must(meter.Float64Histogram("lavender.auth.password.validation.duration",
		metric.WithUnit("s"),
		metric.WithDescription("duration of the check of a password, the slowest step of a login"),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1)))
	authRegistrations = must(meter.Int64Counter("lavender.auth.registrations",
		metric.WithUnit("{registration}"),
		metric.WithDescription("registration attempts by result")))
	authTwoFactor = must(meter.Int64Counter("lavender.auth.two_factor.verifications",
		metric.WithUnit("{verification}"),
		metric.WithDescription("two factor code verifications at login by result and kind of code")))
	authRecoveryCodes = must(meter.Int64Counter("lavender.auth.recovery_codes.used",
		metric.WithUnit("{code}"),
		metric.WithDescription("recovery codes used to log in")))
	authLogouts = must(meter.Int64Counter("lavender.auth.logouts",
		metric.WithUnit("{logout}"),
		metric.WithDescription("logouts")))

	// feed workers
	feedJobs = must(meter.Int64Counter("lavender.feed.refresh.jobs",
		metric.WithUnit("{job}"),
		metric.WithDescription("feed refresh jobs by result and site")))
	feedJobDuration = must(meter.Float64Histogram("lavender.feed.refresh.duration",
		metric.WithUnit("s"),
		metric.WithDescription("duration of the feed refresh jobs"),
		metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 16, 30)))
	feedQueueWait = must(meter.Float64Histogram("lavender.feed.queue.wait",
		metric.WithUnit("s"),
		metric.WithDescription("time a feed waited in the queue before a worker took it"),
		metric.WithExplicitBucketBoundaries(0.01, 0.1, 0.5, 1, 5, 15, 30, 60, 120, 300)))
	feedJobsInFlight = must(meter.Int64UpDownCounter("lavender.feed.refresh.in_flight",
		metric.WithUnit("{job}"),
		metric.WithDescription("feed refresh jobs that are being handled")))
	feedArticlesAdded = must(meter.Int64Counter("lavender.feed.articles.added",
		metric.WithUnit("{article}"),
		metric.WithDescription("new articles stored by the feed refreshes")))
	feedEnqueues = must(meter.Int64Counter("lavender.feed.refresh.enqueues",
		metric.WithUnit("{feed}"),
		metric.WithDescription("feeds offered to the queue by source (scheduler or user) and result (queued, duplicate or full)")))

	// outbound http calls of the feeds and the icons
	feedFetchDuration = must(meter.Float64Histogram("lavender.feed.fetch.duration",
		metric.WithUnit("s"),
		metric.WithDescription("duration of the feed downloads by result, status code and site"),
		metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8)))
	feedFetchSize = must(meter.Int64Histogram("lavender.feed.fetch.size",
		metric.WithUnit("By"),
		metric.WithDescription("bytes read of the downloaded feeds"),
		metric.WithExplicitBucketBoundaries(1024, 4096, 16384, 65536, 262144, 1048576, 4194304)))
	feedFetchItems = must(meter.Int64Histogram("lavender.feed.fetch.items",
		metric.WithUnit("{item}"),
		metric.WithDescription("articles in the downloaded feeds"),
		metric.WithExplicitBucketBoundaries(0, 1, 5, 10, 20, 50, 100, 250, 500)))
	iconLookups = must(meter.Int64Counter("lavender.icon.lookups",
		metric.WithUnit("{lookup}"),
		metric.WithDescription("icon cache lookups by result: hit, stale, miss, failed_cached, unavailable or error")))
	iconDownloadDuration = must(meter.Float64Histogram("lavender.icon.download.duration",
		metric.WithUnit("s"),
		metric.WithDescription("duration of the icon downloads by result"),
		metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8)))

	// backups and imports
	backupOperations = must(meter.Int64Counter("lavender.backup.operations",
		metric.WithUnit("{operation}"),
		metric.WithDescription("backup and OPML exports and imports by operation and result")))
	backupDuration = must(meter.Float64Histogram("lavender.backup.duration",
		metric.WithUnit("s"),
		metric.WithDescription("duration of the backup and OPML exports and imports"),
		metric.WithExplicitBucketBoundaries(0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60)))
	backupItems = must(meter.Int64Counter("lavender.backup.items",
		metric.WithUnit("{item}"),
		metric.WithDescription("what the backups and imports held, by operation and kind: folders, feeds, articles, saved or icons")))
	backupSize = must(meter.Int64Histogram("lavender.backup.size",
		metric.WithUnit("By"),
		metric.WithDescription("size of the backup files that were made or uploaded"),
		metric.WithExplicitBucketBoundaries(1024, 16384, 262144, 1048576, 8388608, 67108864, 536870912)))

	// what the users do
	userActions = must(meter.Int64Counter("lavender.actions",
		metric.WithUnit("{action}"),
		metric.WithDescription("answers to the actions of the users by action and result")))
	articleViews = must(meter.Int64Counter("lavender.articles.views",
		metric.WithUnit("{view}"),
		metric.WithDescription("articles opened in the reading panel")))
	articleListSize = must(meter.Int64Histogram("lavender.articles.list.size",
		metric.WithUnit("{article}"),
		metric.WithDescription("articles on a page of a list, by list"),
		metric.WithExplicitBucketBoundaries(0, 1, 5, 10, 20, 30)))
	savedToggles = must(meter.Int64Counter("lavender.articles.saved",
		metric.WithUnit("{toggle}"),
		metric.WithDescription("articles saved or taken back, by kind (favourite or read_later) and state")))
)

// must keeps the declarations short. An instrument only fails to be created for an invalid name, which is a
// programming error that the tests find, so the error is handed to the otel error handler and a no-op is used.
func must[T any](instrument T, err error) T {
	if err != nil {
		otelapi.Handle(err)
	}

	return instrument
}

// Result values of the metrics.
const (
	ResultSuccess = "success"
	ResultError   = "error"
)

// HTTPRequestStarted counts a request as active. The returned function must be called when the request is done.
func HTTPRequestStarted(ctx context.Context, method string) func() {
	attrs := metric.WithAttributes(attribute.String("http.request.method", method))
	httpActiveRequests.Add(ctx, 1, attrs)

	return func() { httpActiveRequests.Add(ctx, -1, attrs) }
}

// HTTPRequest is a handled http request. Route is the route pattern, not the path, as the path has too many values
// for a metric. Device is the class of the client, see middlewares.DeviceClass.
type HTTPRequest struct {
	Method        string
	Route         string
	Status        int
	RequestBytes  int64
	ResponseBytes int64
	Duration      time.Duration
	HTMX          bool
	Device        string
}

// RecordHTTPRequest records a handled http request.
func RecordHTTPRequest(ctx context.Context, r HTTPRequest) {
	attrs := metric.WithAttributes(
		attribute.String("http.request.method", r.Method),
		attribute.String("http.route", r.Route),
		attribute.Int("http.response.status_code", r.Status),
		attribute.Bool("lavender.htmx", r.HTMX),
		attribute.String("lavender.device", r.Device),
	)
	httpRequestDuration.Record(ctx, r.Duration.Seconds(), attrs)

	// the sizes are by route, a size by device or status says little
	sizeAttrs := metric.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("http.route", r.Route))
	httpRequestSize.Record(ctx, r.RequestBytes, sizeAttrs)
	httpResponseSize.Record(ctx, r.ResponseBytes, sizeAttrs)
}

// RecordHTTPPanic counts a panic that was recovered while handling a request.
func RecordHTTPPanic(ctx context.Context, route string) {
	httpPanics.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", route)))
}

// RecordUnauthorized counts a request of the api that had no valid session.
func RecordUnauthorized(ctx context.Context, route string, htmx bool) {
	httpUnauthorized.Add(ctx, 1, metric.WithAttributes(attribute.String("http.route", route), attribute.Bool("lavender.htmx", htmx)))
}

// Steps of the login.
const (
	LoginPassword  = "password"
	LoginTwoFactor = "two_factor"
)

// RecordLogin counts a login attempt. The step is password or two_factor. The result of the password step is
// success (no second step), invalid_credentials, two_factor_required, invalid_request or error, of the second step
// success or error.
func RecordLogin(ctx context.Context, step, result string) {
	authLogins.Add(ctx, 1, metric.WithAttributes(attribute.String("step", step), attribute.String("result", result)))
}

// RecordPasswordValidation records the check of a password. The result is valid or invalid.
func RecordPasswordValidation(ctx context.Context, result string, duration time.Duration) {
	authPasswordDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("result", result)))
}

// RecordRegistration counts a registration attempt. The result is success, or the reason it failed.
func RecordRegistration(ctx context.Context, result string) {
	authRegistrations.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// RecordTwoFactor counts the check of a two factor code at login. The result is success, invalid, locked, expired,
// invalid_request or error, the kind of the code app or recovery, unknown when it is not known.
func RecordTwoFactor(ctx context.Context, result, kind string) {
	authTwoFactor.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result), attribute.String("kind", kind)))
}

// RecordRecoveryCodeUsed counts a login that used a recovery code, they are single use, so the user is running out.
func RecordRecoveryCodeUsed(ctx context.Context) {
	authRecoveryCodes.Add(ctx, 1)
}

// RecordLogout counts a logout.
func RecordLogout(ctx context.Context) {
	authLogouts.Add(ctx, 1)
}

// FeedJobStarted counts a feed refresh job as in flight. The returned function must be called when it is done.
func FeedJobStarted(ctx context.Context) func() {
	feedJobsInFlight.Add(ctx, 1)

	return func() { feedJobsInFlight.Add(ctx, -1) }
}

// FeedJob is a finished feed refresh job. Result is success, fetch_error, store_error, not_found or cancelled. Host
// is the site of the feed, empty when it is not known.
type FeedJob struct {
	Result      string
	Host        string
	Duration    time.Duration
	QueueWait   time.Duration
	NewArticles int
}

// RecordFeedJob records a finished feed refresh job.
func RecordFeedJob(ctx context.Context, j FeedJob) {
	attrs := metric.WithAttributes(attribute.String("result", j.Result), attribute.String("server.address", j.Host))
	feedJobs.Add(ctx, 1, attrs)
	feedJobDuration.Record(ctx, j.Duration.Seconds(), metric.WithAttributes(attribute.String("result", j.Result)))

	if j.QueueWait > 0 {
		feedQueueWait.Record(ctx, j.QueueWait.Seconds())
	}
	if j.NewArticles > 0 {
		feedArticlesAdded.Add(ctx, int64(j.NewArticles), metric.WithAttributes(attribute.String("server.address", j.Host)))
	}
}

// Sources of a refresh.
const (
	SourceScheduler = "scheduler"
	SourceUser      = "user"
)

// RecordFeedEnqueue counts a feed that was offered to the queue. The source is scheduler or user, the result queued,
// duplicate or full.
func RecordFeedEnqueue(ctx context.Context, source, result string) {
	feedEnqueues.Add(ctx, 1, metric.WithAttributes(attribute.String("source", source), attribute.String("result", result)))
}

// FeedFetch is the download of a feed. Result is success, invalid_url, not_a_feed, bad_status, network or
// cancelled. Status is the http status, 0 when there was no answer.
type FeedFetch struct {
	Result   string
	Host     string
	Status   int
	Duration time.Duration
	Items    int
	Bytes    int64
}

// RecordFeedFetch records the download of a feed.
func RecordFeedFetch(ctx context.Context, f FeedFetch) {
	feedFetchDuration.Record(ctx, f.Duration.Seconds(), metric.WithAttributes(
		attribute.String("result", f.Result),
		attribute.Int("http.response.status_code", f.Status),
		attribute.String("server.address", f.Host),
	))

	if f.Bytes > 0 {
		feedFetchSize.Record(ctx, f.Bytes, metric.WithAttributes(attribute.String("result", f.Result)))
	}
	if f.Result == ResultSuccess {
		feedFetchItems.Record(ctx, int64(f.Items))
	}
}

// RecordIconLookup counts a lookup of the icon cache. The result is hit, stale, miss, failed_cached, unavailable or
// error.
func RecordIconLookup(ctx context.Context, result string) {
	iconLookups.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// RecordIconDownload records the download of an icon. The result is success, bad_status, network, too_big or
// not_an_image.
func RecordIconDownload(ctx context.Context, result string, duration time.Duration) {
	iconDownloadDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("result", result)))
}

// RecordBackup records a backup or OPML export or import. The operation is backup_export, backup_import,
// opml_export or opml_import.
func RecordBackup(ctx context.Context, operation string, duration time.Duration, err error) {
	attrs := metric.WithAttributes(attribute.String("operation", operation), attribute.String("result", resultOf(err)))
	backupOperations.Add(ctx, 1, attrs)
	backupDuration.Record(ctx, duration.Seconds(), attrs)
}

// RecordBackupItems counts what a backup or import held of a kind: folders, feeds, articles, saved or icons.
func RecordBackupItems(ctx context.Context, operation, kind string, n int) {
	if n > 0 {
		backupItems.Add(ctx, int64(n), metric.WithAttributes(attribute.String("operation", operation), attribute.String("kind", kind)))
	}
}

// RecordBackupSize records the size of a backup file that was made or uploaded.
func RecordBackupSize(ctx context.Context, operation string, bytes int64) {
	if bytes > 0 {
		backupSize.Record(ctx, bytes, metric.WithAttributes(attribute.String("operation", operation)))
	}
}

// RecordAction counts the answer to an action of a user, like adding a feed. The action is the route of the request,
// the result is success, error or info. The actions answer 200 whatever happens, as htmx does not swap other
// statuses, so the status of the http metrics does not show the failures.
func RecordAction(ctx context.Context, action, result string) {
	userActions.Add(ctx, 1, metric.WithAttributes(attribute.String("action", action), attribute.String("result", result)))
}

// RecordArticleView counts an article that was opened in the reading panel.
func RecordArticleView(ctx context.Context) {
	articleViews.Add(ctx, 1)
}

// RecordListPage records a page of a list of articles: the list (home, today, favourites, read-later, feed) and how
// many articles it had.
func RecordListPage(ctx context.Context, list string, articles int) {
	articleListSize.Record(ctx, int64(articles), metric.WithAttributes(attribute.String("list", list)))
}

// RecordSavedToggle counts an article that was saved or taken back. The kind is favourite or read_later.
func RecordSavedToggle(ctx context.Context, kind string, saved bool) {
	state := "removed"
	if saved {
		state = "saved"
	}

	savedToggles.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", kind), attribute.String("state", state)))
}

func resultOf(err error) string {
	if err != nil {
		return ResultError
	}

	return ResultSuccess
}

// RegisterWorkerGauges registers the gauges of the feed queue. queueDepth is read on every metrics export.
func RegisterWorkerGauges(queueDepth func() int64, workers int) error {
	depth, err := meter.Int64ObservableGauge("lavender.feed.queue.depth",
		metric.WithUnit("{feed}"),
		metric.WithDescription("feeds waiting in the queue of the workers"))
	if err != nil {
		return err
	}

	size, err := meter.Int64ObservableGauge("lavender.feed.workers",
		metric.WithUnit("{worker}"),
		metric.WithDescription("workers that fetch the feeds"))
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(depth, queueDepth())
		o.ObserveInt64(size, int64(workers))

		return nil
	}, depth, size)

	return err
}

// RegisterTotalsGauges registers gauges for the number of rows per collection of the database, read on every
// metrics export. counts returns the number of rows per name, a name it has no number for is left out.
func RegisterTotalsGauges(names []string, count func(name string) (int64, error)) error {
	totals, err := meter.Int64ObservableGauge("lavender.records",
		metric.WithUnit("{record}"),
		metric.WithDescription("records in the database by collection"))
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for _, name := range names {
			if n, err := count(name); err == nil {
				o.ObserveInt64(totals, n, metric.WithAttributes(attribute.String("collection", name)))
			}
		}

		return nil
	}, totals)

	return err
}

// RegisterRuntimeGauges registers the gauges of the go runtime and the process: goroutines, memory and the garbage
// collector. started is when the application started.
func RegisterRuntimeGauges(started time.Time) error {
	int64Gauge := func(name, unit, description string) (metric.Int64Observable, error) {
		return meter.Int64ObservableGauge(name, metric.WithUnit(unit), metric.WithDescription(description))
	}

	var (
		err         error
		goroutines  metric.Int64Observable
		gomaxprocs  metric.Int64Observable
		heapAlloc   metric.Int64Observable
		heapInuse   metric.Int64Observable
		heapIdle    metric.Int64Observable
		heapSys     metric.Int64Observable
		heapObjects metric.Int64Observable
		stackInuse  metric.Int64Observable
		sysTotal    metric.Int64Observable
		nextGC      metric.Int64Observable
	)

	for _, g := range []struct {
		target      *metric.Int64Observable
		name, unit  string
		description string
	}{
		{&goroutines, "process.runtime.go.goroutines", "{goroutine}", "goroutines that currently exist"},
		{&gomaxprocs, "process.runtime.go.gomaxprocs", "{thread}", "threads that can run go code at the same time"},
		{&heapAlloc, "process.runtime.go.mem.heap_alloc", "By", "bytes of allocated heap objects"},
		{&heapInuse, "process.runtime.go.mem.heap_inuse", "By", "bytes in in-use heap spans"},
		{&heapIdle, "process.runtime.go.mem.heap_idle", "By", "bytes in idle heap spans"},
		{&heapSys, "process.runtime.go.mem.heap_sys", "By", "bytes of heap memory obtained from the os"},
		{&heapObjects, "process.runtime.go.mem.heap_objects", "{object}", "allocated heap objects"},
		{&stackInuse, "process.runtime.go.mem.stack_inuse", "By", "bytes in stack spans"},
		{&sysTotal, "process.runtime.go.mem.sys", "By", "bytes of memory obtained from the os in total"},
		{&nextGC, "process.runtime.go.gc.next", "By", "heap size at which the next garbage collection starts"},
	} {
		if *g.target, err = int64Gauge(g.name, g.unit, g.description); err != nil {
			return err
		}
	}

	gcCount, err := meter.Int64ObservableCounter("process.runtime.go.gc.count",
		metric.WithUnit("{cycle}"), metric.WithDescription("completed garbage collection cycles"))
	if err != nil {
		return err
	}

	mallocs, err := meter.Int64ObservableCounter("process.runtime.go.mem.mallocs",
		metric.WithUnit("{object}"), metric.WithDescription("heap objects allocated, cumulative"))
	if err != nil {
		return err
	}

	frees, err := meter.Int64ObservableCounter("process.runtime.go.mem.frees",
		metric.WithUnit("{object}"), metric.WithDescription("heap objects freed, cumulative"))
	if err != nil {
		return err
	}

	allocated, err := meter.Int64ObservableCounter("process.runtime.go.mem.allocated",
		metric.WithUnit("By"), metric.WithDescription("bytes allocated for heap objects, cumulative"))
	if err != nil {
		return err
	}

	gcPause, err := meter.Float64ObservableCounter("process.runtime.go.gc.pause_total",
		metric.WithUnit("s"), metric.WithDescription("cumulative time the program was paused by the garbage collector"))
	if err != nil {
		return err
	}

	uptime, err := meter.Float64ObservableGauge("process.uptime",
		metric.WithUnit("s"), metric.WithDescription("seconds since the application started"))
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		o.ObserveInt64(goroutines, int64(runtime.NumGoroutine()))
		o.ObserveInt64(gomaxprocs, int64(runtime.GOMAXPROCS(0)))
		o.ObserveInt64(heapAlloc, int64(mem.HeapAlloc))
		o.ObserveInt64(heapInuse, int64(mem.HeapInuse))
		o.ObserveInt64(heapIdle, int64(mem.HeapIdle))
		o.ObserveInt64(heapSys, int64(mem.HeapSys))
		o.ObserveInt64(heapObjects, int64(mem.HeapObjects))
		o.ObserveInt64(stackInuse, int64(mem.StackInuse))
		o.ObserveInt64(sysTotal, int64(mem.Sys))
		o.ObserveInt64(nextGC, int64(mem.NextGC))
		o.ObserveInt64(gcCount, int64(mem.NumGC))
		o.ObserveInt64(mallocs, int64(mem.Mallocs))
		o.ObserveInt64(frees, int64(mem.Frees))
		o.ObserveInt64(allocated, int64(mem.TotalAlloc))
		o.ObserveFloat64(gcPause, time.Duration(mem.PauseTotalNs).Seconds())
		o.ObserveFloat64(uptime, time.Since(started).Seconds())

		return nil
	},
		goroutines, gomaxprocs, heapAlloc, heapInuse, heapIdle, heapSys, heapObjects, stackInuse, sysTotal, nextGC,
		gcCount, mallocs, frees, allocated, gcPause, uptime)

	return err
}

// RegisterBuildInfo registers the gauge that tells which build runs: its value is always 1, the version is in its
// attributes, so it can be graphed next to the other metrics and shows a deploy.
func RegisterBuildInfo(version, revision, goVersion string) error {
	info, err := meter.Int64ObservableGauge("lavender.build.info",
		metric.WithUnit("{build}"), metric.WithDescription("the build that runs, always 1, see the attributes"))
	if err != nil {
		return err
	}

	attrs := metric.WithAttributes(
		attribute.String("version", version),
		attribute.String("revision", revision),
		attribute.String("go_version", goVersion),
	)

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(info, 1, attrs)

		return nil
	}, info)

	return err
}

// FeedStates are the numbers of feeds by state, as stored. Total counts every feed, the others are the ones in that
// state. A feed that is paused is not refreshed, one with an error failed its last refresh, one that is waiting was
// never fetched.
type FeedStates struct {
	Total, Active, Paused, Errored, Waiting int64
	// OldestFetchAge is how long ago the active feed that was fetched longest ago was fetched, zero when there is none
	OldestFetchAge time.Duration
}

// RegisterFeedGauges registers the gauges of the state of the feeds. states is read on every metrics export.
func RegisterFeedGauges(states func() (FeedStates, error)) error {
	feeds, err := meter.Int64ObservableGauge("lavender.feeds",
		metric.WithUnit("{feed}"), metric.WithDescription("feeds by state: total, active, paused, errored (the last refresh failed) or waiting (never fetched)"))
	if err != nil {
		return err
	}

	oldest, err := meter.Float64ObservableGauge("lavender.feeds.oldest_fetch_age",
		metric.WithUnit("s"), metric.WithDescription("how long ago the active feed that was fetched longest ago was fetched"))
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s, err := states()
		if err != nil {
			return nil //nolint:nilerr // a failed read leaves the gauges out of this export, the next one tries again
		}

		for state, n := range map[string]int64{"total": s.Total, "active": s.Active, "paused": s.Paused, "errored": s.Errored, "waiting": s.Waiting} {
			o.ObserveInt64(feeds, n, metric.WithAttributes(attribute.String("state", state)))
		}
		o.ObserveFloat64(oldest, s.OldestFetchAge.Seconds())

		return nil
	}, feeds, oldest)

	return err
}

// Storage is what the application keeps on disk.
type Storage struct {
	// Files are the sizes in bytes of the files of the database by name
	Files map[string]int64
	// Icons is the number of icons in the icon cache, IconBytes the room they take
	Icons     int64
	IconBytes int64
}

// RegisterStorageGauges registers the gauges of the disk use: the files of the database and the icon cache. storage
// is read on every metrics export.
func RegisterStorageGauges(storage func() (Storage, error)) error {
	files, err := meter.Int64ObservableGauge("lavender.db.file.size",
		metric.WithUnit("By"), metric.WithDescription("size of the files of the database by name, the wal grows between checkpoints"))
	if err != nil {
		return err
	}

	icons, err := meter.Int64ObservableGauge("lavender.icon.cache.icons",
		metric.WithUnit("{icon}"), metric.WithDescription("icons in the icon cache"))
	if err != nil {
		return err
	}

	iconBytes, err := meter.Int64ObservableGauge("lavender.icon.cache.size",
		metric.WithUnit("By"), metric.WithDescription("room the icon cache takes on disk"))
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s, err := storage()
		if err != nil {
			return nil //nolint:nilerr // a failed read leaves the gauges out of this export, the next one tries again
		}

		for name, size := range s.Files {
			o.ObserveInt64(files, size, metric.WithAttributes(attribute.String("file", name)))
		}
		o.ObserveInt64(icons, s.Icons)
		o.ObserveInt64(iconBytes, s.IconBytes)

		return nil
	}, files, icons, iconBytes)

	return err
}

// InitCounters creates the series of the counters whose labels have a fixed set of values, at zero. A counter only
// exists once something is counted, so without this the failures that did not happen yet are missing, and a ratio
// or an alert that uses them has nothing to calculate with. Call it once at startup.
func InitCounters(ctx context.Context) {
	for _, result := range []string{ResultSuccess, "invalid_credentials", "two_factor_required", "invalid_request", ResultError} {
		authLogins.Add(ctx, 0, metric.WithAttributes(attribute.String("step", LoginPassword), attribute.String("result", result)))
	}
	for _, result := range []string{ResultSuccess, ResultError} {
		authLogins.Add(ctx, 0, metric.WithAttributes(attribute.String("step", LoginTwoFactor), attribute.String("result", result)))
	}
	for _, result := range []string{ResultSuccess, "email_taken", "weak_password", "invalid_email", "invalid_request", ResultError} {
		authRegistrations.Add(ctx, 0, metric.WithAttributes(attribute.String("result", result)))
	}
	for _, result := range []string{ResultSuccess, "invalid", "locked", "expired", "invalid_request", ResultError} {
		authTwoFactor.Add(ctx, 0, metric.WithAttributes(attribute.String("result", result), attribute.String("kind", "unknown")))
	}
	authRecoveryCodes.Add(ctx, 0)
	authLogouts.Add(ctx, 0)
	httpPanics.Add(ctx, 0, metric.WithAttributes(attribute.String("http.route", unmatchedRoute)))

	for _, source := range []string{SourceScheduler, SourceUser} {
		for _, result := range []string{"queued", "duplicate", "full"} {
			feedEnqueues.Add(ctx, 0, metric.WithAttributes(attribute.String("source", source), attribute.String("result", result)))
		}
	}
	for _, result := range []string{"hit", "stale", "miss", "failed_cached", "unavailable", ResultError} {
		iconLookups.Add(ctx, 0, metric.WithAttributes(attribute.String("result", result)))
	}
	for _, operation := range []string{"backup_export", "backup_import", "opml_export", "opml_import"} {
		for _, result := range []string{ResultSuccess, ResultError} {
			backupOperations.Add(ctx, 0, metric.WithAttributes(attribute.String("operation", operation), attribute.String("result", result)))
		}
	}
	for _, kind := range []string{"favourite", "read_later"} {
		for _, state := range []string{"saved", "removed"} {
			savedToggles.Add(ctx, 0, metric.WithAttributes(attribute.String("kind", kind), attribute.String("state", state)))
		}
	}
	articleViews.Add(ctx, 0)
}

// unmatchedRoute is the route label of the requests that no route handles, see middlewares.Metrics.
const unmatchedRoute = "unmatched"
