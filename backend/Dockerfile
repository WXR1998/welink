# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS builder
ARG GIT_SHA=unknown
ARG GIT_COMMIT_TIME=unknown
RUN apk add --no-cache git gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.gitCommit=${GIT_SHA} -X main.gitCommitTime=${GIT_COMMIT_TIME}" -o welink-backend .

FROM alpine:3.21
RUN adduser -D -u 1001 welink && mkdir -p /app/prefs && chown -R welink:welink /app
WORKDIR /app
COPY --from=builder /app/welink-backend .
RUN chown welink:welink /app/welink-backend
USER welink
EXPOSE 8080
ENV GIN_MODE=release
ENV DATA_DIR=/app/data
ENV WELINK_LISTEN_LAN=1
CMD ["./welink-backend"]
