package otel

import (
	"context"
	"net/http"
	"runtime"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "taalmaat"

func GetTraceID(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return ""
	}

	return span.SpanContext().TraceID().String()
}

func StartNewSpan(ctx context.Context) (context.Context, trace.Span) {
	pc, _, _, ok := runtime.Caller(1)
	if !ok {
		return StartNewSpanWithName(ctx, "unknown")
	}

	funcName := runtime.FuncForPC(pc).Name()
	if idx := strings.LastIndex(funcName, "/"); idx >= 0 {
		funcName = funcName[idx+1:]
	}

	return StartNewSpanWithName(ctx, funcName)
}

func StartNewSpanWithName(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name)
}

func SetHTTPHeaders(ctx context.Context, req *http.Request) {
	if req == nil {
		return
	}

	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return
	}

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}

func SetTag(ctx context.Context, key, value string) {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return
	}

	span.SetAttributes(attribute.String(key, value))
}

func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}

	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func EndSpan(ctx context.Context) {
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().IsValid() {
		span.End()
	}
}
