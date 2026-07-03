#!/bin/bash

echo "🚀 DPIVOT Demo 1 - Setup with dpivot Plugin"
echo "=========================================="
echo ""

# Check for Docker
if ! command -v docker &> /dev/null; then
    echo "❌ Docker is not installed. Please install Docker first."
    exit 1
fi

if ! command -v docker-compose &> /dev/null; then
    echo "❌ Docker Compose is not installed. Please install Docker Compose first."
    exit 1
fi

# Check for dpivot plugin
if ! docker dpivot version &> /dev/null; then
    echo "⚠️  dpivot plugin is not installed"
    echo ""
    echo "Install it with:"
    echo "  cd plugin/"
    echo "  make install"
    echo ""
    echo "Then run this script again"
    exit 1
fi

# Navigate to demo directory
DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DEMO_DIR"

echo "📁 Demo directory: $DEMO_DIR"
echo "✓ dpivot plugin version: $(docker dpivot version)"
echo ""

# Stop existing containers
echo "🛑 Stopping existing containers..."
docker-compose down -v 2>/dev/null || true

echo ""
echo "🔨 Building services..."
docker-compose build --no-cache

echo ""
echo "▶️  Starting services..."
docker-compose up -d

echo ""
echo "⏳ Waiting for services to be healthy..."
TIMEOUT=120
ELAPSED=0

while [ $ELAPSED -lt $TIMEOUT ]; do
    NGINX=$(docker-compose exec -T web curl -s http://localhost/health 2>/dev/null | grep -q healthy && echo "ok" || echo "down")
    API=$(docker-compose exec -T api curl -s http://localhost:8080/health 2>/dev/null | grep -q healthy && echo "ok" || echo "down")
    DB=$(docker-compose exec -T postgres pg_isready -U dpivot 2>/dev/null | grep -q "accepting" && echo "ok" || echo "down")

    echo "  Nginx: $NGINX | API: $API | Database: $DB"

    if [ "$NGINX" = "ok" ] && [ "$API" = "ok" ] && [ "$DB" = "ok" ]; then
        echo "✅ All services are healthy!"
        break
    fi

    sleep 5
    ELAPSED=$((ELAPSED + 5))
done

if [ $ELAPSED -ge $TIMEOUT ]; then
    echo "⚠️  Timeout waiting for services to be healthy"
    echo "Try checking logs with: docker-compose logs -f"
    exit 1
fi

echo ""
echo "✓ dpivot plugin initialized"
echo ""

echo "📊 System Status (via dpivot):"
docker dpivot status || echo "  (Proxy not yet running for this stack)"
echo ""

echo "📊 Service Endpoints:"
echo "  Frontend: http://localhost"
echo "  API: http://localhost:8080/health"
echo "  Database: localhost:5432"
echo ""

echo "🧪 Test commands:"
echo "  # Generate dpivot-enhanced compose file"
echo "  docker dpivot generate"
echo ""
echo "  # View status via dpivot"
echo "  docker dpivot status"
echo ""
echo "  # Deploy new version with zero downtime"
echo "  ./scripts/deploy-v2-with-dpivot.sh"
echo ""
echo "  # Rollback if needed"
echo "  docker dpivot rollback api"
echo ""
echo "✅ Setup complete!"
echo ""
echo "💡 Next: Run './scripts/deploy-v2-with-dpivot.sh' to see zero-downtime deployment"
