package logger

import (
	"context"
	"log/slog"
	"sync"
)

type ctxAttrsKey struct{}

// ctxAttrs are the attributes that every log record of a request gets: the id of the request, the user. They are
// filled in while the request is handled, the user is not known when it starts.
type ctxAttrs struct {
	mu    sync.Mutex
	attrs []slog.Attr
}

// NewContext returns a context that collects attributes for the records logged with it, see AddToContext.
func NewContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxAttrsKey{}, &ctxAttrs{})
}

// AddToContext adds attributes to every record that is logged with ctx from now on. An attribute with a key that is
// there already replaces it. It does nothing when ctx is not made by NewContext.
func AddToContext(ctx context.Context, attrs ...slog.Attr) {
	holder, ok := ctx.Value(ctxAttrsKey{}).(*ctxAttrs)
	if !ok {
		return
	}

	holder.mu.Lock()
	defer holder.mu.Unlock()

	for _, attr := range attrs {
		replaced := false
		for i := range holder.attrs {
			if holder.attrs[i].Key == attr.Key {
				holder.attrs[i], replaced = attr, true
			}
		}
		if !replaced {
			holder.attrs = append(holder.attrs, attr)
		}
	}
}

// ctxHandler adds the attributes of the context to the records, unless the record has an attribute with that key
// already: a call that names the user itself is not logged twice.
type ctxHandler struct {
	slog.Handler
}

func (h ctxHandler) Handle(ctx context.Context, record slog.Record) error {
	if holder, ok := ctx.Value(ctxAttrsKey{}).(*ctxAttrs); ok {
		holder.mu.Lock()
		attrs := append([]slog.Attr(nil), holder.attrs...)
		holder.mu.Unlock()

		present := map[string]struct{}{}
		record.Attrs(func(a slog.Attr) bool {
			present[a.Key] = struct{}{}
			return true
		})

		for _, attr := range attrs {
			if _, ok := present[attr.Key]; !ok {
				record.AddAttrs(attr)
			}
		}
	}

	return h.Handler.Handle(ctx, record)
}

func (h ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithGroup(name)}
}
