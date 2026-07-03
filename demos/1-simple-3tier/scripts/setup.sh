#!/bin/bash

set -e

echo "🚀 DPIVOT Demo - 3-Tier Application Setup"
echo "=========================================="
echo ""

# Check if docker is installed
if ! command -v docker &> /dev/null; then
    echo "❌ Docker is not installed. Please install Docker first."
    exit 1
fi

if ! command -v docker-compose &> /dev/null; then
    echo "❌ Docker Compose is not installed. Please install Docker Compose first."
    exit 1
fi

# Change to demo directory
DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DEMO_DIR"

echo "📁 Working directory: $DEMO_DIR"
echo ""

# Stop any running containers
echo "🛑 Stopping any running containers..."
docker-compose down -v 2>/dev/null || true
echo ""

# Build images
echo "🔨 Building Docker images..."
docker-compose build --no-cache
echo ""

# Start services
echo "▶️  Starting services..."
docker-compose up -d
echo ""

# Wait for services to be ready
echo "⏳ Waiting for services to be healthy..."
for i in {1..60}; do
    if docker-compose exec -T postgres pg_isready -U dpivot &>/dev/null; then
        echo "✓ Database is ready"
        break
    fi
    echo -n "."
    sleep 1
done

for i in {1..60}; do
    if curl -s http://localhost:8080/health &>/dev/null; then
        echo "✓ API is ready"
        break
    fi
    echo -n "."
    sleep 1
done

for i in {1..60}; do
    if curl -s http://localhost/health &>/dev/null; then
        echo "✓ Web frontend is ready"
        break
    fi
    echo -n "."
    sleep 1
done

echo ""
echo "✅ Demo is ready!"
echo ""
echo "📊 Access the application:"
echo "   - Frontend: http://localhost"
echo "   - API: http://localhost:8080/health"
echo "   - Database: localhost:5432"
echo ""
echo "🔄 To deploy a new version, run:"
echo "   ./scripts/deploy-v2.sh"
echo ""
echo "💥 To trigger a failure scenario, run:"
echo "   ./scripts/trigger-failure.sh"
echo ""
