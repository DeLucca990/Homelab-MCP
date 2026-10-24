FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/server ./cmd/telegram-bot && mkdir /data

FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates systemd \
    && rm -rf /var/lib/apt/lists/*
ENV TZ=America/Sao_Paulo STATE_DIRECTORY=/data
COPY --from=build /out/server /out/telegram-bot /
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
HEALTHCHECK --interval=1m --timeout=10s --start-period=30s CMD ["/server", "healthcheck"]
CMD ["/server"]
