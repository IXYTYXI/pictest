#!/usr/bin/env bash
set -euo pipefail
source "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
lock_deployment
stopped=0
backup_tmp=
cleanup() {
  local result=$?
  trap - EXIT
  [[ -z $backup_tmp ]] || rm -f -- "$backup_tmp"
  if [[ $stopped == 1 ]]; then
    # Restart the exact container. Applying edited .env/Compose settings here
    # could unexpectedly switch storage or images during a backup.
    if docker start "$container" >/dev/null; then wait_healthy "$container" || result=1; else result=1; fi
  fi
  rmdir .deploy/lock
  exit "$result"
}
trap cleanup EXIT
container=$(compose ps --quiet server)
[[ -n $container ]] || { echo 'Server must be running before backup.' >&2; exit 1; }
volume=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$container")
[[ -n $volume ]] || { echo 'Expected named data volume was not found.' >&2; exit 1; }
helper=${BACKUP_HELPER_IMAGE:-alpine:3.23}
docker image inspect "$helper" >/dev/null 2>&1 || docker pull "$helper"
mkdir -p backups
chmod 700 backups
backup="backups/upic-$(date -u +%Y%m%d%H%M%S)-$$.tar.gz"
backup_tmp="$backup.partial"
# Stop only after helper is available. The EXIT trap restarts on backup failure.
stopped=1
compose stop --timeout 30 server
docker run --rm --network none --read-only --cap-drop ALL --user 10001:10001 --mount "type=volume,src=$volume,dst=/data,readonly" "$helper" tar -C /data -czf - . > "$backup_tmp"
gzip -t "$backup_tmp"
mv "$backup_tmp" "$backup"
backup_tmp=
echo "Backup saved: $backup"
echo 'Store .env and .secrets/upload_token separately in a protected location.'
