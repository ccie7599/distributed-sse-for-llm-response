#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_demo() { echo -e "${BLUE}[DEMO]${NC} $1"; }

# Get SSE adapter endpoints from all clusters
get_endpoints() {
    local endpoints=()
    
    for kubeconfig in "$PROJECT_ROOT"/kubeconfigs/*.yaml; do
        if [[ -f "$kubeconfig" ]]; then
            local ip=$(KUBECONFIG="$kubeconfig" kubectl get svc sse-adapter -n sse-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "")
            if [[ -n "$ip" ]]; then
                endpoints+=("$ip")
            fi
        fi
    done
    
    echo "${endpoints[@]}"
}

# Demo 1: Basic SSE streaming
demo_basic_streaming() {
    log_demo "Demo 1: Basic SSE Streaming"
    echo "This demonstrates SSE connection to an edge node"
    echo ""
    
    local endpoint=$1
    local conversation_id="demo-$(date +%s)"
    
    echo "Connecting to: http://$endpoint/stream/$conversation_id"
    echo "Press Ctrl+C to stop"
    echo ""
    
    curl -N "http://$endpoint/stream/$conversation_id"
}

# Demo 2: Multi-region comparison
demo_multi_region() {
    log_demo "Demo 2: Multi-Region Latency Comparison"
    echo "This compares connection times across edge locations"
    echo ""
    
    local endpoints=($(get_endpoints))
    
    if [[ ${#endpoints[@]} -lt 2 ]]; then
        log_warn "Need at least 2 endpoints for multi-region demo"
        return
    fi
    
    echo "Measuring time-to-first-byte for each region..."
    echo ""
    
    for endpoint in "${endpoints[@]}"; do
        local start=$(date +%s.%N)
        curl -s -o /dev/null -w "TTFB: %{time_starttransfer}s\n" "http://$endpoint/healthz"
        echo "  Endpoint: $endpoint"
    done
}

# Demo 3: Simulate LLM token stream
demo_simulate_stream() {
    log_demo "Demo 3: Simulated LLM Token Stream"
    echo "Publishing tokens to Redis to simulate LLM output"
    echo ""
    
    # Use origin kubeconfig
    export KUBECONFIG="$PROJECT_ROOT/kubeconfigs/origin.yaml"
    
    local conversation_id="demo-$(date +%s)"
    local redis_pod=$(kubectl get pods -n redis-system -l app.kubernetes.io/name=redis -o jsonpath='{.items[0].metadata.name}')
    
    echo "Conversation ID: $conversation_id"
    echo ""
    echo "Publishing tokens... (open another terminal to consume with curl)"
    echo "  curl -N http://<sse-adapter-ip>/stream/$conversation_id"
    echo ""
    
    local tokens=("Hello" " there" "!" " I" " am" " an" " AI" " assistant" "." " How" " can" " I" " help" " you" " today" "?")
    local seq=1
    
    for token in "${tokens[@]}"; do
        local msg=$(cat <<EOF
{"conversation_id":"$conversation_id","token":"$token","sequence":$seq,"done":false,"timestamp":$(date +%s%3N)}
EOF
)
        kubectl exec -n redis-system "$redis_pod" -- redis-cli PUBLISH "llm:tokens:$conversation_id" "$msg"
        echo "Published: $token"
        ((seq++))
        sleep 0.1
    done
    
    # Send done message
    local done_msg=$(cat <<EOF
{"conversation_id":"$conversation_id","token":"","sequence":$seq,"done":true,"timestamp":$(date +%s%3N)}
EOF
)
    kubectl exec -n redis-system "$redis_pod" -- redis-cli PUBLISH "llm:tokens:$conversation_id" "$done_msg"
    echo ""
    echo "Stream complete!"
}

# Demo 4: Load test
demo_load_test() {
    log_demo "Demo 4: Load Test"
    echo "Spawning multiple concurrent SSE connections"
    echo ""
    
    local endpoint=$1
    local num_connections=${2:-10}
    
    echo "Creating $num_connections concurrent connections to $endpoint"
    echo ""
    
    for i in $(seq 1 $num_connections); do
        local conv_id="load-test-$i-$(date +%s)"
        (curl -s -N "http://$endpoint/stream/$conv_id" > /dev/null &)
    done
    
    sleep 2
    
    echo "Checking metrics..."
    curl -s "http://$endpoint:9090/metrics" | grep sse_active_connections || echo "Metrics not available"
    
    echo ""
    echo "Connections spawned. Check Grafana for metrics."
}

# Menu
show_menu() {
    echo ""
    echo "╔══════════════════════════════════════════════════════════════╗"
    echo "║         Distributed SSE Edge Delivery Demo                   ║"
    echo "╠══════════════════════════════════════════════════════════════╣"
    echo "║  1. Basic SSE Streaming                                      ║"
    echo "║  2. Multi-Region Latency Comparison                          ║"
    echo "║  3. Simulate LLM Token Stream                                ║"
    echo "║  4. Load Test                                                ║"
    echo "║  5. Show All Endpoints                                       ║"
    echo "║  6. Exit                                                     ║"
    echo "╚══════════════════════════════════════════════════════════════╝"
    echo ""
}

# Main
main() {
    log_info "Distributed SSE Edge Delivery Demo"
    
    local endpoints=($(get_endpoints))
    
    if [[ ${#endpoints[@]} -eq 0 ]]; then
        log_error "No SSE adapter endpoints found. Deploy clusters first."
        exit 1
    fi
    
    log_info "Found ${#endpoints[@]} endpoint(s)"
    
    while true; do
        show_menu
        read -p "Select demo (1-6): " choice
        
        case $choice in
            1)
                echo ""
                read -p "Enter endpoint (default: ${endpoints[0]}): " endpoint
                endpoint=${endpoint:-${endpoints[0]}}
                demo_basic_streaming "$endpoint"
                ;;
            2)
                demo_multi_region
                ;;
            3)
                demo_simulate_stream
                ;;
            4)
                echo ""
                read -p "Enter endpoint (default: ${endpoints[0]}): " endpoint
                endpoint=${endpoint:-${endpoints[0]}}
                read -p "Number of connections (default: 10): " num
                num=${num:-10}
                demo_load_test "$endpoint" "$num"
                ;;
            5)
                echo ""
                echo "Available endpoints:"
                for ep in "${endpoints[@]}"; do
                    echo "  http://$ep"
                done
                ;;
            6)
                log_info "Goodbye!"
                exit 0
                ;;
            *)
                log_warn "Invalid choice"
                ;;
        esac
    done
}

main "$@"
