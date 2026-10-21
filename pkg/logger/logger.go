// Package logger has the logging configuration
package logger

import (
	"log/slog"
	"os"
	"strings"

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

	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})

	logger := slog.New(
		slog.NewMultiHandler(
			otelHandler,
			jsonHandler,
		),
	)
	slog.SetDefault(logger)

	logger.Info("logger initialized", slog.String("level", cfg.LogLevel), slog.String("environment", cfg.Environment))

	return logger
}
