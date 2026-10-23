# syntax=docker/dockerfile:1

FROM golang:1.27 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# the version that is reported in the logs and the metrics, set with --build-arg VERSION=1.2.3
ARG VERSION=dev

# pocketbase uses the pure-go modernc sqlite driver, so CGO can stay disabled
# and the binary is fully static.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/cksidharthan/lavender/pkg/buildinfo.Version=${VERSION}" -o /out/lavender . \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck \
    && mkdir -p /out/data/pb_data /out/tmp

FROM alpine:3.22 AS certs
RUN apk add --no-cache ca-certificates

FROM scratch

# CA certificates for outbound TLS (e.g. the OTEL collector)
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/lavender /lavender
# the container health check, scratch has no curl
COPY --from=builder /out/healthcheck /healthcheck
# writable dirs for the pocketbase data and temp files, owned by nobody
COPY --from=builder --chown=65534:65534 /out/data /data
COPY --from=builder --chown=65534:65534 /out/tmp /tmp

ENV POCKETBASE_DATA_DIR=/data/pb_data
ENV ICON_CACHE_DIR=/data/icons

USER 65534:65534

EXPOSE 8080

VOLUME ["/data"]

ENTRYPOINT ["/lavender"]
