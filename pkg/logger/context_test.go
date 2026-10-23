package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextAttributes(t *testing.T) {
	newLogger := func(buf *bytes.Buffer) *slog.Logger {
		return slog.New(ctxHandler{Handler: slog.NewJSONHandler(buf, nil)})
	}
	entry := func(buf *bytes.Buffer) map[string]any {
		var e map[string]any
		require.NoError(t, json.Unmarshal(buf.Bytes(), &e))

		return e
	}

	t.Run("adds the attributes of the context to the records", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := NewContext(context.Background())
		AddToContext(ctx, slog.String(RequestIDKey, "req-1"))
		AddToContext(ctx, slog.String(UserIDKey, "user-1"))

		newLogger(&buf).InfoContext(ctx, "hello")

		assert.Equal(t, "req-1", entry(&buf)[RequestIDKey])
		assert.Equal(t, "user-1", entry(&buf)[UserIDKey])
	})

	t.Run("an attribute that is added later shows on the records after it", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := NewContext(context.Background())
		logger := newLogger(&buf)

		logger.InfoContext(ctx, "before")
		assert.NotContains(t, entry(&buf), UserIDKey)

		buf.Reset()
		AddToContext(ctx, slog.String(UserIDKey, "user-1"))
		logger.InfoContext(ctx, "after")
		assert.Equal(t, "user-1", entry(&buf)[UserIDKey])
	})

	t.Run("a key that is added again replaces the value", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := NewContext(context.Background())
		AddToContext(ctx, slog.String(UserIDKey, "old"))
		AddToContext(ctx, slog.String(UserIDKey, "new"))

		newLogger(&buf).InfoContext(ctx, "hello")

		assert.Equal(t, "new", entry(&buf)[UserIDKey])
	})

	t.Run("the attributes of the record itself win and are not logged twice", func(t *testing.T) {
		var buf bytes.Buffer
		ctx := NewContext(context.Background())
		AddToContext(ctx, slog.String(UserIDKey, "from-context"))

		newLogger(&buf).InfoContext(ctx, "hello", slog.String(UserIDKey, "explicit"))

		assert.Equal(t, "explicit", entry(&buf)[UserIDKey])
		assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte(`"`+UserIDKey+`"`)))
	})

	t.Run("a context without holder is left alone", func(t *testing.T) {
		var buf bytes.Buffer
		AddToContext(context.Background(), slog.String(UserIDKey, "x")) // must not panic

		newLogger(&buf).InfoContext(context.Background(), "hello")

		assert.NotContains(t, entry(&buf), UserIDKey)
	})
}
