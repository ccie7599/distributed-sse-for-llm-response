# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Copy go mod and source files
COPY go.mod ./
COPY *.go ./

# Download dependencies and build
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -o sse-adapter .

# Runtime stage
FROM alpine:3.18

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/sse-adapter .

# Non-root user
RUN adduser -D -g '' appuser
USER appuser

EXPOSE 8080 9090

ENTRYPOINT ["./sse-adapter"]
