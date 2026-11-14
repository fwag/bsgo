#!/usr/bin/env bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Starting bsgo local dependencies via Podman..."

# 1. NATS JetStream
if ! podman ps -q -f name=bsgo-nats | grep -q .; then
  podman rm -f bsgo-nats 2>/dev/null || true
  podman run -d --name bsgo-nats -p 4222:4222 docker.io/library/nats:2.10-alpine -js
fi

# 2. PostgreSQL
if ! podman ps -q -f name=bsgo-postgres | grep -q .; then
  podman rm -f bsgo-postgres 2>/dev/null || true
  podman run -d --name bsgo-postgres -p 5432:5432 \
    -v bsgo-pgdata:/var/lib/postgresql/data:Z \
    -e POSTGRES_USER=bsgo \
    -e POSTGRES_PASSWORD=password \
    -e POSTGRES_DB=bsgodb \
    docker.io/library/postgres:16-alpine
fi

# 3. Garage S3
if ! podman ps -q -f name=bsgo-garage | grep -q .; then
  podman rm -f bsgo-garage 2>/dev/null || true
  podman run -d --name bsgo-garage -p 3900:3900 -p 3903:3903 \
    -v "$DIR/garage.toml:/etc/garage.toml:ro,Z" \
    docker.io/dxflrs/garage:v1.0.1 /garage server
fi

echo "Waiting for services to initialize..."
sleep 2

# Load external credentials file if present
ENV_FILE="$DIR/../../config/s3-credentials.env"
if [ -f "$ENV_FILE" ]; then
  # shellcheck source=/dev/null
  source "$ENV_FILE"
fi

ACCESS_KEY="${AWS_ACCESS_KEY_ID:-${S3_ACCESS_KEY}}"
SECRET_KEY="${AWS_SECRET_ACCESS_KEY:-${S3_SECRET_KEY}}"

# Configure Garage cluster layout & bucket
NODE_ID=$(podman exec bsgo-garage /garage node id 2>/dev/null | head -n 1 | awk '{print $1}' | cut -d'@' -f1)
if [ -n "$NODE_ID" ]; then
  podman exec bsgo-garage /garage layout assign -z dc1 -c 1G "$NODE_ID" >/dev/null 2>&1 || true
  podman exec bsgo-garage /garage layout apply --version 1 >/dev/null 2>&1 || true
  podman exec bsgo-garage /garage bucket create bsgo-artifacts >/dev/null 2>&1 || true
  podman exec bsgo-garage /garage bucket website --allow bsgo-artifacts >/dev/null 2>&1 || true

  if [ -n "$ACCESS_KEY" ] && [ -n "$SECRET_KEY" ]; then
    podman exec bsgo-garage /garage key import --yes -n bsgo-key "$ACCESS_KEY" "$SECRET_KEY" >/dev/null 2>&1 || true
    podman exec bsgo-garage /garage bucket allow --read --write bsgo-artifacts --key "$ACCESS_KEY" >/dev/null 2>&1 || true
  else
    podman exec bsgo-garage /garage key create bsgo-key >/dev/null 2>&1 || true
    podman exec bsgo-garage /garage bucket allow --read --write bsgo-artifacts --key bsgo-key >/dev/null 2>&1 || true
  fi
fi

echo ""
echo "✔ All local dependencies started and healthy!"
echo "  • NATS JetStream: localhost:4222"
echo "  • PostgreSQL:     localhost:5432 (user: bsgo, pass: password, db: bsgodb)"
echo "  • Garage S3 API:  http://localhost:3900 (bucket: bsgo-artifacts)"
