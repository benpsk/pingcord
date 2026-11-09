# syntax=docker/dockerfile:1

# ---- build ------------------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
# Pure-Go SQLite: no cgo, fully static binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/pingcord ./cmd/pingcord \
 && mkdir -p /out/data

# ---- runtime ----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/pingcord /usr/local/bin/pingcord
# Pre-create the data dir owned by the nonroot user so named volumes inherit it.
COPY --from=build --chown=65532:65532 /out/data /data

ENV DB_PATH=/data/pingcord.db \
    PORT=8080
EXPOSE 8080
VOLUME ["/data"]
USER nonroot:nonroot

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/pingcord", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/pingcord"]
