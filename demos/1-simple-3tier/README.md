# DPIVOT Demo 1: Simple 3-Tier Application

A demonstration of **zero-downtime deployments** using dpivot with Docker.

## Architecture

```
┌─────────────────────────────────────────┐
│         Nginx Web Frontend              │
│    (React, auto-refreshing dashboard)   │
└──────────────────┬──────────────────────┘
                   │
┌──────────────────▼──────────────────────┐
│         Go API Server                   │
│  (REST API with health checks)          │
└──────────────────┬──────────────────────┘
                   │
┌──────────────────▼──────────────────────┐
│      PostgreSQL Database                │
│   (Users, Products, Orders tables)      │
└─────────────────────────────────────────┘
```

## Features

✅ Zero-downtime deployments  
✅ Health checks at each layer  
✅ Automatic rollback on failure  
✅ Live dashboard showing version & uptime  
✅ Load testing capabilities  
✅ Failure injection scenarios  

## Prerequisites

- Docker & Docker Compose
- bash shell
- curl (for load testing)

## Quick Start

### 1. Setup the demo

```bash
chmod +x scripts/*.sh
./scripts/setup.sh
```

This will:
- Build all services
- Start the 3-tier application
- Configure health checks
- Initialize the database

### 2. Access the application

- **Frontend**: http://localhost
- **API**: http://localhost:8080/health
- **Database**: localhost:5432 (psql client)

## Demo Scenarios

### Scenario 1: Zero-Downtime Deployment

**Terminal 1** - Start continuous load test:
```bash
./scripts/load-test.sh
```

**Terminal 2** - Deploy API v2:
```bash
./scripts/deploy-v2.sh
```

**Observation**:
- Load test continues with NO failed requests
- Frontend automatically shows v2 in the dashboard
- Zero downtime achieved!

### Scenario 2: Automatic Rollback

Deploy a broken version (simulated):
```bash
# This triggers health check failure → automatic rollback
./scripts/deploy-broken.sh
```

**Observation**:
- Deployment fails health checks
- Automatically rolls back to v1
- Service remains available

### Scenario 3: Cascading Failure Prevention

Simulate database failure:
```bash
docker-compose stop postgres
```

**Observation**:
- API health checks fail
- Circuit breaker opens
- Web frontend shows degraded status
- Requests fail gracefully instead of cascading

Recover:
```bash
docker-compose start postgres
```

## API Endpoints

### Health Check
```bash
curl http://localhost:8080/health
```

Response:
```json
{
  "status": "healthy",
  "version": "v1",
  "uptime": "5m30s",
  "database": "ok",
  "timestamp": "2024-01-15T10:30:00Z"
}
```

### Get Users
```bash
curl http://localhost:8080/api/users
```

### Get Products
```bash
curl http://localhost:8080/api/products
```

### Get Stats
```bash
curl http://localhost:8080/api/stats
```

## Deployment Flow

1. **Prepare**: Build new version (v2)
2. **Start**: Run v2 container alongside v1
3. **Validate**: Wait for health checks to pass
4. **Switch**: Direct traffic to v2
5. **Drain**: Let v1 finish existing requests
6. **Cleanup**: Remove v1 container

## Key Configuration

### Health Checks (docker-compose.yml)

```yaml
healthcheck:
  test: ["CMD", "curl", "-f", "http://localhost:8080/health"]
  interval: 5s
  timeout: 3s
  retries: 5
  start_period: 10s
```

### Database Connection Pool (API)

- Max connections: 25
- Idle connections: 5
- Connection timeout: 5m

### Load Balancing (Nginx)

```nginx
upstream api_backend {
    server api:8080;
}

proxy_pass http://api_backend/api/;
proxy_read_timeout 60s;
proxy_connect_timeout 5s;
```

## Troubleshooting

### Services not starting

```bash
# Check logs
docker-compose logs -f

# Rebuild everything
docker-compose down -v
docker-compose build --no-cache
docker-compose up -d
```

### Health checks failing

```bash
# Check individual service health
curl http://localhost/health
curl http://localhost:8080/health
docker-compose exec postgres pg_isready -U dpivot
```

### Database issues

```bash
# Connect to database
docker-compose exec postgres psql -U dpivot -d demo_app

# View tables
\dt

# View data
SELECT * FROM users;
```

## Next Steps

### Demo 2: Microservices Mesh
- 5+ services with dependencies
- Dependency graph visualization
- Circuit breaker demonstration

### Demo 3: Real-World E-Commerce
- Production-like complexity
- Failure scenarios
- Performance testing

## Key Takeaways

1. **Zero-Downtime Deployments**: No dropped requests during updates
2. **Health-Driven**: Validation before traffic switching
3. **Automatic Rollback**: Safety net for failed deployments
4. **Cascading Failure Prevention**: Blast radius containment
5. **Docker-Native**: Works with standard Docker tooling

## Architecture Decisions

### Why Go API?
- Fast, compiled binary
- Minimal resource footprint
- Easy to modify for failure scenarios

### Why PostgreSQL?
- Persistent data
- Real-world database behavior
- Health check verification

### Why React Frontend?
- Auto-refreshing dashboard
- Visual feedback on versions
- Real-time status monitoring

## Extending This Demo

### Add a Cache Layer
```bash
# Add Redis service
# Update API to use cache
# Demonstrate cache invalidation during deployments
```

### Add Message Queue
```bash
# Add RabbitMQ/Kafka
# Implement async processing
# Show graceful queue draining
```

### Add Load Balancer
```bash
# Add HAProxy/nginx upstream
# Multiple API replicas
# Demonstrate rolling deployments
```

## Cleanup

```bash
./scripts/cleanup.sh
```

Or manually:
```bash
docker-compose down -v
```

---

**Note**: This is a POC demo. For production use, additional hardening is needed:
- TLS/mTLS certificates
- Persistent volume management
- Resource limits & requests
- Network policies
- Secrets management
