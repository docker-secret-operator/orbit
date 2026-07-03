# DPIVOT Demo 2: Microservices Mesh

A demonstration of **cascading failure prevention** and **blast radius analysis** using dpivot with a multi-service architecture.

## Architecture

```
┌──────────────────────────────────────────────────┐
│              API Gateway (3000)                  │
│         (Routes to all upstream services)        │
└─────────────┬────────────────────────────────────┘
              │
    ┌─────────┼─────────┬──────────┬─────────┐
    │         │         │          │         │
    ▼         ▼         ▼          ▼         ▼
┌────────┐┌────────┐┌────────┐┌────────┐┌────────┐
│ Auth   ││ User   ││Product ││ Order  ││Payment │
│(8001) ││(8002) ││(8003) ││(8004) ││(8005) │
└────────┘└────────┘└────────┘└────────┘└────────┘
    │         │         │          │         │
    └─────────┴─────────┴──────────┴─────────┘
                       │
                ┌──────▼─────────┐
                │   PostgreSQL   │
                │    (5432)      │
                └────────────────┘
                
            └──────────────────┘
            │ Shared dpivot-mesh│
            │ Docker Bridge Net │
            └────────────────────┘
```

## Service Dependencies

```
Gateway
  ├── Auth Service (no dependencies)
  ├── User Service
  │   └── Auth Service (dependency)
  ├── Product Service (no dependencies)
  ├── Order Service
  │   ├── User Service
  │   ├── Product Service
  │   └── Payment Service
  └── Payment Service (no dependencies)
```

## Features

✅ Multi-service orchestration  
✅ Dependency graph visualization  
✅ Cascading failure prevention  
✅ Blast radius containment  
✅ Health-driven service discovery  
✅ Graceful degradation  
✅ Circuit breaker pattern (built-in via health checks)  

## Prerequisites

- Docker & Docker Compose
- bash shell
- curl (for testing)
- jq (optional, for pretty JSON output)

## Quick Start

### 1. Setup the demo

```bash
chmod +x scripts/*.sh
./scripts/setup.sh
```

This will:
- Build all 5 microservices
- Start the complete mesh
- Configure health checks
- Initialize the database
- Wait for all services to be healthy

### 2. Access the application

```bash
# API Gateway (routes to all services)
curl http://localhost:3000

# Individual Services
curl http://localhost:8001/health  # Auth
curl http://localhost:8002/health  # User
curl http://localhost:8003/health  # Product
curl http://localhost:8004/health  # Order
curl http://localhost:8005/health  # Payment
```

## Demo Scenarios

### Scenario 1: Cascading Failure Prevention

**Automated scenario**:
```bash
./scripts/failure-scenario.sh
```

This demonstrates:
1. ✅ All services healthy - orders can be created
2. ❌ Product service stops - order creation fails gracefully
3. ✅ Product service recovers - orders work again
4. ❌ Payment service stops - order creation fails gracefully
5. ✅ Payment service recovers - orders work again

**Manual scenario** - Stop a dependent service:

Terminal 1: Monitor order service health
```bash
watch -n 1 'curl -s http://localhost:3000/orders/stats | jq'
```

Terminal 2: Create orders (works initially)
```bash
curl -X POST http://localhost:3000/orders/order/create \
  -H 'Content-Type: application/json' \
  -d '{"user_id":1,"product_id":1,"quantity":1}'
```

Terminal 3: Stop product service
```bash
docker-compose stop product
```

**Observation**:
- Order service immediately detects product service failure
- Subsequent order creation requests fail with 503 Service Unavailable
- No cascading crash to other services
- Blast radius limited to order service only

### Scenario 2: Service Recovery

```bash
# After stopping a service, restart it
docker-compose start product

# Wait a few seconds for health checks
sleep 10

# Services automatically recover
curl -s http://localhost:3000/orders/stats | jq
```

**Observation**:
- Health checks detect service recovery
- Dependent services automatically resume operation
- No manual intervention needed

### Scenario 3: Inspect Service Dependencies

**Get gateway status** (shows all upstream services):
```bash
curl -s http://localhost:3000/health | jq '.services'
```

**Get order service status** (shows all dependencies):
```bash
curl -s http://localhost:8004/health | jq '.dependent_services'
```

**Check individual service health**:
```bash
for svc in auth user product order payment; do
  echo "=== $svc ==="
  curl -s http://localhost:800$i/health | jq .
done
```

## API Endpoints

### Gateway (Port 3000)

```bash
# Root - shows available routes
curl http://localhost:3000

# Health check (shows upstream services)
curl http://localhost:3000/health

# Route to user service
curl http://localhost:3000/users/users

# Route to auth service
curl -X POST http://localhost:3000/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"pass"}'
```

### User Service (Port 8002)

```bash
# Get all users
curl http://localhost:8002/users

# Get specific user
curl 'http://localhost:8002/user?id=1'

# Get user stats (shows auth service status)
curl http://localhost:8002/stats
```

### Product Service (Port 8003)

```bash
# Get all products
curl http://localhost:8003/products

# Get specific product
curl 'http://localhost:8003/product?id=1'

# Get product stats
curl http://localhost:8003/stats
```

### Order Service (Port 8004)

```bash
# Get all orders
curl http://localhost:8004/orders

# Create an order (requires all dependencies)
curl -X POST http://localhost:8004/order/create \
  -H 'Content-Type: application/json' \
  -d '{"user_id":1,"product_id":1,"quantity":2}'

# Get order stats (shows all dependent service statuses)
curl http://localhost:8004/stats
```

### Payment Service (Port 8005)

```bash
# Process payment
curl -X POST http://localhost:8005/process \
  -H 'Content-Type: application/json' \
  -d '{"user_id":1,"amount":99.99}'

# Get payment stats
curl http://localhost:8005/stats
```

## Failure Modes & Handling

### Auth Service Down
- **Impact**: User service becomes unavailable (direct dependency)
- **Order Service**: ✅ Still operational (different dependency path)
- **Blast Radius**: User service only

### Product Service Down
- **Impact**: Order service becomes unavailable (direct dependency)
- **User Service**: ✅ Still operational (independent)
- **Blast Radius**: Order service only

### Payment Service Down
- **Impact**: Order creation fails (direct dependency)
- **User/Product Services**: ✅ Still operational (independent)
- **Blast Radius**: Order creation only

### Order Service Down
- **Impact**: Gateway returns 503 for /orders routes
- **Other Services**: ✅ Still operational through gateway
- **Blast Radius**: Order operations only

## Key Configuration

### Docker Compose Dependency Ordering

```yaml
# Auth service - no dependencies, starts first
depends_on:
  postgres:
    condition: service_healthy
  redis:
    condition: service_healthy

# User service - waits for auth
depends_on:
  auth-service:
    condition: service_healthy

# Order service - waits for all its dependencies
depends_on:
  user-service:
    condition: service_healthy
  product-service:
    condition: service_healthy
  payment-service:
    condition: service_healthy
```

### Health Check Pattern (all services)

```yaml
healthcheck:
  test: ["CMD", "curl", "-f", "http://localhost:PORT/health"]
  interval: 5s
  timeout: 3s
  retries: 5
  start_period: 15s
```

### Database Connection Pooling

- Max connections: 25
- Idle connections: 5
- Connection lifetime: 5 minutes

## Scaling Concepts

### Horizontal Scaling
To scale a service (e.g., 2 user instances):

```bash
# Create docker-compose.override.yml
echo 'version: "3.8"
services:
  user-service-2:
    <<: *user-service
    ports: ["8012:8002"]
    container_name: dpivot-user-service-2' > docker-compose.override.yml

docker-compose up -d
```

### Load Balancing
Update the API Gateway to route to multiple instances:

```go
// In gateway/main.go
upstream := []string{
  "http://user-service:8002",
  "http://user-service-2:8002",
}
// Round-robin or least-connections
```

## Troubleshooting

### Services not starting
```bash
# Check logs
docker-compose logs -f

# Rebuild
docker-compose down -v
docker-compose build --no-cache
docker-compose up -d
```

### Health checks failing
```bash
# Check gateway health
curl http://localhost:3000/health | jq

# Check individual service
curl http://localhost:8001/health | jq

# Check database
docker-compose exec postgres pg_isready -U dpivot
```

### Service to service communication failing
```bash
# Test from inside a container
docker-compose exec user curl http://product-service:8003/health

# Check network
docker network ls
docker network inspect dpivot_dpivot-mesh
```

### Database issues
```bash
# Connect to database
docker-compose exec postgres psql -U dpivot -d microservices

# Check tables
\dt

# View data
SELECT * FROM users;
```

## Blast Radius Analysis

dpivot provides automatic blast radius analysis:

1. **Dependency Mapping**: Identifies which services depend on which
2. **Failure Detection**: Health checks identify failures in < 15 seconds
3. **Isolation**: Failing services are isolated from dependent services
4. **Recovery**: Services automatically resume when dependencies recover

### Blast Radius Example

If Product Service fails:
```
Product Service ❌
  ↓
Order Service (depends on Product) ❌
  ↓
Gateway (routes to Order) ⚠️ (returns 503 for /orders route)

But:
✅ User Service continues (no Product dependency)
✅ Auth Service continues (no Product dependency)
✅ Payment Service continues (no Product dependency)
✅ Gateway continues (other routes work)
```

## Next Steps

### Try extending this demo:

1. **Add Cache Layer**: Implement Redis caching in services
2. **Add Message Queue**: Use RabbitMQ for async processing
3. **Add Load Balancing**: Route to multiple service replicas
4. **Add Circuit Breaker**: Explicit circuit breaker pattern
5. **Add Tracing**: Request tracing across services

### For Production Deployment:

1. **Add TLS/mTLS**: Encrypt service-to-service communication
2. **Add Authentication**: API keys and token validation
3. **Add Rate Limiting**: Per-service rate limits
4. **Add Logging**: Centralized logging (ELK stack)
5. **Add Monitoring**: Prometheus/Grafana metrics
6. **Add Secrets Management**: HashiCorp Vault or similar

## Cleanup

```bash
./scripts/cleanup.sh
```

Or manually:
```bash
docker-compose down -v
```

---

**Key Takeaway**: dpivot enables zero-downtime deployments for microservices by ensuring health-driven traffic routing and automatic failure detection, preventing cascading failures and containing blast radius.

This demo shows dpivot's advantage: detect failures fast (via health checks) and contain them (via dependency isolation) without requiring Kubernetes orchestration complexity.
