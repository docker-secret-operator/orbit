# Orbit Docker Image Build Instructions

Build and push the Orbit Docker image to Docker Hub.

## Prerequisites

- Docker installed and running
- Docker Hub account logged in: `docker login`
- Repository created: `orbit/orbit`

## Build Steps

### 1. Build Locally (Test)

```bash
cd orbit

# Build with tag
docker build --no-cache -t orbit/orbit:latest -f docker/Dockerfile .

# Verify build succeeded
docker images | grep orbit/orbit
```

### 2. Test the Image

```bash
# Test help command
docker run --rm orbit/orbit:latest --help

# Test with specific command
docker run --rm orbit/orbit:latest deploy --help
```

### 3. Push to Docker Hub

```bash
# Login to Docker Hub (if not already logged in)
docker login

# Push image
docker push orbit/orbit:latest

# Verify on Docker Hub
# Visit: https://hub.docker.com/r/orbit/orbit
```

### 4. Test Installation

```bash
# Reinstall locally
sudo bash install.sh

# Verify
docker orbit --help
docker orbit deploy --help
```

## Complete Build + Push Script

```bash
#!/bin/bash
set -e

cd orbit

echo "Building Docker image..."
docker build --no-cache -t orbit/orbit:latest -f docker/Dockerfile .

echo "Testing image..."
docker run --rm orbit/orbit:latest --help > /dev/null

echo "Pushing to Docker Hub..."
docker push orbit/orbit:latest

echo "✓ Build and push complete!"
echo ""
echo "Next step: Install locally"
echo "  sudo bash install.sh"
```

## Troubleshooting

### Build fails: "no Go files in /app"
- Check that cmd/docker-orbit/main.go exists
- Run: `ls -la cmd/docker-orbit/main.go`

### Build fails: "cannot find package"
- Run: `go mod tidy` in the repo root
- Then retry build

### Image test fails: "command not found"
- Check: `docker run --rm orbit/orbit:latest --help`
- Should show Orbit help, not "proxy" help

### Installation fails after push
- Verify image exists: `docker pull orbit/orbit:latest`
- Check: `docker run --rm orbit/orbit:latest --help`
- Then: `sudo bash install.sh`

## Quick Checklist

- [ ] Docker is running
- [ ] Logged in to Docker Hub: `docker login`
- [ ] cd to orbit directory
- [ ] Build: `docker build --no-cache -t orbit/orbit:latest -f docker/Dockerfile .`
- [ ] Test: `docker run --rm orbit/orbit:latest --help` (should work)
- [ ] Push: `docker push orbit/orbit:latest`
- [ ] Reinstall: `sudo bash install.sh`
- [ ] Verify: `docker orbit --help` (should work)
