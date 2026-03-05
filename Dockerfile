# Stage 1: Build
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /cloud-tasks-emulator ./cmd/server

# Stage 2: Run
FROM alpine:3.19

RUN adduser -D -g '' appuser

WORKDIR /app

COPY --from=builder /cloud-tasks-emulator .
RUN mkdir -p /app/data && chown -R appuser:appuser /app

USER appuser

ENV DATABASE_PATH=/app/data/tasks.db

EXPOSE 8085 9090

ENTRYPOINT ["./cloud-tasks-emulator"]
