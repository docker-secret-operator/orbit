#!/bin/bash
# Orbit Docker Entrypoint
# Simple passthrough to docker-orbit binary

set -e

# Forward all arguments to docker-orbit
exec /usr/local/bin/docker-orbit "$@"
