# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags "-X github.com/mapherez/nox-gateway/internal/buildinfo.Version=${VERSION}" -o /out/nox-gateway ./cmd/nox-gateway
FROM alpine:3.23
RUN addgroup -g 10001 noxsuite \
 && adduser -D -H -u 10002 -G noxsuite noxgateway \
 && mkdir -p /data /run/nox-suite \
 && chown 10002:10001 /data \
 && chmod 0700 /data \
 && chown 10001:10001 /run/nox-suite \
 && chmod 3770 /run/nox-suite
COPY --from=build /out/nox-gateway /usr/local/bin/nox-gateway
ENV NOX_GATEWAY_LISTEN_ADDR=0.0.0.0:8080 NOX_GATEWAY_DATA_DIR=/data NOX_GATEWAY_SUITE_SOCKET_DIR=/run/nox-suite
USER noxgateway
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/nox-gateway", "--healthcheck"]
ENTRYPOINT ["/usr/local/bin/nox-gateway"]
