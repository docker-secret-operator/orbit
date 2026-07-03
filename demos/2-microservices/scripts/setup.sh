#!/bin/bash

echo "🚀 DPIVOT Demo 2 - Microservices Mesh Setup"
echo "============================================"
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

# Navigate to demo directory
DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DEMO_DIR"

echo "📁 Demo directory: $DEMO_DIR"
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
echo "📊 Service Endpoints:"
echo "  API Gateway: http://localhost:3000"
echo "  Auth Service: http://localhost:8001"
echo "  User Service: http://localhost:8002"
echo "  Product Service: http://localhost:8003"
echo "  Order Service: http://localhost:8004"
echo "  Payment Service: http://localhost:8005"
echo ""
echo "🧪 Test commands:"
echo "  # Check gateway health"
echo "  curl http://localhost:3000/health"
echo ""
echo "  # Get users"
echo "  curl http://localhost:3000/users/users"
echo ""
echo "  # Create order (requires JSON)"
echo "  curl -X POST http://localhost:3000/orders/order/create -H 'Content-Type: application/json' -d '{\"user_id\":1,\"product_id\":1,\"quantity\":1}'"
echo ""
echo "  # View logs"
echo "  docker-compose logs -f"
echo ""
echo "✅ Setup complete!"
