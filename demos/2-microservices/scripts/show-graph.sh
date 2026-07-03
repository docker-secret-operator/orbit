#!/bin/bash

echo "📊 DPIVOT Demo 2 - Service Dependency Graph"
echo "==========================================="
echo ""

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DEMO_DIR"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to check service health
check_service() {
    local port=$1
    local name=$2
    if curl -s http://localhost:$port/health > /dev/null 2>&1; then
        echo -e "${GREEN}✓${NC}"
    else
        echo -e "${RED}✗${NC}"
    fi
}

echo "Service Status:"
echo "=============="
echo ""
echo -n "Auth Service (8001): "
check_service 8001 "auth"

echo -n "User Service (8002): "
check_service 8002 "user"

echo -n "Product Service (8003): "
check_service 8003 "product"

echo -n "Order Service (8004): "
check_service 8004 "order"

echo -n "Payment Service (8005): "
check_service 8005 "payment"

echo -n "Gateway (3000): "
check_service 3000 "gateway"

echo ""
echo "Dependency Graph:"
echo "================"
echo ""
echo "Gateway (3000)"
echo "  ├── Auth (8001) - No dependencies"
echo "  ├── User (8002)"
echo "  │   └── Auth (8001)"
echo "  ├── Product (8003) - No dependencies"
echo "  ├── Order (8004)"
echo "  │   ├── User (8002)"
echo "  │   ├── Product (8003)"
echo "  │   └── Payment (8005)"
echo "  └── Payment (8005) - No dependencies"
echo ""

echo "Upstream Service Dependencies:"
echo "============================="
echo ""

# Get order service details
echo "Order Service Dependencies:"
ORDER_DEPS=$(curl -s http://localhost:8004/health | jq '.dependent_services // empty' 2>/dev/null)
if [ ! -z "$ORDER_DEPS" ]; then
    echo "$ORDER_DEPS" | jq .
else
    echo "  (Unable to fetch - service may be down)"
fi

echo ""
echo "User Service Dependencies:"
USER_DEPS=$(curl -s http://localhost:8002/health | jq '.auth_service // "N/A"' 2>/dev/null)
if [ "$USER_DEPS" != "null" ]; then
    echo "  auth_service: $USER_DEPS"
else
    echo "  (Unable to fetch - service may be down)"
fi

echo ""
echo "Gateway Upstream Services:"
GATEWAY_DEPS=$(curl -s http://localhost:3000/health | jq '.services // empty' 2>/dev/null)
if [ ! -z "$GATEWAY_DEPS" ]; then
    echo "$GATEWAY_DEPS" | jq .
else
    echo "  (Unable to fetch - service may be down)"
fi

echo ""
echo "Failure Impact Analysis:"
echo "======================="
echo ""
echo "If Auth (8001) fails:"
echo "  ├─❌ User Service (8002) becomes unavailable"
echo "  ├─⚠️  Order Service (8004) becomes unavailable (User → Auth)"
echo "  ├─⚠️  Gateway /users route fails (503)"
echo "  ├─⚠️  Gateway /orders route fails (503)"
echo "  └─✅ Product & Payment services unaffected"
echo ""

echo "If Product (8003) fails:"
echo "  ├─❌ Order Service (8004) becomes unavailable"
echo "  ├─⚠️  Gateway /orders route fails (503)"
echo "  └─✅ All other services unaffected"
echo ""

echo "If Payment (8005) fails:"
echo "  ├─❌ Order creation (POST /orders/order/create) fails"
echo "  ├─⚠️  Gateway /orders route partially fails"
echo "  ├─✅ GET /orders still works"
echo "  └─✅ All other services unaffected"
echo ""

echo "If User (8002) fails:"
echo "  ├─❌ Order Service (8004) becomes unavailable (depends on User)"
echo "  ├─⚠️  Gateway /users route fails (503)"
echo "  ├─⚠️  Gateway /orders route fails (503)"
echo "  ├─⚠️  Auth Service affected by incoming requests"
echo "  └─✅ Product & Payment services unaffected"
echo ""

echo "If Order (8004) fails:"
echo "  ├─⚠️  Gateway /orders route fails (503)"
echo "  ├─✅ All other services unaffected"
echo "  └─✅ GET /users, /products, etc. still work"
echo ""

echo "Blast Radius Summary:"
echo "===================="
echo "Service           | Blast Radius | Dependent Services"
echo "─────────────────────────────────────────────────────"
echo "Auth              | Medium       | User (1 level), Order (2 levels)"
echo "Product           | Low          | Order (1 level)"
echo "Payment           | Low          | Order (1 level, partial)"
echo "User              | Medium       | Order (1 level)"
echo "Order             | Low          | Gateway (routing only)"
echo "Gateway           | Full         | All requests"
echo ""

echo "✅ Dependency graph visualization complete!"
