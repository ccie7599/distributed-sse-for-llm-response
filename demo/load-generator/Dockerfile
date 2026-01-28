FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod ./
COPY *.go ./

RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -o load-generator .

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/load-generator .

ENTRYPOINT ["./load-generator"]
