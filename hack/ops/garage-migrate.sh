#!/bin/bash
# Move bsgo-garage's data (/var/lib/garage, today inside the container's
# writable layer) to a host bind mount, without losing objects.
#
# Usage:
#   garage-migrate.sh plan       list every object (key, size, etag) and the
#                                MD5 of a sample; changes nothing
#   garage-migrate.sh migrate    stop bsgo services, copy, recreate the container
#                                with the bind mount, verify; on any mismatch roll
#                                back to the old container
#   garage-migrate.sh finalize   remove the old container (only after checking)
#
# Rehearsed on a throwaway container (same image and garage.toml): objects,
# node id, layout, bucket and keys survive, also after recreating the container.
set -euo pipefail

NAME=${NAME:-bsgo-garage}
OLD=${OLD:-$NAME-old}
IMG=docker.io/dxflrs/garage:v1.0.1
CONF=${CONF:-$HOME/Devel/bsgo/hack/mini-setup/garage.toml}
DATA=${DATA:-$HOME/bsgo-data/garage}
W=${W:-$HOME/garage-migrate}
BUCKET=${BUCKET:-bsgo-artifacts}
PORT=${PORT:-3900}
ADMINPORT=${ADMINPORT:-3903}
CREDS=${CREDS:-$HOME/Devel/bsgo/config/s3-credentials.env}
SERVICES=${SERVICES-"bsgo-worker bsgo-worker-kvm bsgo-server"}
SAMPLE=400

mkdir -p "$W"
set -a; . "$CREDS"; set +a
export RCLONE_CONFIG_G_TYPE=s3 RCLONE_CONFIG_G_PROVIDER=Other \
  RCLONE_CONFIG_G_ENDPOINT=http://127.0.0.1:$PORT RCLONE_CONFIG_G_REGION=garage \
  RCLONE_CONFIG_G_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
  RCLONE_CONFIG_G_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
  RCLONE_CONFIG_G_FORCE_PATH_STYLE=true RCLONE_CONFIG_G_LIST_VERSION=2
log() { echo "[$(date +%T)] $*"; }

# listing: "<key> <size> <etag>" for every object, sorted
listing() {
  rclone lsf -R --files-only --format "psh" --hash MD5 --separator " " "g:$BUCKET" 2>/dev/null | sort
}
# content check: download the sample objects and hash their bytes
sample_md5() {
  while read -r key; do
    echo "$(rclone cat "g:$BUCKET/$key" 2>/dev/null | md5sum | cut -d' ' -f1) $key"
  done < "$W/sample.keys"
}
wait_garage() {
  for _ in $(seq 1 60); do
    curl -s -o /dev/null http://127.0.0.1:$PORT/ && podman exec "$NAME" /garage status >/dev/null 2>&1 && return 0
    sleep 2
  done
  return 1
}
compare() { # $1 = phase name
  listing > "$W/$1.list"
  sample_md5 > "$W/$1.sample"
  local ok=1
  if ! diff -q "$W/before.list" "$W/$1.list" >/dev/null; then
    log "MISMATCH in object listing:"; diff "$W/before.list" "$W/$1.list" | head -20; ok=0
  fi
  if ! diff -q "$W/before.sample" "$W/$1.sample" >/dev/null; then
    log "MISMATCH in sample content:"; diff "$W/before.sample" "$W/$1.sample" | head -20; ok=0
  fi
  # test hook (rehearsal only): force the verification to fail
  if [ -n "${FORCE_VERIFY_FAIL:-}" ] && [ "$1" = after ]; then log "FORCE_VERIFY_FAIL set: failing on purpose"; ok=0; fi
  [ $ok = 1 ] && log "$1: $(wc -l < "$W/$1.list") objects identical (key, size, etag); $(wc -l < "$W/$1.sample") sampled objects byte-identical"
  [ $ok = 1 ]
}
run_new() {
  podman run -d --name "$NAME" --restart=always -p $PORT:3900 -p $ADMINPORT:3903 \
    -v "$CONF:/etc/garage.toml:ro,Z" -v "$DATA:/var/lib/garage:Z" \
    "$IMG" /garage server >/dev/null
}

case "${1:-}" in
plan)
  log "listing all objects of $BUCKET"
  listing > "$W/before.list"
  shuf -n "$SAMPLE" --random-source=<(yes) "$W/before.list" | cut -d' ' -f1 > "$W/sample.keys"
  log "hashing the content of $SAMPLE sampled objects"
  sample_md5 > "$W/before.sample"
  log "objects: $(wc -l < "$W/before.list"), sample: $(wc -l < "$W/before.sample")"
  podman exec "$NAME" /garage node id 2>/dev/null | head -1 | cut -d@ -f1 > "$W/node.id"
  log "node id: $(cat "$W/node.id")"
  df -h "$HOME" | tail -1
  ;;
migrate)
  [ -s "$W/before.list" ] || { echo "run plan first"; exit 1; }
  podman container exists "$OLD" && { echo "$OLD exists: a previous migration is pending"; exit 1; }
  [ -e "$DATA" ] && { echo "$DATA already exists"; exit 1; }
  log "fresh listing before stopping (catches writes since plan)"
  listing > "$W/before.list"
  log "stopping $SERVICES"
  [ -n "$SERVICES" ] && systemctl --user stop $SERVICES
  log "re-listing with bsgo stopped (final reference)"
  listing > "$W/before.list"
  sample_md5 > "$W/before.sample"
  log "stopping $NAME"
  podman stop "$NAME" >/dev/null
  mkdir -p "$DATA"
  log "copying /var/lib/garage to $DATA"
  podman cp "$NAME:/var/lib/garage/." "$DATA/"
  du -sh "$DATA"
  podman rename "$NAME" "$OLD"
  log "starting $NAME with the bind mount"
  run_new
  if wait_garage && [ "$(podman exec "$NAME" /garage node id 2>/dev/null | head -1 | cut -d@ -f1)" = "$(cat "$W/node.id")" ] && compare after; then
    podman update --restart=no "$OLD" >/dev/null 2>&1 || true
    log "OK: data now in $DATA; starting $SERVICES"
    [ -n "$SERVICES" ] && systemctl --user start $SERVICES
    log "done. Old container kept as $OLD (stopped); remove it with: $0 finalize"
  else
    log "VERIFICATION FAILED: rolling back to the old container"
    podman rm -f "$NAME" >/dev/null 2>&1 || true
    podman rename "$OLD" "$NAME"
    podman start "$NAME" >/dev/null
    wait_garage && compare rollback || log "rollback check failed too: investigate before starting bsgo"
    log "bsgo services left STOPPED; $DATA left in place for inspection"
    exit 1
  fi
  ;;
finalize)
  podman container exists "$OLD" || { echo "no $OLD container"; exit 0; }
  podman inspect "$NAME" --format '{{range .Mounts}}{{.Destination}} {{end}}' | grep -q /var/lib/garage \
    || { echo "$NAME has no /var/lib/garage bind mount: refusing"; exit 1; }
  podman rm "$OLD"
  log "removed $OLD"
  ;;
*)
  sed -n '2,15p' "$0"; exit 1;;
esac
