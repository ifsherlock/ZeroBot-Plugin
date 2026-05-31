# syntax=docker/dockerfile:1

# Build with the Go version declared by go.mod. Override when your registry uses
# a patched tag, for example:
#   docker build --build-arg GO_IMAGE=golang:1.26.1-alpine .
ARG GO_IMAGE=golang:1.26-alpine
ARG ALPINE_IMAGE=alpine:3.22

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder

WORKDIR /src
RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/zbp .

FROM ${ALPINE_IMAGE} AS runtime

RUN apk add --no-cache ca-certificates tzdata nginx

WORKDIR /app

COPY --from=builder /out/zbp /app/zbp

# Keep built-in transparent logos available in the small runtime image. Runtime
# cache, cookies and secrets stay in /app/data through the compose volume.
COPY --from=builder /src/plugin/mediaparser/data/mediaparser/logos /app/plugin/mediaparser/data/mediaparser/logos

COPY docker/nginx.conf /etc/nginx/nginx.conf
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh && \
    mkdir -p /app/data/mediaparser/cache /run/nginx /var/log/nginx

ENV TZ=Asia/Shanghai \
    WEBUI_ADDR=0.0.0.0:3000 \
    ONEBOT_WS_URL=ws://127.0.0.1:3001 \
    ONEBOT_WS_TOKEN= \
    BOT_NICKNAME=ZeroBot \
    COMMAND_PREFIX=/ \
    SUPER_USERS= \
    ZBP_ARGS=

EXPOSE 3000 3088
VOLUME ["/app/data"]

ENTRYPOINT ["/entrypoint.sh"]
