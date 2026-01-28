#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$(dirname "$SCRIPT_DIR")")"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }

# Default values
MODE="both"
REDIS_ADDR=""
SSE_URL=""
CONVERSATIONS=5
TOKENS=50
TOKEN_DELAY=50
DURATION="30s"

usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Distributed SSE Load Generator

Options:
  -m, --mode MODE           Mode: producer, consumer, or both (default: both)
  -r, --redis ADDR          Redis address (default: auto-detect from cluster)
  -s, --sse URL             SSE adapter URL (default: auto-detect from cluster)
  -c, --conversations N     Number of concurrent conversations (default: 5)
  -t, --tokens N            Tokens per conversation (default: 50)
  -d, --delay MS            Delay between tokens in ms (default: 50)
  --duration DURATION       Test duration, e.g., 30s, 5m (default: 30s)
  -h, --help                Show this help

Examples:
  # Run full end-to-end test against cluster
  $0

  # Producer only (publish to Redis)
  $0 -m producer -r localhost:6379

  # Consumer only (connect to SSE)
  $0 -m consumer -s http://localhost:8080

  # High load test
  $0 -c 50 -t 100 --duration 5m

  # Local development (port-forwarded services)
  $0 -r localhost:6379 -s http://localhost:8080
EOF
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -m|--mode) MODE="$2"; shift 2 ;;
        -r|--redis) REDIS_ADDR="$2"; shift 2 ;;
        -s|--sse) SSE_URL="$2"; shift 2 ;;
        -c|--conversations) CONVERSATIONS="$2"; shift 2 ;;
        -t|--tokens) TOKENS="$2"; shift 2 ;;
        -d|--delay) TOKEN_DELAY="$2"; shift 2 ;;
        --duration) DURATION="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown option: $1"; usage; exit 1 ;;
    esac
done

# Auto-detect addresses from Kubernetes if not provided
detect_cluster_addresses() {
    local kubeconfig="${PROJECT_ROOT}/kubeconfigs/origin.yaml"

    if [[ -f "$kubeconfig" ]]; then
        export KUBECONFIG="$kubeconfig"
        log_info "Using kubeconfig: $kubeconfig"

        # Detect Redis address
        if [[ -z "$REDIS_ADDR" ]] && [[ "$MODE" == "producer" || "$MODE" == "both" ]]; then
            log_info "Detecting Redis address from cluster..."
            REDIS_IP=$(kubectl get svc redis -n redis-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "")
            if [[ -n "$REDIS_IP" ]]; then
                REDIS_ADDR="${REDIS_IP}:6379"
                log_info "Found Redis at $REDIS_ADDR"
            else
                log_warn "Redis LoadBalancer IP not available. Use port-forward:"
                echo "  kubectl port-forward -n redis-system svc/redis 6379:6379"
                REDIS_ADDR="localhost:6379"
            fi
        fi

        # Detect SSE adapter address
        if [[ -z "$SSE_URL" ]] && [[ "$MODE" == "consumer" || "$MODE" == "both" ]]; then
            log_info "Detecting SSE adapter address from cluster..."
            SSE_IP=$(kubectl get svc sse-adapter -n sse-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "")
            if [[ -n "$SSE_IP" ]]; then
                SSE_URL="http://${SSE_IP}"
                log_info "Found SSE adapter at $SSE_URL"
            else
                log_warn "SSE adapter LoadBalancer IP not available. Use port-forward:"
                echo "  kubectl port-forward -n sse-system svc/sse-adapter 8080:80"
                SSE_URL="http://localhost:8080"
            fi
        fi
    else
        log_warn "No kubeconfig found. Using localhost defaults."
        [[ -z "$REDIS_ADDR" ]] && REDIS_ADDR="localhost:6379"
        [[ -z "$SSE_URL" ]] && SSE_URL="http://localhost:8080"
    fi
}

# Build the load generator if needed
build_generator() {
    cd "$SCRIPT_DIR"

    if [[ ! -f "load-generator" ]] || [[ "main.go" -nt "load-generator" ]]; then
        log_info "Building load generator..."
        go build -o load-generator .
    fi
}

# Main
main() {
    detect_cluster_addresses
    build_generator

    log_info "Starting load test..."
    echo "  Mode:          $MODE"
    echo "  Redis:         $REDIS_ADDR"
    echo "  SSE URL:       $SSE_URL"
    echo "  Conversations: $CONVERSATIONS"
    echo "  Tokens/conv:   $TOKENS"
    echo "  Token delay:   ${TOKEN_DELAY}ms"
    echo "  Duration:      $DURATION"
    echo ""

    cd "$SCRIPT_DIR"
    ./load-generator \
        -mode "$MODE" \
        -redis "$REDIS_ADDR" \
        -sse "$SSE_URL" \
        -conversations "$CONVERSATIONS" \
        -tokens "$TOKENS" \
        -token-delay "$TOKEN_DELAY" \
        -duration "$DURATION"
}

main
