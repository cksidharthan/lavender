package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Run("app defaults", func(t *testing.T) {
		config, err := New()
		require.Nil(t, err)

		expectedConfig := &Config{
			HTTPServerPort:  8080,
			LogLevel:        "info",
			Environment:     "local",
			OTELEnabled:     false,
			OTELServiceName: "lavender",
			OTELHost:        "localhost",
			OTELPort:        4317,
		}

		require.Equal(t, expectedConfig, config, "expectedConfig is not equal to actual config returned by New function")
	})

	t.Run("set env variables to change the defaults", func(t *testing.T) {
		assert.NoError(t, os.Setenv("HTTP_SERVER_PORT", "8081"))
		assert.NoError(t, os.Setenv("OTEL_ENABLED", "true"))

		config, err := New()
		require.Nil(t, err)

		expectedConfig := &Config{
			HTTPServerPort:  8081,
			LogLevel:        "info",
			Environment:     "local",
			OTELEnabled:     true,
			OTELServiceName: "lavender",
			OTELHost:        "localhost",
			OTELPort:        4317,
		}

		require.Equal(t, expectedConfig, config, "expectedConfig is not equal to the actual config returned by New function")
	})

	t.Run("set invalid values in env variables", func(t *testing.T) {
		assert.NoError(t, os.Setenv("OTEL_ENABLED", "invalid-value"))

		config, err := New()
		assert.Nil(t, config)
		require.Error(t, err)
	})
}
