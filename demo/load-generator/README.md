# Load Generator

A load testing tool for the Distributed SSE system. Simulates LLM token generation and SSE client connections to measure end-to-end latency and throughput.

## Modes

| Mode | Description |
|------|-------------|
| `producer` | Publishes tokens to Redis (simulates LLM output) |
| `consumer` | Connects to SSE endpoints (simulates users) |
| `both` | Runs producer and consumer together for end-to-end testing |

## Quick Start

```bash
# Run against deployed cluster (auto-detects endpoints)
./run-loadtest.sh

# Local development with port-forwarding
kubectl port-forward -n redis-system svc/redis 6379:6379 &
kubectl port-forward -n sse-system svc/sse-adapter 8080:80 &
./run-loadtest.sh -r localhost:6379 -s http://localhost:8080
```

## Options

```
-m, --mode MODE           Mode: producer, consumer, or both (default: both)
-r, --redis ADDR          Redis address (default: auto-detect from cluster)
-s, --sse URL             SSE adapter URL (default: auto-detect from cluster)
-c, --conversations N     Number of concurrent conversations (default: 5)
-t, --tokens N            Tokens per conversation (default: 50)
-d, --delay MS            Delay between tokens in ms (default: 50)
--duration DURATION       Test duration, e.g., 30s, 5m (default: 30s)
```

## Examples

```bash
# Quick smoke test
./run-loadtest.sh -c 2 -t 10 --duration 10s

# Medium load test
./run-loadtest.sh -c 20 -t 100 --duration 2m

# High load test
./run-loadtest.sh -c 100 -t 200 -d 20 --duration 5m

# Producer only (for testing Redis → NATS bridge)
./run-loadtest.sh -m producer -c 10 -t 50

# Consumer only (connect to existing streams)
./run-loadtest.sh -m consumer -c 10
```

## Metrics

The load generator reports:

- **Tokens Published**: Number of tokens sent to Redis
- **Tokens Received**: Number of tokens received via SSE
- **Connections Opened/Closed**: SSE connection lifecycle
- **Errors**: Failed operations
- **Latency** (min/avg/max): End-to-end time from Redis publish to SSE receive

## Running in Kubernetes

Build and push the Docker image:

```bash
docker build -t brianapley/sse-load-generator:latest .
docker push brianapley/sse-load-generator:latest
```

Run as a Kubernetes Job:

```bash
kubectl run loadtest --rm -it --restart=Never \
  --image=brianapley/sse-load-generator:latest \
  -- -mode both -redis redis.redis-system:6379 \
     -sse http://sse-adapter.sse-system:80 \
     -conversations 50 -tokens 100 -duration 5m
```

## Architecture

```
┌─────────────────┐         ┌─────────────────┐
│  Load Generator │         │  Load Generator │
│   (producer)    │         │   (consumer)    │
└────────┬────────┘         └────────▲────────┘
         │                           │
         │ Redis Pub/Sub             │ SSE
         │                           │
         ▼                           │
┌─────────────────┐         ┌────────┴────────┐
│     Redis       │────────▶│   SSE Adapter   │
└─────────────────┘         └─────────────────┘
         │                           ▲
         │                           │
         ▼                           │
┌─────────────────┐         ┌────────┴────────┐
│  Redis-NATS     │────────▶│   NATS Leaf     │
│    Bridge       │         │                 │
└─────────────────┘         └─────────────────┘
```

The producer publishes to Redis with timestamps. The consumer receives via SSE and calculates latency by comparing the original timestamp to receipt time.
