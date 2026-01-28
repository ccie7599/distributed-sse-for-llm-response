# Distributed SSE Edge Delivery

A distributed architecture for delivering LLM streaming responses via Server-Sent Events (SSE), using NATS as a fan-out layer to reduce origin connection pressure and provide edge-native delivery.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              ORIGIN (us-ord)                                    │
│                                                                                 │
│  ┌──────────────┐    ┌────────────────┐    ┌─────────┐    ┌─────────────────┐  │
│  │ LLM Inference│───▶│ LLM Stream     │───▶│  Redis  │───▶│ Redis-NATS      │  │
│  │  (vLLM)      │    │ Proxy          │    │ pub/sub │    │ Bridge          │  │
│  │  Mistral-7B  │    │ POST /chat     │    │         │    │                 │  │
│  │  GPU Node    │    └────────────────┘    └─────────┘    └────────┬────────┘  │
│  └──────────────┘                                                  │           │
│                                                                    ▼           │
│                                                    ┌───────────────────────┐   │
│                                                    │ NATS Core Cluster     │   │
│                                                    │ (3 nodes, JetStream)  │   │
│                                                    └───────────┬───────────┘   │
│                                                                │               │
│  ┌──────────────┐    ┌──────────────┐                          │               │
│  │ SSE Adapter  │◀───│ NATS Leaf    │◀─────────────────────────┤               │
│  │ (origin)     │    │ (origin)     │                          │               │
│  └──────────────┘    └──────────────┘                          │               │
└────────────────────────────────────────────────────────────────┼───────────────┘
                                                                 │
                    ┌────────────────────────────────────────────┼────────────────┐
                    │                                            │                │
                    ▼                                            ▼                ▼
┌───────────────────────────────┐  ┌───────────────────────────────┐  ┌───────────────────────────────┐
│      EDGE (us-lax)            │  │      EDGE (us-mia)            │  │   Additional Edge Regions     │
│                               │  │                               │  │                               │
│  ┌─────────────────────────┐  │  │  ┌─────────────────────────┐  │  │  ┌─────────────────────────┐  │
│  │  NATS Leaf (2 replicas) │  │  │  │  NATS Leaf (2 replicas) │  │  │  │  NATS Leaf (2 replicas) │  │
│  └────────────┬────────────┘  │  │  └────────────┬────────────┘  │  │  └────────────┬────────────┘  │
│               │               │  │               │               │  │               │               │
│               ▼               │  │               ▼               │  │               ▼               │
│  ┌─────────────────────────┐  │  │  ┌─────────────────────────┐  │  │  ┌─────────────────────────┐  │
│  │   SSE Adapter Service   │  │  │  │   SSE Adapter Service   │  │  │  │   SSE Adapter Service   │  │
│  └─────────────────────────┘  │  │  └─────────────────────────┘  │  │  └─────────────────────────┘  │
│               │               │  │               │               │  │               │               │
│               ▼               │  │               ▼               │  │               ▼               │
│          [ Users ]            │  │          [ Users ]            │  │          [ Users ]            │
└───────────────────────────────┘  └───────────────────────────────┘  └───────────────────────────────┘
```

## Quick Test

Send a chat request to any edge and receive streaming tokens:

```bash
curl -N -X POST "http://<edge-sse-adapter>/chat" -H "Content-Type: application/json" -d '{"message": "What is the capital of France?"}'
```

The response streams back as Server-Sent Events with each token from the LLM.

## Data Flow

1. **User Request**: Client sends `POST /chat` to the nearest **edge** SSE Adapter
2. **Edge Subscribe**: Edge SSE Adapter generates conversation ID, subscribes to NATS leaf
3. **Forward to Origin**: Edge forwards the request to Origin LLM Stream Proxy
4. **LLM Inference**: Origin calls vLLM (Mistral-7B on GPU) with streaming enabled
5. **Token Publishing**: Each token is published to Redis channel `chat.<conversation_id>.tokens`
6. **NATS Bridge**: Redis-NATS Bridge publishes to NATS subject `chat.<id>.tokens`
7. **Fan-out**: NATS Core fans out to the edge leaf node that subscribed
8. **SSE Delivery**: Edge SSE Adapter streams tokens back to client on the same HTTP response

## Key Benefits

1. **Reduced Origin Connection Pressure**: NATS handles fan-out, so Redis only needs one subscriber instead of thousands of direct SSE connections
2. **Edge-Native Delivery**: SSE connections terminate closer to users, reducing latency
3. **Interest-Based Replication**: Edge NATS nodes only receive messages for conversations they have active clients for
4. **Horizontal Scaling**: Add more edge locations without impacting origin

## Components

| Component | Purpose | Location |
|-----------|---------|----------|
| LLM (vLLM + Mistral-7B) | Generate streaming responses | Origin (GPU node) |
| LLM Stream Proxy | Accept chat requests, stream to Redis | Origin |
| Redis | Pub/sub message bus | Origin |
| Redis-NATS Bridge | Subscribe to Redis, publish to NATS | Origin |
| NATS Core Cluster | Central message routing (3 nodes) | Origin |
| NATS Leaf Nodes | Edge message delivery (2 per region) | Each edge region |
| SSE Adapter | Convert NATS messages to SSE streams | Each region |
| Prometheus + Grafana | Monitoring and observability | Origin |

## Prerequisites

- [Terraform](https://terraform.io) >= 1.5
- [kubectl](https://kubernetes.io/docs/tasks/tools/) >= 1.28
- [Linode CLI](https://www.linode.com/docs/products/tools/cli/get-started/) configured with API token
- [Docker](https://docker.com) for building images

## Quick Start

### 1. Deploy Infrastructure

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with your Linode API token

terraform init
terraform apply
```

This creates 3 LKE clusters:
- `sse-origin` (us-ord) - With GPU node pool for LLM
- `sse-edge-lax` (us-lax) - Edge cluster
- `sse-edge-mia` (us-mia) - Edge cluster

### 2. Get Kubeconfigs

```bash
# Get cluster IDs
linode-cli lke clusters-list

# Download kubeconfigs (replace IDs with your values)
linode-cli lke kubeconfig-view <origin-id> --json | jq -r '.[0].kubeconfig' | base64 -d > kubeconfig-origin.yaml
linode-cli lke kubeconfig-view <lax-id> --json | jq -r '.[0].kubeconfig' | base64 -d > kubeconfig-edge-lax.yaml
linode-cli lke kubeconfig-view <mia-id> --json | jq -r '.[0].kubeconfig' | base64 -d > kubeconfig-edge-mia.yaml
```

### 3. Deploy Origin Components

```bash
export KUBECONFIG=kubeconfig-origin.yaml

# Install NVIDIA device plugin (for GPU node)
kubectl create ns nvidia-device-plugin
helm repo add nvdp https://nvidia.github.io/k8s-device-plugin
helm install nvidia-device-plugin nvdp/nvidia-device-plugin -n nvidia-device-plugin

# Deploy NATS Core cluster
kubectl apply -k kubernetes/overlays/origin/nats-core

# Deploy Redis
kubectl apply -k kubernetes/base/redis

# Deploy LLM (vLLM with Mistral-7B)
kubectl apply -k kubernetes/base/llm

# Deploy LLM Stream Proxy
kubectl apply -k kubernetes/base/llm-stream-proxy

# Deploy Redis-NATS Bridge
kubectl apply -k kubernetes/base/redis-nats-bridge

# Deploy SSE Adapter (origin)
kubectl apply -k kubernetes/overlays/origin/sse-adapter

# Deploy NATS Leaf (origin)
kubectl apply -k kubernetes/overlays/origin/nats-leaf

# Deploy Monitoring
kubectl apply -k kubernetes/base/monitoring
```

### 4. Deploy Edge Components

For each edge cluster (us-lax, us-mia):

```bash
# Get NATS Core external IP
NATS_CORE_IP=$(kubectl --kubeconfig=kubeconfig-origin.yaml get svc -n nats-system nats-external -o jsonpath='{.status.loadBalancer.ingress[0].ip}')

# Deploy to edge (example: us-lax)
export KUBECONFIG=kubeconfig-edge-lax.yaml

# Update NATS leaf config with core IP
kubectl create ns nats-system
kubectl create secret generic nats-leaf-credentials -n nats-system \
  --from-literal=LEAF_USER=leaf \
  --from-literal=LEAF_PASSWORD=<your-password> \
  --from-literal=NATS_CORE_HOST=$NATS_CORE_IP

# Deploy NATS Leaf
kubectl apply -k kubernetes/overlays/edge-lax/nats-leaf

# Deploy SSE Adapter
kubectl apply -k kubernetes/overlays/edge-lax/sse-adapter
```

### 5. Test the System

```bash
# Get SSE Adapter IPs
SSE_ORIGIN=$(kubectl --kubeconfig=kubeconfig-origin.yaml get svc -n sse-system sse-adapter -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
SSE_LAX=$(kubectl --kubeconfig=kubeconfig-edge-lax.yaml get svc -n sse-system sse-adapter -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
SSE_MIA=$(kubectl --kubeconfig=kubeconfig-edge-mia.yaml get svc -n sse-system sse-adapter -o jsonpath='{.status.loadBalancer.ingress[0].ip}')

# Send a chat request to any edge - tokens stream back as SSE
curl -N -X POST "http://$SSE_LAX/chat" -H "Content-Type: application/json" -d '{"message": "Hello, how are you?"}'
```

The response streams back as SSE events:
```
event: connected
data: {"conversation_id":"b92d330b-b010-46dc-a5fe-a409ddc1b0e6"}

event: token
id: 1
data: {"conversation_id":"...","token":"Hello","sequence":1,"done":false,"timestamp":...}

event: token
id: 2
data: {"conversation_id":"...","token":"!","sequence":2,"done":false,"timestamp":...}

event: token
id: 3
data: {"conversation_id":"...","token":"[DONE]","sequence":3,"done":true,"timestamp":...}
```

## Project Structure

```
.
├── terraform/                    # Infrastructure as Code
│   ├── main.tf                   # LKE cluster definitions
│   ├── variables.tf              # Input variables
│   └── terraform.tfvars.example  # Example configuration
├── kubernetes/
│   ├── base/                     # Base Kustomize configurations
│   │   ├── nats-core/            # NATS Core cluster (3 nodes)
│   │   ├── nats-leaf/            # NATS Leaf node config
│   │   ├── sse-adapter/          # SSE Adapter service
│   │   ├── llm/                  # vLLM deployment
│   │   ├── llm-stream-proxy/     # Chat API proxy
│   │   ├── redis/                # Redis for pub/sub
│   │   ├── redis-nats-bridge/    # Redis to NATS bridge
│   │   └── monitoring/           # Prometheus + Grafana
│   └── overlays/                 # Environment-specific overlays
│       ├── origin/               # Origin cluster configs
│       ├── edge-lax/             # LA edge configs
│       └── edge-mia/             # Miami edge configs
├── src/
│   ├── sse-adapter/              # Go service: NATS → SSE
│   ├── redis-nats-bridge/        # Go service: Redis → NATS
│   ├── llm-stream-proxy/         # Go service: Chat API → Redis
│   └── spin-functions/           # WASM examples (optional)
└── CLAUDE.md                     # Project context for Claude Code
```

## Building Images

```bash
# SSE Adapter
cd src/sse-adapter
docker build --platform linux/amd64 -t your-registry/sse-adapter:latest .
docker push your-registry/sse-adapter:latest

# Redis-NATS Bridge
cd src/redis-nats-bridge
docker build --platform linux/amd64 -t your-registry/redis-nats-bridge:latest .
docker push your-registry/redis-nats-bridge:latest

# LLM Stream Proxy
cd src/llm-stream-proxy
docker build --platform linux/amd64 -t your-registry/llm-stream-proxy:latest .
docker push your-registry/llm-stream-proxy:latest
```

## Monitoring

Access Grafana at the LoadBalancer IP on port 80:
- Default credentials: `admin` / `admin123` (change in production)
- Prometheus datasource is pre-configured

Useful queries:
- `nats_varz_connections` - Active NATS connections
- `rate(nats_varz_in_msgs[1m])` - Message throughput

## Architecture Decisions

### Why NATS instead of direct Redis fan-out?
- Redis pub/sub doesn't support interest-based routing
- Each SSE connection would require a Redis connection
- NATS leaf nodes enable geographic distribution

### Why Leaf Nodes instead of Full Cluster?
- Leaf nodes don't participate in consensus
- No cross-region latency impact on message delivery
- Simpler operational model at the edge

### Why Core NATS instead of JetStream for fan-out?
- JetStream publishes don't automatically propagate to leaf node subscriptions
- Core NATS provides the fan-out semantics needed for real-time streaming
- JetStream is still used for persistence and replay if needed

## Security Inspection Patterns

LLM outputs may need inspection before delivery (PII detection, prompt injection, toxic content). The challenge: inspection traditionally requires buffering, which defeats streaming. This architecture supports three patterns with different trade-offs.

> **Current Status**: Inspection is disabled by default. See `src/sse-adapter/sse_handler.go` for implementation hooks.

### Pattern Comparison

| Pattern | Latency | Security | When to Use |
|---------|---------|----------|-------------|
| **Disabled** | 0ms | None | Development, trusted content |
| **Inline** | +10-50ms/token | Highest | Regulated environments (healthcare, finance) |
| **Async** | ~0ms | Lowest | Audit/logging, post-hoc remediation acceptable |
| **Hybrid** | +100-200ms initial | Moderate | Balance of security and performance |

### Inline (Blocking)

```
NATS → SSE Adapter → Inspector → User
                         ↓
                   Drop/Redact if flagged
```

Every token is inspected before delivery. Inspector failure blocks streaming.

### Async Tap (Non-blocking)

```
NATS ─┬─→ SSE Adapter → User (immediate)
      │
      └─→ Inspector (parallel) → Alert if flagged
```

Tokens delivered immediately. Inspector runs in parallel and can trigger alerts or kill the connection via control channel.

### Hybrid (Buffered)

```
NATS → Buffer (100-200ms) → Inspector → SSE Adapter → User
```

Small initial delay allows inspection to keep pace. Backpressure if inspector falls behind.

### Configuration

Set via environment variables in the SSE Adapter:

```yaml
INSPECTION_MODE: "disabled"  # Options: disabled, inline, async, hybrid
INSPECTION_BUFFER_MS: "150"  # For hybrid mode
INSPECTION_ENDPOINT: "http://inspector:8080/inspect"  # For inline mode
```

## Regions

| Region | Linode DC | Purpose |
|--------|-----------|---------|
| us-ord | Chicago | Origin (LLM + NATS Core) |
| us-lax | Los Angeles | Edge |
| us-mia | Miami | Edge |

## License

MIT
