# Monitoring

Grafana dashboards for the metrics Lavender exports through OpenTelemetry (OTLP).

| Dashboard | What it shows |
| --- | --- |
| `lavender-overview` | Request rate, 5xx ratio, latency percentiles, slowest and busiest routes, response sizes, panics and the Go runtime. |
| `lavender-feeds` | The feed workers: feeds by state, queue depth and time in the queue, jobs by result, durations, new articles, feed downloads by status and site, and the icon cache. |
| `lavender-users` | Logins, registrations, two-factor checks, what users do and which actions fail, reading and saving, backups and imports, and the size of the data and the database files. |

## Requirements

The dashboards query **Prometheus** and use the metric names Prometheus makes of the OTLP names: dots become
underscores, the unit is added (`_seconds`, `_bytes`), and counters end in `_total`. For example
`http.server.request.duration` is `http_server_request_duration_seconds_bucket` and `lavender.auth.logins` is
`lavender_auth_logins_total`.

Send the metrics to Prometheus in one of two ways:

- Prometheus with `--web.enable-otlp-receiver`, and the OpenTelemetry collector (or Lavender) sending OTLP/HTTP to
  `http://prometheus:9090/api/v1/otlp/v1/metrics`.
- The collector's `prometheus` exporter, scraped by Prometheus. If you do this, set `resource_to_telemetry_conversion`
  as you like; the dashboards do not filter on any resource label.

Lavender itself exports OTLP over gRPC to `OTEL_HOST:OTEL_PORT` when `OTEL_ENABLED=true`, so the collector sits in
between. `METRICS_UPLOAD_INTERVAL_SECS` sets how often the metrics are pushed, keep it at or below your scrape interval
for smooth graphs.

## Use

**With `deploy/docker-compose.yaml` (grafana/otel-lgtm):** nothing to do. The compose file mounts
`grafana/dashboards` and the provider file `grafana/provisioning/dashboards/lavender.yaml` into the `otel-lgtm`
container, so `docker compose up` loads the dashboards into the folder *Lavender*. Grafana rescans the folder every 30
seconds, so an edited dashboard shows up without a restart. The data source of the image (uid `prometheus`) is used,
so the data source file of this folder is not needed there.

**Your own Grafana:** mount the two folders into Grafana.

```yaml
grafana:
  image: grafana/grafana:latest
  volumes:
    - ./monitoring/grafana/dashboards:/otel-lgtm/lavender-dashboards:ro
    - ./monitoring/grafana/provisioning:/etc/grafana/provisioning:ro
```

(The provider file reads the dashboards from `/otel-lgtm/lavender-dashboards`, mount them there or change `path`.)
The data source file points at `http://prometheus:9090`, change it when yours is elsewhere. The dashboards pick the
data source from the **Data source** variable at the top, so they also work with a data source that was not provisioned.

**By hand:** in Grafana choose *Dashboards → New → Import* and upload the files of `grafana/dashboards`.

## Labels

The labels have a small, fixed set of values, so the metrics stay cheap. The one exception is `server_address`, the
host of a feed, which has a value for every site the users follow; it is only on `lavender_feed_refresh_jobs_total`,
`lavender_feed_articles_added_total` and `lavender_feed_fetch_duration_seconds`, and is what the "sites" tables use.
Drop it in the collector if you host Lavender for many users.

| Label | Values |
| --- | --- |
| `http_route` | the route pattern, `unmatched` for what no route handles |
| `lavender_device` | `mobile`, `tablet`, `desktop`, `bot`, `unknown` (from the user agent) |
| `lavender_htmx` | `true` for the requests the page makes, `false` for page loads |
| `step`, `result` of the logins | `password` / `two_factor`; `success`, `invalid_credentials`, `two_factor_required`, `invalid_request`, `error` |
| `source` of the queue | `scheduler`, `user` |
| `state` of `lavender_feeds` | `total`, `active`, `paused`, `errored`, `waiting` |

Counters with a fixed set of labels are created at zero when the application starts, so a failure that has not happened
yet is `0` and not missing. Every log line of a request has `request_id`, `user_id` and `trace_id`, the `request_id`
is also sent back in the `X-Request-Id` header, and the logs have `service`, `environment`, `version` and `revision`.
Set the version of a build with `--build-arg VERSION=1.2.3` (compose: `VERSION=1.2.3 docker compose build`).

## What to watch

- **5xx error ratio** above 1% and any **panics** need a look at the logs, the `trace_id` of the log line opens the trace.
- Actions that fail answer `200`, as htmx does not swap other statuses. The *Failing actions by route* panel of
  `lavender-users` is where they show.
- **Queue depth** that stays high, or **queue full**, means the workers cannot keep up: raise `FEED_WORKERS`.
- A high **refresh failure ratio** is usually feeds that are gone, see *Downloads by result* for the reason.
- A jump in **failed logins** or **2FA lockouts** can be someone guessing passwords.
- **Uptime** that resets often means the application restarts.

Metric names are defined in `pkg/otel/metrics.go`. When one is added or renamed there, update the dashboards.
