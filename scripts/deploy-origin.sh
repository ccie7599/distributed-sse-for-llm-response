#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl not found. Please install kubectl."
        exit 1
    fi
    
    if ! command -v kustomize &> /dev/null; then
        log_warn "kustomize not found, will use kubectl's built-in kustomize"
    fi
}

# Set kubeconfig for origin cluster
setup_kubeconfig() {
    local kubeconfig="$PROJECT_ROOT/kubeconfigs/origin.yaml"
    
    if [[ ! -f "$kubeconfig" ]]; then
        log_error "Origin kubeconfig not found at $kubeconfig"
        log_error "Run 'terraform apply' first to create the clusters"
        exit 1
    fi
    
    export KUBECONFIG="$kubeconfig"
    log_info "Using kubeconfig: $kubeconfig"
}

# Wait for a deployment to be ready
wait_for_deployment() {
    local namespace=$1
    local deployment=$2
    local timeout=${3:-300}
    
    log_info "Waiting for deployment $deployment in namespace $namespace..."
    kubectl rollout status deployment/"$deployment" -n "$namespace" --timeout="${timeout}s"
}

# Wait for a statefulset to be ready
wait_for_statefulset() {
    local namespace=$1
    local statefulset=$2
    local timeout=${3:-300}
    
    log_info "Waiting for statefulset $statefulset in namespace $namespace..."
    kubectl rollout status statefulset/"$statefulset" -n "$namespace" --timeout="${timeout}s"
}

# Deploy origin components
deploy_origin() {
    log_info "Deploying origin components..."
    
    # Apply the origin overlay using kustomize
    kubectl apply -k "$PROJECT_ROOT/kubernetes/overlays/origin"
    
    # Wait for core components
    log_info "Waiting for NATS core cluster..."
    wait_for_statefulset "nats-system" "nats" 600
    
    log_info "Waiting for Redis..."
    wait_for_deployment "redis-system" "redis" 120

    log_info "Waiting for Redis-NATS Bridge..."
    wait_for_deployment "bridge-system" "redis-nats-bridge" 120

    log_info "Waiting for LLM (this may take several minutes for model download)..."
    wait_for_deployment "llm-system" "vllm" 900
    
    log_info "Waiting for SSE Adapter..."
    wait_for_deployment "sse-system" "sse-adapter" 120
    
    log_info "Waiting for monitoring stack..."
    wait_for_deployment "monitoring" "prometheus" 120
    wait_for_deployment "monitoring" "grafana" 120
}

# Run JetStream setup job
setup_jetstream() {
    log_info "Setting up JetStream streams..."
    
    # Check if job already completed
    if kubectl get job nats-stream-setup -n nats-system &> /dev/null; then
        local status=$(kubectl get job nats-stream-setup -n nats-system -o jsonpath='{.status.succeeded}')
        if [[ "$status" == "1" ]]; then
            log_info "JetStream streams already configured"
            return
        fi
    fi
    
    # Wait for the job to complete
    kubectl wait --for=condition=complete job/nats-stream-setup -n nats-system --timeout=120s || {
        log_warn "Stream setup job may have failed, check logs:"
        kubectl logs job/nats-stream-setup -n nats-system
    }
}

# Get service endpoints
print_endpoints() {
    log_info "Deployment complete! Service endpoints:"
    
    echo ""
    echo "NATS Core (internal): nats.nats-system.svc.cluster.local:4222"
    echo "NATS Leaf Port (internal): nats.nats-system.svc.cluster.local:7422"
    
    # Get external IPs if available
    local nats_external=$(kubectl get svc nats -n nats-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "pending")
    local sse_external=$(kubectl get svc sse-adapter -n sse-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "pending")
    local grafana_external=$(kubectl get svc grafana -n monitoring -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "pending")
    
    echo ""
    echo "External endpoints (may take a few minutes to provision):"
    echo "  NATS (for leaf connections): $nats_external:7422"
    echo "  SSE Adapter: http://$sse_external"
    echo "  Grafana: http://$grafana_external (admin/admin123)"
    echo ""
    
    # Save NATS external IP for edge deployments
    if [[ "$nats_external" != "pending" ]]; then
        echo "$nats_external" > "$PROJECT_ROOT/kubeconfigs/nats-core-ip.txt"
        log_info "NATS core IP saved to kubeconfigs/nats-core-ip.txt"
    else
        log_warn "NATS external IP not yet available. Run this script again later or check:"
        echo "  kubectl get svc nats -n nats-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}'"
    fi
}

# Main
main() {
    log_info "Deploying origin cluster components..."
    
    check_prerequisites
    setup_kubeconfig
    deploy_origin
    setup_jetstream
    print_endpoints
    
    log_info "Origin deployment complete!"
}

main "$@"
