# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o welink-feishu-bot .

FROM alpine:3.21
RUN adduser -D -u 1001 welink && mkdir -p /var/lib/welink-feishu && chown -R welink:welink /var/lib/welink-feishu
WORKDIR /app
COPY --from=builder /app/welink-feishu-bot .
RUN chown welink:welink /app/welink-feishu-bot
USER welink
ENV SESSION_STORE_PATH=/var/lib/welink-feishu/sessions.json
CMD ["./welink-feishu-bot"]
