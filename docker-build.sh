#!/bin/bash
# Orbit Docker Build and Push Script
# Usage: ./docker-build.sh [version] [push]
# Example: ./docker-build.sh 1.0.0 push

set -e

REGISTRY="orbit"
IMAGE_NAME="orbit"
VERSION="${1:-latest}"
PUSH="${2:-false}"

FULL_IMAGE="${REGISTRY}/${IMAGE_NAME}:${VERSION}"
LATEST_IMAGE="${REGISTRY}/${IMAGE_NAME}:latest"

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${BLUE}➜${NC} $1"
}

log_success() {
    echo -e "${GREEN}✓${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}⚠${NC} $1"
}

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

log_info "Building Orbit Docker image"
log_info "Version: $VERSION"
log_info "Image: $FULL_IMAGE"
echo ""

# Build image
log_info "Building Docker image..."
if docker build -f docker/Dockerfile \
    -t "$FULL_IMAGE" \
    -t "$LATEST_IMAGE" \
    --progress=plain \
    "$SCRIPT_DIR"; then
    log_success "Build successful"
else
    echo -e "${RED}✗ Build failed${NC}"
    exit 1
fi

echo ""
log_info "Image size:"
docker images "$REGISTRY/$IMAGE_NAME" --no-trunc

echo ""

# Optional: Push to registry
if [ "$PUSH" = "push" ]; then
    echo ""
    log_info "Pushing to Docker Hub..."

    # Check Docker login
    if ! docker info | grep -q "Username"; then
        log_warn "Not logged in to Docker Hub. Attempting login..."
        docker login -u "$REGISTRY"
    fi

    if docker push "$FULL_IMAGE" && docker push "$LATEST_IMAGE"; then
        log_success "Push successful"
        echo ""
        log_info "Image is now available:"
        echo "  docker pull $FULL_IMAGE"
        echo "  docker pull $LATEST_IMAGE"
    else
        echo -e "${RED}✗ Push failed${NC}"
        exit 1
    fi
fi

echo ""
log_success "Docker image ready!"
echo ""
echo "Quick test:"
echo "  docker run --rm -v /var/run/docker.sock:/var/run/docker.sock $FULL_IMAGE version"
echo ""
