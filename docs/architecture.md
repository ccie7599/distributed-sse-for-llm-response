# Architecture Documentation

## Overview

This document describes the distributed SSE edge delivery architecture for LLM streaming responses. The system is designed to reduce connection pressure on the origin while providing low-latency delivery to geographically distributed users.

## Problem Statement

Traditional LLM streaming architectures face several challenges:

1. **Connection Scalability**: Each user maintains a persistent SSE connection to the origin, limiting concurrent users
2. **Geographic Latency**: Users far from the origin experience higher latency for each token
3. **Single Point of Failure**: All connections terminate at a single location
4. **Security Inspection**: Inspecting streaming content adds latency or requires post-hoc analysis

## Solution Architecture

### Data Flow

```
User Request → LLM → Redis → NATS Core → NATS Leaf → SSE Adapter → User
                      │
                      └── (existing infrastructure, preserved)
```

### Key Design Decisions

#### 1. Redis Remains Source of Truth

The customer's existing Redis infrastructure is preserved. Rather than replacing Redis, we add NATS as a fan-out layer:

- Redis handles LLM → message publishing (existing flow)
- Redundant Redis-NATS bridges subscribe to Redis and republish to NATS
- Reduces Redis connection count from O(users) to O(bridges)

#### 2. Interest-Based Subscription

NATS leaf nodes only subscribe to subjects (conversation IDs) they have active clients for:

```
Subject naming: chat.{conversation_id}.tokens

Example:
- User A connects to us-lax edge, conversation abc123
- us-lax NATS leaf subscribes to: chat.abc123.tokens
- us-ord and us-mia leaf nodes do NOT receive these messages
```

This prevents unnecessary traffic to edge nodes without active users for a given conversation.

#### 3. JetStream for Reliability

NATS JetStream at the origin provides:

- **Message persistence**: Survives NATS restarts
- **Replay capability**: Reconnecting clients can catch up
- **Consumer tracking**: Know which messages have been delivered

Stream configuration:
```yaml
name: CHAT_TOKENS
subjects: ["chat.*.tokens"]
retention: limits
max_age: 5m          # Tokens only relevant for active streams
max_bytes: 1GB
replicas: 3          # HA across NATS cluster nodes
```

#### 4. Leaf Node Topology

Leaf nodes connect to the core cluster but don't participate in clustering consensus:

```
                    ┌─────────────────────┐
                    │   NATS Core Cluster │
                    │   (3 nodes, us-ord) │
                    │   - JetStream       │
                    │   - Clustering      │
                    └─────────┬───────────┘
                              │
           ┌──────────────────┼──────────────────┐
           │                  │                  │
           ▼                  ▼                  ▼
    ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
    │ Leaf Node A │    │ Leaf Node B │    │ Leaf Node C │
    │   us-ord    │    │   us-lax    │    │   us-mia    │
    └─────────────┘    └─────────────┘    └─────────────┘
```

Benefits:
- No cross-region consensus delays
- Each leaf independently connected
- Core cluster handles all durability

## Component Details

### Redis-NATS Bridge

**Purpose**: Bridge Redis pub/sub to NATS subjects

**Implementation**: Go service using:
- `github.com/redis/go-redis/v9` for Redis subscription
- `github.com/nats-io/nats.go` for NATS publishing

**High Availability**: Run 2-3 instances with coordination to prevent duplicate publishing:
- Option A: Redis XREAD with consumer groups (if using Streams)
- Option B: NATS deduplication window (simpler, slight overhead)

**Message Transformation**:
```go
// Redis message format (from LLM)
type RedisMessage struct {
    ConversationID string `json:"conversation_id"`
    Token          string `json:"token"`
    Sequence       int64  `json:"sequence"`
    Done           bool   `json:"done"`
}

// Published to NATS subject: chat.{conversation_id}.tokens
```

### SSE Adapter

**Purpose**: Convert NATS messages to SSE events for HTTP clients

**Implementation**: Go service using:
- `github.com/nats-io/nats.go` for subscription
- Standard `net/http` with streaming response

**Flow**:
1. Client connects: `GET /stream/{conversation_id}`
2. Adapter subscribes to `chat.{conversation_id}.tokens` on local NATS leaf
3. For each NATS message, write SSE event:
   ```
   event: token
   id: {sequence}
   data: {"token": "Hello", "done": false}
   
   ```
4. On `done: true`, close connection

**Reconnection Handling**:
- Client sends `Last-Event-ID` header
- Adapter uses JetStream consumer with `OptStartSeq(lastEventID + 1)`
- Missed tokens replayed before live stream

### NATS Core Cluster

**Deployment**: 3-node cluster in us-ord (origin)

**Configuration**:
```yaml
jetstream:
  enabled: true
  store_dir: /data/jetstream
  max_memory: 1Gi
  max_file: 10Gi

cluster:
  name: nats-core
  routes:
    - nats://nats-0.nats-headless:6222
    - nats://nats-1.nats-headless:6222
    - nats://nats-2.nats-headless:6222

leafnodes:
  port: 7422
  authorization:
    users:
      - user: leaf
        password: $LEAF_PASSWORD
```

### NATS Leaf Nodes

**Deployment**: 2-3 nodes per edge LKE cluster

**Configuration**:
```yaml
leafnodes:
  remotes:
    - url: nats-leaf://leaf:$LEAF_PASSWORD@nats-core.origin.svc:7422
      account: $SYS
```

**Note**: Leaf nodes at the same edge location don't cluster with each other. They're behind a Kubernetes Service for load balancing, but each maintains independent connection to core.

### LLM Inference (vLLM)

**Model**: Mistral-7B-Instruct-v0.2 (fits single GPU)

**Deployment**: 
- GPU node pool in us-ord LKE cluster
- vLLM server with OpenAI-compatible API
- Publishes tokens to Redis as they're generated

**Configuration**:
```yaml
model: mistralai/Mistral-7B-Instruct-v0.2
tensor-parallel-size: 1
gpu-memory-utilization: 0.9
max-model-len: 8192
```

## Network Architecture

### Internal Communication

| From | To | Port | Protocol |
|------|-----|------|----------|
| LLM | Redis | 6379 | Redis |
| Redis-NATS Bridge | Redis | 6379 | Redis |
| Redis-NATS Bridge | NATS Core | 4222 | NATS |
| NATS Core nodes | Each other | 6222 | NATS Cluster |
| NATS Leaf | NATS Core | 7422 | NATS Leaf |
| SSE Adapter | NATS Leaf | 4222 | NATS |

### External Communication

| Endpoint | Port | Protocol | Purpose |
|----------|------|----------|---------|
| SSE Adapter | 443 | HTTPS/SSE | User connections |
| Grafana | 443 | HTTPS | Monitoring dashboards |

## Scaling Considerations

### Horizontal Scaling

| Component | Scale By | Considerations |
|-----------|----------|----------------|
| SSE Adapter | Add pods | Stateless, simple HPA |
| NATS Leaf | Add nodes | Behind K8s Service |
| Redis-NATS Bridge | Limited (2-3) | Deduplication overhead |
| NATS Core | Add nodes | Impacts consensus latency |
| LLM | Add GPU nodes | Model sharding for larger models |

### Vertical Scaling

| Component | Resource | Impact |
|-----------|----------|--------|
| NATS Core | Memory | More in-flight messages |
| NATS Core | Disk | Longer JetStream retention |
| LLM | GPU VRAM | Larger models, more batch |

## Failure Modes

### Redis-NATS Bridge Failure

- **Impact**: New tokens not delivered to NATS
- **Mitigation**: Run multiple bridges, health checks restart failed instances
- **Recovery**: New bridge instance subscribes to Redis, resumes publishing

### NATS Core Node Failure

- **Impact**: Temporary unavailability during leader election
- **Mitigation**: 3-node cluster tolerates 1 failure
- **Recovery**: Automatic via Raft consensus

### NATS Leaf Node Failure

- **Impact**: SSE adapters on that node lose NATS connection
- **Mitigation**: Multiple leaf nodes behind Service, automatic reconnection
- **Recovery**: SSE adapter reconnects to another leaf node

### SSE Adapter Failure

- **Impact**: Connected users lose stream
- **Mitigation**: Multiple replicas, load balancer health checks
- **Recovery**: User reconnects with Last-Event-ID, catches up via JetStream

## Monitoring

### Key Metrics

| Metric | Source | Alert Threshold |
|--------|--------|-----------------|
| `nats_varz_connections` | NATS | > 10000 per node |
| `nats_varz_slow_consumers` | NATS | > 0 |
| `nats_jetstream_consumer_pending` | NATS | > 1000 |
| `sse_adapter_active_connections` | SSE Adapter | > 5000 per pod |
| `sse_adapter_reconnection_rate` | SSE Adapter | > 10/min |
| `redis_bridge_publish_errors` | Redis-NATS Bridge | > 0 |

### Dashboards

See `demo/dashboards/` for Grafana dashboard configurations covering:
- NATS cluster health
- SSE connection counts by region
- Token delivery latency (Redis → User)
- JetStream lag per consumer
