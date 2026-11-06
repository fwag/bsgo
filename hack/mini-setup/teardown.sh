#!/usr/bin/env bash
echo "Stopping bsgo local dependencies..."
podman rm -f bsgo-nats bsgo-postgres bsgo-garage 2>/dev/null || true
echo "✔ All local dependencies stopped and cleaned up."
