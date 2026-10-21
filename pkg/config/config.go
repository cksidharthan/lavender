package config

import (
	"errors"
	"fmt"
	env "github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"io/fs"
)

type Config struct {
	// HTTP Server configuration
	HTTPServerPort int64  `env:"HTTP_SERVER_PORT" envDefault:"8080"`
	LogLevel       string `env:"LOG_LEVEL" envDefault:"info"`
	Environment    string `env:"ENVIRONMENT" envDefault:"local"`

	// OTEL Config
	OTELEnabled     bool   `env:"OTEL_ENABLED" envDefault:"false"`
	OTELServiceName string `env:"OTEL_SERVICE_NAME" envDefault:"lavender"`
	OTELHost        string `env:"OTEL_HOST" envDefault:"localhost"`
	OTELPort        int64  `env:"OTEL_PORT" envDefault:"4317"`
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
