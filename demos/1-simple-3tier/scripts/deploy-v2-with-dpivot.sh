#!/bin/bash

set -e

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DEMO_DIR"

echo "🚀 DPIVOT - Zero-Downtime Deployment with dpivot Plugin"
echo "======================================================"
echo ""

# Check if dpivot plugin is installed
if ! docker dpivot version &> /dev/null; then
    echo "❌ dpivot plugin is not installed"
    echo ""
    echo "Install it with:"
    echo "  cd plugin/"
    echo "  make install"
    echo ""
    exit 1
fi

echo "✓ dpivot plugin is installed: $(docker dpivot version)"
echo ""

# Show current status before deployment
echo "📊 Current system status:"
docker dpivot status
echo ""

# Ask for confirmation
read -p "Ready to deploy? (y/n) " -n 1 -r
echo ""
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Deployment cancelled"
    exit 0
fi

echo ""
echo "🚀 Starting zero-downtime deployment for api service..."
echo ""

# Deploy using dpivot (actual command is rollout)
docker dpivot rollout api --timeout 120s

echo ""
echo "📊 Post-deployment status:"
docker dpivot status
echo ""

echo "✅ Deployment complete!"
echo ""
echo "💡 Next steps:"
echo "  # Monitor status"
echo "  docker dpivot status"
echo ""
echo "  # Rollback if needed"
echo "  docker dpivot rollback api"
echo ""
