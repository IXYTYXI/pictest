#!/usr/bin/env bash
set -euo pipefail
source "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
[[ $# == 2 ]] || { echo 'Usage: scripts/restore.sh BACKUP.tar.gz NEW_VOLUME_NAME' >&2; exit 2; }
archive=$1
new_volume=$2
[[ -f $archive && $new_volume =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]+$ ]] || { echo 'Invalid backup file or volume name.' >&2; exit 2; }
lock_deployment
trap 'rmdir .deploy/lock' EXIT
docker info >/dev/null
if docker volume inspect "$new_volume" >/dev/null 2>&1; then
  echo 'Restore requires a new volume; existing volumes are never overwritten.' >&2
  exit 1
fi
gzip -t "$archive"
helper=${BACKUP_HELPER_IMAGE:-alpine:3.23}
docker image inspect "$helper" >/dev/null 2>&1 || docker pull "$helper"
docker volume create "$new_volume" >/dev/null
docker run --rm --network none --read-only --cap-drop ALL --cap-add CHOWN --user 0:0 --mount "type=volume,src=$new_volume,dst=/data" "$helper" chown 10001:10001 /data
docker run --rm -i --network none --read-only --cap-drop ALL --user 10001:10001 --mount "type=volume,src=$new_volume,dst=/data" "$helper" tar -C /data -xzf - < "$archive"
echo "Restored to new volume: $new_volume"
echo "Set DATA_VOLUME_NAME=$new_volume in .env, then run scripts/deploy.sh --image YOUR_CURRENT_IMAGE."
echo 'The running server and its original volume have not been changed.'
