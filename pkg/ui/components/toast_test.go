package components

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()

	var buf bytes.Buffer
	require.NoError(t, c.Render(context.Background(), &buf))

	return buf.String()
}

func TestToast(t *testing.T) {
	t.Run("auto dismisses after 3 seconds by default", func(t *testing.T) {
		out := render(t, Toast(ToastProps{Message: "Saved"}))
		assert.Contains(t, out, "animation-duration: 3000ms")
		assert.Contains(t, out, "Saved")
		assert.Contains(t, out, `aria-label="Dismiss"`)
	})

	t.Run("duration is configurable", func(t *testing.T) {
		out := render(t, Toast(ToastProps{Message: "Saved", Duration: 5 * time.Second}))
		assert.Contains(t, out, "animation-duration: 5000ms")
	})

	t.Run("negative duration keeps the toast without a progress bar", func(t *testing.T) {
		out := render(t, Toast(ToastProps{Message: "Saved", Duration: -1}))
		assert.NotContains(t, out, "animation-duration")
		assert.Contains(t, out, `aria-label="Dismiss"`)
	})

	t.Run("errors use the alert role", func(t *testing.T) {
		assert.Contains(t, render(t, Toast(ToastProps{Variant: ToastError, Message: "x"})), `role="alert"`)
		assert.Contains(t, render(t, Toast(ToastProps{Variant: ToastSuccess, Message: "x"})), `role="status"`)
	})

	t.Run("escapes the message", func(t *testing.T) {
		out := render(t, Toast(ToastProps{Message: "<script>alert(1)</script>"}))
		assert.NotContains(t, out, "<script>")
	})

	t.Run("oob variant targets the toast container", func(t *testing.T) {
		out := render(t, ToastOOB(ToastProps{Message: "Saved"}))
		assert.Contains(t, out, `hx-swap-oob="beforeend:#toasts"`)
	})
}
