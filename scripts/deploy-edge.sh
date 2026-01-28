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

usage() {
    echo "Usage: $0 <region>"
    echo ""
    echo "Regions:"
    echo "  us-ord  - Chicago (also origin)"
    echo "  us-lax  - Los Angeles"
    echo "  us-mia  - Miami"
    exit 1
}

# Check arguments
if [[ $# -lt 1 ]]; then
    usage
fi

REGION=$1

# Validate region
case "$REGION" in
    us-ord|us-lax|us-mia)
        ;;
    *)
        log_error "Invalid region: $REGION"
        usage
        ;;
esac

# Map region to kubeconfig
get_kubeconfig() {
    case "$REGION" in
        us-ord)
            echo "$PROJECT_ROOT/kubeconfigs/origin.yaml"
            ;;
        us-lax)
            echo "$PROJECT_ROOT/kubeconfigs/edge-lax.yaml"
            ;;
        us-mia)
            echo "$PROJECT_ROOT/kubeconfigs/edge-mia.yaml"
            ;;
    esac
}

# Map region to overlay
get_overlay() {
    case "$REGION" in
        us-ord)
            echo "edge-us-ord"
            ;;
        us-lax)
            echo "edge-us-lax"
            ;;
        us-mia)
            echo "edge-us-mia"
            ;;
    esac
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl not found. Please install kubectl."
        exit 1
    fi
    
    # Check for NATS core IP
    local nats_ip_file="$PROJECT_ROOT/kubeconfigs/nats-core-ip.txt"
    if [[ ! -f "$nats_ip_file" ]]; then
        log_error "NATS core IP not found. Deploy origin first with ./deploy-origin.sh"
        exit 1
    fi
    
    NATS_CORE_IP=$(cat "$nats_ip_file")
    export NATS_CORE_EXTERNAL_IP="$NATS_CORE_IP"
    log_info "Using NATS core IP: $NATS_CORE_IP"
}

# Set kubeconfig for the region
setup_kubeconfig() {
    local kubeconfig=$(get_kubeconfig)
    
    if [[ ! -f "$kubeconfig" ]]; then
        log_error "Kubeconfig not found at $kubeconfig"
        log_error "Run 'terraform apply' first to create the clusters"
        exit 1
    fi
    
    export KUBECONFIG="$kubeconfig"
    log_info "Using kubeconfig: $kubeconfig"
}

# Update the NATS core endpoint in the overlay
update_nats_endpoint() {
    local overlay=$(get_overlay)
    local kustomization="$PROJECT_ROOT/kubernetes/overlays/$overlay/kustomization.yaml"
    
    log_info "Updating NATS core endpoint in overlay..."
    
    # Use sed to replace the placeholder
    if [[ "$OSTYPE" == "darwin"* ]]; then
        sed -i '' "s/\${NATS_CORE_EXTERNAL_IP}/$NATS_CORE_IP/g" "$kustomization"
    else
        sed -i "s/\${NATS_CORE_EXTERNAL_IP}/$NATS_CORE_IP/g" "$kustomization"
    fi
}

# Wait for a deployment to be ready
wait_for_deployment() {
    local namespace=$1
    local deployment=$2
    local timeout=${3:-300}
    
    log_info "Waiting for deployment $deployment in namespace $namespace..."
    kubectl rollout status deployment/"$deployment" -n "$namespace" --timeout="${timeout}s"
}

# Deploy edge components
deploy_edge() {
    local overlay=$(get_overlay)
    
    log_info "Deploying edge components for $REGION..."
    
    # Apply the edge overlay using kustomize
    kubectl apply -k "$PROJECT_ROOT/kubernetes/overlays/$overlay"
    
    # Wait for components
    log_info "Waiting for NATS leaf nodes..."
    wait_for_deployment "nats-system" "nats-leaf" 180
    
    log_info "Waiting for SSE Adapter..."
    wait_for_deployment "sse-system" "sse-adapter" 120
    
    log_info "Waiting for monitoring stack..."
    wait_for_deployment "monitoring" "prometheus" 120
    wait_for_deployment "monitoring" "grafana" 120
}

# Verify NATS leaf connection
verify_nats_connection() {
    log_info "Verifying NATS leaf node connection..."
    
    # Get a leaf node pod
    local pod=$(kubectl get pods -n nats-system -l app.kubernetes.io/name=nats-leaf -o jsonpath='{.items[0].metadata.name}')
    
    if [[ -z "$pod" ]]; then
        log_error "No NATS leaf pods found"
        return 1
    fi
    
    # Check if connected to core
    local connected=$(kubectl exec -n nats-system "$pod" -- wget -q -O - http://localhost:8222/leafz 2>/dev/null | grep -c "remotes" || echo "0")
    
    if [[ "$connected" -gt 0 ]]; then
        log_info "NATS leaf node connected to core cluster"
    else
        log_warn "NATS leaf node may not be connected. Check logs:"
        echo "  kubectl logs -n nats-system $pod"
    fi
}

# Print endpoints
print_endpoints() {
    log_info "Deployment complete for $REGION! Service endpoints:"
    
    local sse_external=$(kubectl get svc sse-adapter -n sse-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "pending")
    local grafana_external=$(kubectl get svc grafana -n monitoring -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || echo "pending")
    
    echo ""
    echo "External endpoints (may take a few minutes to provision):"
    echo "  SSE Adapter ($REGION): http://$sse_external"
    echo "  Grafana ($REGION): http://$grafana_external (admin/admin123)"
    echo ""
    echo "Test SSE streaming:"
    echo "  curl -N http://$sse_external/stream/test-conversation"
}

# Main
main() {
    log_info "Deploying edge cluster for region: $REGION"
    
    check_prerequisites
    setup_kubeconfig
    update_nats_endpoint
    deploy_edge
    verify_nats_connection
    print_endpoints
    
    log_info "Edge deployment complete for $REGION!"
}

main "$@"
