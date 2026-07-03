#!/bin/bash

echo "🧹 Cleaning up DPIVOT Demo..."
echo ""

# Change to demo directory
DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DEMO_DIR"

# Stop and remove containers
echo "🛑 Stopping containers..."
docker-compose down -v

# Remove backup files created by deploy script
echo "🗑️  Removing backup files..."
rm -f docker-compose.yml.bak

echo "✅ Cleanup complete!"
echo ""
