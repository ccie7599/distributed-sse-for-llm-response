# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod ./
COPY *.go ./

RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -o redis-nats-bridge .

# Runtime stage
FROM alpine:3.18

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/redis-nats-bridge .

RUN adduser -D -g '' appuser
USER appuser

ENTRYPOINT ["./redis-nats-bridge"]
