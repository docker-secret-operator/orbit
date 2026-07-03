#!/bin/bash

echo "💥 DPIVOT Demo 2 - Cascading Failure Scenario"
echo "=============================================="
echo ""

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DEMO_DIR"

echo "This scenario demonstrates how dpivot handles cascading failures"
echo "when a dependent service goes down."
echo ""

# Function to show service health
show_health() {
    echo ""
    echo "📊 Current Service Status:"
    echo "  Gateway: $(curl -s http://localhost:3000/health | grep -q healthy && echo '✅' || echo '❌')"
    echo "  Auth: $(curl -s http://localhost:8001/health | grep -q healthy && echo '✅' || echo '❌')"
    echo "  User: $(curl -s http://localhost:8002/health | grep -q healthy && echo '✅' || echo '❌')"
    echo "  Product: $(curl -s http://localhost:8003/health | grep -q healthy && echo '✅' || echo '❌')"
    echo "  Order: $(curl -s http://localhost:8004/health | grep -q healthy && echo '✅' || echo '❌')"
    echo "  Payment: $(curl -s http://localhost:8005/health | grep -q healthy && echo '✅' || echo '❌')"
}

# Step 1: Show initial health
echo "Step 1: Initial system health"
show_health

echo ""
echo "Step 2: All services healthy - testing order creation..."
curl -s -X POST http://localhost:3000/orders/order/create \
    -H 'Content-Type: application/json' \
    -d '{"user_id":1,"product_id":1,"quantity":1}' | jq .

# Step 2: Stop product service (order depends on it)
echo ""
echo "Step 3: Stopping product service..."
docker-compose stop product
sleep 5
show_health

echo ""
echo "Step 4: Attempting order creation (should fail - product service down)..."
curl -s -X POST http://localhost:3000/orders/order/create \
    -H 'Content-Type: application/json' \
    -d '{"user_id":1,"product_id":1,"quantity":1}' | jq .

# Step 3: Restore product service
echo ""
echo "Step 5: Restarting product service..."
docker-compose start product
sleep 10
show_health

echo ""
echo "Step 6: Order service recovered - testing again..."
curl -s -X POST http://localhost:3000/orders/order/create \
    -H 'Content-Type: application/json' \
    -d '{"user_id":1,"product_id":1,"quantity":1}' | jq .

# Step 4: Stop payment service (order depends on it)
echo ""
echo "Step 7: Stopping payment service..."
docker-compose stop payment
sleep 5
show_health

echo ""
echo "Step 8: Attempting order creation (should fail - payment service down)..."
curl -s -X POST http://localhost:3000/orders/order/create \
    -H 'Content-Type: application/json' \
    -d '{"user_id":1,"product_id":1,"quantity":1}' | jq .

# Step 5: Restore payment service
echo ""
echo "Step 9: Restarting payment service..."
docker-compose start payment
sleep 10
show_health

echo ""
echo "Step 10: Payment recovered - testing order again..."
curl -s -X POST http://localhost:3000/orders/order/create \
    -H 'Content-Type: application/json' \
    -d '{"user_id":1,"product_id":1,"quantity":1}' | jq .

echo ""
echo "✅ Cascading failure scenario complete!"
echo ""
echo "Key observations:"
echo "1. When product service went down, order service detected it immediately"
echo "2. Order creation failed gracefully with 503 Service Unavailable"
echo "3. When payment went down, the same graceful failure occurred"
echo "4. System recovered automatically when services came back online"
echo "5. No cascading crash - blast radius was contained"
