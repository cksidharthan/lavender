package config

import (
	"errors"
	"fmt"
	"io/fs"

	env "github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	// HTTP Server configuration
	HTTPServerPort      int64  `env:"HTTP_SERVER_PORT" envDefault:"8080"`
	ShutdownWaitSeconds int    `env:"SHUTDOWN_WAIT_SECONDS" envDefault:"30"`
	LogLevel            string `env:"LOG_LEVEL" envDefault:"info"`
	Environment         string `env:"ENVIRONMENT" envDefault:"local"`

	// OTEL Config
	OTELEnabled               bool   `env:"OTEL_ENABLED" envDefault:"false" json:"otel_enabled"`
	OTELServiceName           string `env:"OTEL_SERVICE_NAME" envDefault:"lavender" json:"otel_service_name"`
	OTELHost                  string `env:"OTEL_HOST" envDefault:"localhost" json:"otel_host"`
	OTELPort                  int64  `env:"OTEL_PORT" envDefault:"4317" json:"otel_port"`
	MetricsUploadIntervalSecs int    `env:"METRICS_UPLOAD_INTERVAL_SECS" envDefault:"10" json:"metrics_upload_interval_secs"`

	// Pocketbase config
	PocketBaseDataDir       string `env:"POCKETBASE_DATA_DIR" envDefault:"./data/pb_data"`
	PocketBaseAdminDisabled bool   `env:"POCKETBASE_ADMIN_DISABLED" envDefault:"true"`
	PocketBaseAdminPort     int64  `env:"POCKETBASE_ADMIN_PORT" envDefault:"8090"`
}

func New() (*Config, error) {
	err := godotenv.Load(".env")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("failed to load .env file: %w", err)
	}

	var config Config
	if err = env.Parse(&config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &config, nil
}
