# syntax=docker/dockerfile:1
# Dockerfile — §8.1 «Сборка образа»: three stages, §10 День 5.
#
# The run stage is `alpine` + `ca-certificates`, NOT `scratch`: without a CA store Go cannot verify
# TLS to api.telegram.org, and the bot would fail its Telegram requests silently (§8.1).
# The test stage is the only place `-race` exists on this host: there is no C compiler on Windows
# (§3.1), so CGO_ENABLED=1 lives here, in `apk add gcc musl-dev`.

FROM golang:1.27-alpine AS builder
WORKDIR /src
# go.mod/go.sum first: the module cache layer survives source edits and rebuilds skip the download.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# §8.1: a static binary, no cgo, so the run stage needs nothing but the CA store.
ENV CGO_ENABLED=0 GOOS=linux
ARG BUILD_COMMIT=dev
RUN go build -trimpath -ldflags="-s -w -X main.buildCommit=${BUILD_COMMIT}" -o /bot .

FROM alpine:3.21 AS run
# tzdata: created_at is stored in UTC (§5) and the logs of §12 name the timezone of the container.
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bot /bot
# §8.1: nobody (uid 65534 — alpine ships it already, adduser would fail with «user 'nobody' in use»),
# not root; /data is the named volume bot-data of compose (§8.1 forbids a Windows bind-mount there:
# 9p/virtiofs + WAL risks corrupting the database). The chown matters because a named volume is
# created root-owned on the first `up`, and store.Open of §5 must be able to write bot.db in it.
RUN mkdir -p /data && chown -R 65534:65534 /data
USER 65534:65534
ENV DB_PATH=/data/bot.db
EXPOSE 8080
# §8.1 HEALTHCHECK: curl is not in the image, wget is part of alpine's busybox. The shell form (not the
# JSON form) is deliberate: /bin/sh expands HEALTH_PORT, so a container started with another §7 port is
# still probed on the port it actually listens on instead of being marked unhealthy at 8080.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --start-interval=1s \
  CMD wget -qO- "http://127.0.0.1:${HEALTH_PORT:-8080}/healthz" || exit 1
ENTRYPOINT ["/bot"]

FROM golang:1.27-alpine AS test
# The §3.1 resolution: -race needs cgo, cgo needs a toolchain, the toolchain is inside the image.
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# §9 scripts/lint + scripts/test-race in one image: gofmt must list nothing, vet must be clean, then
# the race detector over every package. gofmt -l exits 0 even when it lists files, hence the -s test.
CMD ["sh", "-c", "set -e; gofmt -l . > /tmp/fmt.txt; if [ -s /tmp/fmt.txt ]; then cat /tmp/fmt.txt; echo 'gofmt -l listed files above (§16 lint)'; exit 1; fi; go vet ./...; CGO_ENABLED=1 go test -race ./... -count=1"]
