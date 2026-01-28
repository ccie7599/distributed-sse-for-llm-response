# CLAUDE.md - Project Context for Claude Code

## Project Overview

This is a **Distributed SSE Edge Delivery** system designed to stream LLM responses to users via Server-Sent Events (SSE), using NATS as a message distribution layer.

### Business Context
- **Customer**: Has existing Redis infrastructure for LLM token pub/sub
- **Problem**: NGINX fronting Redis can't scale SSE connections effectively
- **Solution**: NATS fan-out layer reduces Redis connection pressure while enabling edge delivery

### Architecture Summary
```
LLM → Redis → Redis-NATS Bridge → NATS Core (origin) → NATS Leaf (edge) → SSE Adapter → User
```

## Key Directories

| Path | Purpose |
|------|---------|
| `terraform/` | LKE cluster provisioning (3 clusters: us-ord origin, us-lax edge, us-mia edge) |
| `kubernetes/base/` | Base Kustomize configs for all components |
| `kubernetes/overlays/` | Environment-specific configs (origin vs edge) |
| `src/sse-adapter/` | Go service: NATS → SSE streaming |
| `src/redis-nats-bridge/` | Go service: Redis pub/sub → NATS |
| `src/spin-functions/` | Rust WASM examples for security inspection |
| `scripts/` | Deployment automation |

## Development Workflow

### Prerequisites
```bash
# Required tools
brew install terraform kubectl helm linode-cli

# For Go development
go version  # Needs 1.21+

# For Rust/WASM development
rustup target add wasm32-wasi
cargo install spin
```

### Deploy Sequence
```bash
# 1. Provision infrastructure
cd terraform
cp terraform.tfvars.example terraform.tfvars
# Edit with Linode API token
terraform init && terraform apply

# 2. Deploy origin (NATS core, LLM, Redis)
./scripts/deploy-origin.sh

# 3. Deploy edges
./scripts/deploy-edge.sh us-lax
./scripts/deploy-edge.sh us-mia

# 4. Run demo
./scripts/run-demo.sh
```

### Local Development

**SSE Adapter:**
```bash
cd src/sse-adapter
go mod tidy
NATS_URL=nats://localhost:4222 go run .
```

**Redis-NATS Bridge:**
```bash
cd src/redis-nats-bridge
REDIS_ADDR=localhost:6379 NATS_URL=nats://localhost:4222 go run .
```

**Spin Functions:**
```bash
cd src/spin-functions/nats-subscriber
spin build
spin up
```

## Common Tasks

### Rebuild and Push Images
```bash
# SSE Adapter
cd src/sse-adapter
docker build -t brianapley/sse-adapter:latest .
docker push brianapley/sse-adapter:latest

# Redis-NATS Bridge
cd src/redis-nats-bridge
docker build -t brianapley/redis-nats-bridge:latest .
docker push brianapley/redis-nats-bridge:latest
```

### Test SSE Streaming
```bash
# Connect to SSE endpoint
curl -N http://<sse-adapter-ip>/stream/<conversation-id>
```

### Publish Test Tokens
```bash
# Via NATS CLI
nats pub chat.test123.tokens '{"conversation_id":"test123","token":"Hello","sequence":1,"done":false}'
```

### Check NATS Cluster Health
```bash
# Core cluster
kubectl exec -n nats-system nats-0 -- nats-server --version
kubectl exec -n nats-system nats-0 -- wget -q -O - http://localhost:8222/varz

# Leaf connection status
kubectl exec -n nats-system deploy/nats-leaf -- wget -q -O - http://localhost:8222/leafz
```

## Architecture Decisions

### Why NATS instead of direct Redis fan-out?
- Redis pub/sub doesn't support interest-based routing
- Each SSE connection would require a Redis connection
- NATS leaf nodes enable geographic distribution

### Why Leaf Nodes instead of Full Cluster?
- Leaf nodes don't participate in consensus
- No cross-region latency impact on message delivery
- Simpler operational model at the edge

### Why JetStream?
- Message persistence for reconnection handling
- Built-in deduplication (important for multi-bridge HA)
- Consumer tracking for replay

## TODOs for Claude Code

1. ~~**Image Registry**: Update image references in Kubernetes manifests~~ (Done: using `brianapley/...`)

2. **NATS Leaf Node Config**: The `${NATS_CORE_EXTERNAL_IP}` placeholder in edge overlays needs to be resolved by deploy script

3. **HuggingFace Token**: If using gated models, add HF_TOKEN to `kubernetes/base/llm/deployment.yaml`

4. **Security Inspection**: Disabled by default. Three patterns documented in README and `docs/security-inspection-patterns.md`. Implementation hooks in `src/sse-adapter/sse_handler.go`

5. ~~**Load Generator**: Create `demo/load-generator/` to simulate concurrent users~~ (Done: see `demo/load-generator/`)

6. **Redis-NATS Bridge HA**: Current implementation is single-instance; add leader election or NATS dedup

## Useful Commands

```bash
# Watch NATS metrics
watch -n1 'kubectl exec -n nats-system nats-0 -- wget -q -O - http://localhost:8222/varz | jq .connections'

# Tail SSE adapter logs
kubectl logs -f -n sse-system -l app.kubernetes.io/name=sse-adapter

# Check GPU node status
kubectl describe node -l nvidia.com/gpu=true

# Force rollout after config change
kubectl rollout restart deployment/sse-adapter -n sse-system
```

## Links

- [NATS Documentation](https://docs.nats.io/)
- [NATS Leaf Nodes](https://docs.nats.io/nats-server/configuration/leafnodes)
- [JetStream](https://docs.nats.io/nats-concepts/jetstream)
- [Fermyon Spin](https://developer.fermyon.com/spin)
- [vLLM](https://docs.vllm.ai/)
