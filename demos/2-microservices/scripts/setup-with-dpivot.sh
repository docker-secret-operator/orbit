#!/bin/bash

echo "🚀 DPIVOT Demo 2 - Microservices Mesh Setup with dpivot Plugin"
echo "=============================================================="
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
    GATEWAY=$(docker-compose exec -T gateway curl -s http://localhost:3000/health 2>/dev/null | grep -q healthy && echo "ok" || echo "down")
    AUTH=$(docker-compose exec -T auth curl -s http://localhost:8001/health 2>/dev/null | grep -q healthy && echo "ok" || echo "down")
    USER=$(docker-compose exec -T user curl -s http://localhost:8002/health 2>/dev/null | grep -q healthy && echo "ok" || echo "down")
    PRODUCT=$(docker-compose exec -T product curl -s http://localhost:8003/health 2>/dev/null | grep -q healthy && echo "ok" || echo "down")
    ORDER=$(docker-compose exec -T order curl -s http://localhost:8004/health 2>/dev/null | grep -q healthy && echo "ok" || echo "down")
    PAYMENT=$(docker-compose exec -T payment curl -s http://localhost:8005/health 2>/dev/null | grep -q healthy && echo "ok" || echo "down")

    echo "  Gateway: $GATEWAY | Auth: $AUTH | User: $USER | Product: $PRODUCT | Order: $ORDER | Payment: $PAYMENT"

    if [ "$GATEWAY" = "ok" ] && [ "$AUTH" = "ok" ] && [ "$USER" = "ok" ] && [ "$PRODUCT" = "ok" ] && [ "$ORDER" = "ok" ] && [ "$PAYMENT" = "ok" ]; then
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
docker dpivot status --file docker-compose.yml --format table
echo ""

echo "❤️  Health Check (via dpivot):"
docker dpivot health --file docker-compose.yml --format table
echo ""

echo "🔄 Recovery State (via dpivot):"
docker dpivot recovery --show-state --format json | jq '.' 2>/dev/null || echo "  (Loading...)"
echo ""

echo "📊 Service Endpoints:"
echo "  API Gateway: http://localhost:3000"
echo "  Auth Service: http://localhost:8001"
echo "  User Service: http://localhost:8002"
echo "  Product Service: http://localhost:8003"
echo "  Order Service: http://localhost:8004"
echo "  Payment Service: http://localhost:8005"
echo ""

echo "🧪 Test commands:"
echo "  # View status via dpivot"
echo "  docker dpivot status --file docker-compose.yml --follow"
echo ""
echo "  # View dependency graph"
echo "  ./scripts/show-graph.sh"
echo ""
echo "  # View health"
echo "  docker dpivot health --file docker-compose.yml"
echo ""
echo "  # Demonstrate cascading failure prevention"
echo "  ./scripts/failure-scenario.sh"
echo ""
echo "  # View logs"
echo "  docker dpivot logs --follow"
echo ""
echo "  # View recovery info"
echo "  docker dpivot recovery --show-state --show-plan"
echo ""
echo "✅ Setup complete!"
echo ""
echo "💡 Next: Run './scripts/failure-scenario.sh' to see cascading failure prevention"
