# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o welink-wecom-bot .

FROM alpine:3.21
RUN adduser -D -u 1001 welink
WORKDIR /app
COPY --from=builder /app/welink-wecom-bot .
RUN chown welink:welink /app/welink-wecom-bot
USER welink
CMD ["./welink-wecom-bot"]
