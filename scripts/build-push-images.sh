#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

DOCKER_USER="${DOCKER_USER:-brianapley}"

IMAGES=(
    "sse-adapter:src/sse-adapter"
    "redis-nats-bridge:src/redis-nats-bridge"
    "sse-load-generator:demo/load-generator"
)

usage() {
    cat <<EOF
Usage: $0 [OPTIONS] [COMMAND]

Build and push Docker images for Distributed SSE project.

Commands:
  build     Build all images (default)
  push      Push all images to Docker Hub
  all       Build and push all images

Options:
  -u, --user USER    Docker Hub username (default: brianapley)
  -h, --help         Show this help

Examples:
  $0 build              # Build all images
  $0 push               # Push all images (requires docker login)
  $0 all                # Build and push
  $0 -u myuser all      # Use different Docker Hub user
EOF
}

build_images() {
    log_info "Building Docker images..."

    for entry in "${IMAGES[@]}"; do
        IFS=':' read -r name path <<< "$entry"
        full_name="${DOCKER_USER}/${name}:latest"
        build_path="${PROJECT_ROOT}/${path}"

        log_info "Building ${full_name} from ${path}..."
        docker build -t "$full_name" "$build_path"
        log_info "Built ${full_name}"
    done

    log_info "All images built successfully!"
    docker images | grep "$DOCKER_USER"
}

push_images() {
    log_info "Pushing Docker images to Docker Hub..."

    # Check if logged in
    if ! docker info 2>/dev/null | grep -q "Username"; then
        log_warn "You may not be logged in to Docker Hub."
        log_warn "Run 'docker login' first if push fails."
    fi

    for entry in "${IMAGES[@]}"; do
        IFS=':' read -r name path <<< "$entry"
        full_name="${DOCKER_USER}/${name}:latest"

        log_info "Pushing ${full_name}..."
        docker push "$full_name"
        log_info "Pushed ${full_name}"
    done

    log_info "All images pushed successfully!"
}

# Parse arguments
COMMAND="build"

while [[ $# -gt 0 ]]; do
    case $1 in
        -u|--user) DOCKER_USER="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        build|push|all) COMMAND="$1"; shift ;;
        *) echo "Unknown option: $1"; usage; exit 1 ;;
    esac
done

# Execute command
case $COMMAND in
    build)
        build_images
        ;;
    push)
        push_images
        ;;
    all)
        build_images
        push_images
        ;;
esac
