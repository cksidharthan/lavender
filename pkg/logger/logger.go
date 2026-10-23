// Package logger has the logging configuration
package logger

import (
	"log/slog"
	"os"
	"strings"

	"github.com/cksidharthan/lavender/pkg/buildinfo"
	"github.com/cksidharthan/lavender/pkg/config"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
)

func New(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case strings.ToLower(slog.LevelDebug.String()):
		level = slog.LevelDebug
	case strings.ToLower(slog.LevelInfo.String()):
		level = slog.LevelInfo
	case strings.ToLower(slog.LevelWarn.String()):
		level = slog.LevelWarn
	case strings.ToLower(slog.LevelError.String()):
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	otelHandler := otelslog.NewHandler(
		cfg.OTELServiceName,
		otelslog.WithSource(true),
		otelslog.WithSchemaURL("https://opentelemetry.io/schemas/1.34.0"),
		otelslog.WithLoggerProvider(otel.GetLoggerProvider()),
	)

	// what identifies the build on every line of stdout. The otel handler has it as resource attributes.
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}).WithAttrs([]slog.Attr{
		slog.String("service", cfg.OTELServiceName),
		slog.String("environment", cfg.Environment),
		slog.String("version", buildinfo.Version),
		slog.String("revision", buildinfo.Revision()),
	})

	// the context attributes (request id, user) go to both, the trace ids are added to stdout only as the otel
	// handler sets them itself
	logger := slog.New(ctxHandler{Handler: slog.NewMultiHandler(
		otelHandler,
		traceHandler{Handler: jsonHandler},
	)})
	slog.SetDefault(logger)

	logger.Info("logger initialized", slog.String("level", cfg.LogLevel))

	return logger
}
