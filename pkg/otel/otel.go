package otel

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cksidharthan/lavender/pkg/buildinfo"
	"github.com/cksidharthan/lavender/pkg/config"
	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.28.0"
)

// export limits keep an unreachable collector from blocking flushes (and so
// application shutdown) for the SDK default of up to a minute.
const (
	exportTimeout        = 3 * time.Second
	retryInitialInterval = 500 * time.Millisecond
	retryMaxInterval     = time.Second
)

type LavenderOTEL struct {
	TraceProvider   *sdktrace.TracerProvider
	MetricsProvider *metric.MeterProvider
	LoggerProvider  *sdklog.LoggerProvider
}

func New(logger *slog.Logger, cfg *config.Config) (*LavenderOTEL, error) {
	customResource, err := resource.New(
		context.Background(),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		// the process detectors without WithProcessOwner: it looks up the current user, which fails in the scratch
		// image (no cgo, no $USER, no passwd file) and would keep the application from starting.
		resource.WithProcessPID(),
		resource.WithProcessExecutableName(),
		resource.WithProcessExecutablePath(),
		resource.WithProcessCommandArgs(),
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
		resource.WithProcessRuntimeDescription(),
		resource.WithOS(),
		resource.WithContainer(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create custom resource: %w", err)
	}

	resource, err := resource.Merge(
		resource.NewWithAttributes(
			"",
			semconv.ServiceNameKey.String(cfg.OTELServiceName),
			semconv.ServiceVersionKey.String(buildinfo.Version),
			attribute.String("deployment.environment.name", cfg.Environment),
			attribute.String("environment", cfg.Environment),
			attribute.String("vcs.revision", buildinfo.Revision()),
		),
		customResource,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	ctx := context.Background()
	endpoint := fmt.Sprintf("%s:%d", cfg.OTELHost, cfg.OTELPort)

	// Trace provider: export to the OTLP collector when enabled, otherwise fall
	// back to the slog exporter so spans are still visible during local dev.
	var traceProvider *sdktrace.TracerProvider
	if cfg.OTELEnabled {
		traceExporter, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithInsecure(),
			otlptracegrpc.WithTimeout(exportTimeout),
			otlptracegrpc.WithRetry(otlptracegrpc.RetryConfig{
				Enabled:         true,
				InitialInterval: retryInitialInterval,
				MaxInterval:     retryMaxInterval,
				MaxElapsedTime:  exportTimeout,
			}),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
		}

		traceProvider = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(resource),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		)
	} else {
		traceProvider = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(&slogSpanExporter{
				logger: logger.With(slog.String(lavenderLogger.ModuleKey, "otel")),
			}),
			sdktrace.WithResource(resource),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		)
	}

	// Meter provider: a provider without a reader exports nothing, so only wire
	// up the periodic OTLP reader when telemetry is enabled.
	meterOpts := []metric.Option{metric.WithResource(resource)}
	if cfg.OTELEnabled {
		metricExporter, err := otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithEndpoint(endpoint),
			otlpmetricgrpc.WithInsecure(),
			otlpmetricgrpc.WithTimeout(exportTimeout),
			otlpmetricgrpc.WithRetry(otlpmetricgrpc.RetryConfig{
				Enabled:         true,
				InitialInterval: retryInitialInterval,
				MaxInterval:     retryMaxInterval,
				MaxElapsedTime:  exportTimeout,
			}),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
		}

		meterOpts = append(meterOpts, metric.WithReader(
			metric.NewPeriodicReader(
				metricExporter,
				// at what intervals metrics has to be pushed to the otel collector.
				metric.WithInterval(time.Duration(cfg.MetricsUploadIntervalSecs)*time.Second),
			),
		))
	}
	meterProvider := metric.NewMeterProvider(meterOpts...)

	// Logger provider: a provider without a processor drops every record, so
	// only attach the batch OTLP processor when telemetry is enabled.
	logOpts := []sdklog.LoggerProviderOption{sdklog.WithResource(resource)}
	if cfg.OTELEnabled {
		logExporter, err := otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(endpoint),
			otlploggrpc.WithInsecure(),
			otlploggrpc.WithTimeout(exportTimeout),
			otlploggrpc.WithRetry(otlploggrpc.RetryConfig{
				Enabled:         true,
				InitialInterval: retryInitialInterval,
				MaxInterval:     retryMaxInterval,
				MaxElapsedTime:  exportTimeout,
			}),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP log exporter: %w", err)
		}

		logOpts = append(logOpts, sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)))
	}
	loggerProvider := sdklog.NewLoggerProvider(logOpts...)

	otel.SetTracerProvider(traceProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetLoggerProvider(loggerProvider)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	logger.Info("openTelemetry configured",
		slog.String("service_name", cfg.OTELServiceName),
		slog.Bool("otlp_export_enabled", cfg.OTELEnabled),
		slog.String("otlp_endpoint", endpoint),
	)

	return &LavenderOTEL{
		TraceProvider:   traceProvider,
		MetricsProvider: meterProvider,
		LoggerProvider:  loggerProvider,
	}, nil
}

type slogSpanExporter struct {
	logger *slog.Logger
}

func (e *slogSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	for _, span := range spans {
		e.logger.DebugContext(ctx, "otel span completed",
			slog.String("name", span.Name()),
			slog.String("trace_id", span.SpanContext().TraceID().String()),
			slog.String("span_id", span.SpanContext().SpanID().String()),
			slog.Duration("duration", span.EndTime().Sub(span.StartTime())),
			slog.Int("status_code", int(span.Status().Code)),
		)
	}

	return nil
}

func (e *slogSpanExporter) Shutdown(context.Context) error {
	return nil
}
