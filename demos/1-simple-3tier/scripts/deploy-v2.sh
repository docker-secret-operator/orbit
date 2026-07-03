#!/bin/bash

set -e

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DEMO_DIR"

echo "🚀 DPIVOT Demo - Zero-Downtime Deployment"
echo "=========================================="
echo ""
echo "Deploying API v2 (with zero downtime)..."
echo ""

# Update API version in docker-compose.yml
echo "📝 Updating API version to v2..."
sed -i.bak 's/VERSION: v1/VERSION: v2/' docker-compose.yml || sed -i '' 's/VERSION: v1/VERSION: v2/' docker-compose.yml

# Rebuild API image
echo "🔨 Building new API image..."
docker-compose build api --no-cache

# Start a new API container (v2)
echo "▶️  Starting new API container (v2)..."
docker-compose up -d api

# Give it time to start and pass health checks
echo "⏳ Waiting for v2 API to become healthy..."
sleep 10

# Verify new version is running
echo "✓ Checking API version..."
API_VERSION=$(curl -s http://localhost:8080/api/stats | grep -o '"version":"[^"]*' | cut -d'"' -f4)
echo "  API version: $API_VERSION"

if [ "$API_VERSION" = "v2" ]; then
    echo "✅ Deployment successful! v2 is now live."
else
    echo "⚠️  Version mismatch. Rolling back..."
    sed -i.bak 's/VERSION: v2/VERSION: v1/' docker-compose.yml || sed -i '' 's/VERSION: v2/VERSION: v1/' docker-compose.yml
    docker-compose up -d api
    exit 1
fi

echo ""
echo "📊 Traffic is being served by v2 with zero downtime!"
echo ""
