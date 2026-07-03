#!/bin/bash

echo "🔥 DPIVOT Demo - Load Testing"
echo "============================="
echo ""
echo "Generating continuous traffic to the API..."
echo "Keep this running while deploying to see zero downtime!"
echo ""

# Ensure curl is available
if ! command -v curl &> /dev/null; then
    echo "❌ curl is not installed. Please install curl first."
    exit 1
fi

# Start timestamp
START=$(date +%s)
REQUEST_COUNT=0
FAILED_COUNT=0

# Function to handle interrupts
cleanup() {
    END=$(date +%s)
    DURATION=$((END - START))
    RPS=$(echo "scale=2; $REQUEST_COUNT / $DURATION" | bc 2>/dev/null || echo "N/A")

    echo ""
    echo "📊 Load Test Summary"
    echo "==================="
    echo "Total Requests: $REQUEST_COUNT"
    echo "Failed Requests: $FAILED_COUNT"
    echo "Duration: ${DURATION}s"
    echo "Requests/sec: $RPS"

    if [ $FAILED_COUNT -eq 0 ]; then
        echo "✅ All requests succeeded!"
    else
        echo "⚠️  Some requests failed during deployment"
    fi

    exit 0
}

trap cleanup SIGINT SIGTERM

# Main load test loop
while true; do
    {
        RESPONSE=$(curl -s -w "\n%{http_code}" http://localhost/api/users 2>/dev/null)
        HTTP_CODE=$(echo "$RESPONSE" | tail -n1)

        if [ "$HTTP_CODE" = "200" ]; then
            echo "✓ $(date '+%H:%M:%S') - API responding (status: $HTTP_CODE)"
            ((REQUEST_COUNT++))
        else
            echo "✗ $(date '+%H:%M:%S') - API error (status: $HTTP_CODE)"
            ((FAILED_COUNT++))
            ((REQUEST_COUNT++))
        fi
    } &

    # Limit concurrent requests
    if [ $((REQUEST_COUNT % 5)) -eq 0 ]; then
        wait
    fi

    sleep 0.5
done
