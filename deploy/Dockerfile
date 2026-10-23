# syntax=docker/dockerfile:1

FROM golang:1.27 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# pocketbase uses the pure-go modernc sqlite driver, so CGO can stay disabled
# and the binary is fully static.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lavender . \
    && mkdir -p /out/data/pb_data /out/tmp

# alpine ships a fully static curl build (multi-arch), which scratch can run
FROM alpine:3.22 AS curl
RUN apk add --no-cache curl-static ca-certificates

FROM scratch

# CA certificates for outbound TLS (e.g. the OTEL collector)
COPY --from=curl /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=curl /usr/bin/curl.static /usr/bin/curl
COPY --from=builder /out/lavender /lavender
# writable dirs for the pocketbase data and temp files, owned by nobody
COPY --from=builder --chown=65534:65534 /out/data /data
COPY --from=builder --chown=65534:65534 /out/tmp /tmp

ENV POCKETBASE_DATA_DIR=/data/pb_data
ENV ICON_CACHE_DIR=/data/icons

USER 65534:65534

EXPOSE 8080

VOLUME ["/data"]

ENTRYPOINT ["/lavender"]
