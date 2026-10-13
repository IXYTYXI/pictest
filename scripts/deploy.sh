#!/usr/bin/env bash
set -euo pipefail
source "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
image_override=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --https) deploy_mode=https; shift ;;
    --image) [[ $# -ge 2 && -n $2 ]] || exit 2; image_override=$2; shift 2 ;;
    *) echo 'Usage: scripts/deploy.sh [--https] [--image IMAGE]' >&2; exit 2 ;;
  esac
done
lock_deployment
trap 'rmdir .deploy/lock' EXIT
compose config --quiet
docker info >/dev/null
old_container=$(compose ps --all --quiet server)
old_image=
if [[ -s .deploy/current-image ]]; then
  # A stopped/unhealthy container can be an interrupted candidate. Only the
  # persisted successful release is a trustworthy rollback target.
  old_image=$(cat .deploy/current-image)
  docker image inspect "$old_image" >/dev/null || { echo 'Last successful image is missing; restore it before deploying.' >&2; exit 1; }
elif [[ -n $old_container && $(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$old_container") == healthy ]]; then
  old_id=$(docker inspect --format '{{.Image}}' "$old_container")
  old_image="upic-server:rollback-$(date -u +%Y%m%d%H%M%S)-$$"
  docker image tag "$old_id" "$old_image"
  save_state current-image "$old_image"
fi
release_image="upic-server:release-$(date -u +%Y%m%d%H%M%S)-$$"
if [[ -n $image_override ]]; then
  docker image inspect "$image_override" >/dev/null 2>&1 || docker pull "$image_override"
  # Snapshot mutable registry tags so a future pull cannot replace the saved
  # image underneath a previously successful deployment.
  docker image tag "$image_override" "$release_image"
  export UPIC_IMAGE=$release_image
else
  export UPIC_IMAGE=$release_image
  compose build server
fi
if [[ $deploy_mode == https ]]; then compose pull caddy; fi
if ! compose up --detach --no-build --wait --wait-timeout 90; then
  echo 'Deployment failed health checks.' >&2
  if [[ -n $old_image ]]; then
    export UPIC_IMAGE=$old_image
    if compose up --detach --no-build --wait --wait-timeout 90; then
      save_state current-image "$old_image"
      echo 'Previous application image restored. Check configuration and logs before retrying.' >&2
    else
      echo 'Automatic image rollback also failed; inspect docker compose logs.' >&2
    fi
  fi
  exit 1
fi
if [[ -n $old_image ]]; then save_state previous-image "$old_image"; fi
save_state current-image "$UPIC_IMAGE"
save_state mode "$deploy_mode"
echo "Deployment healthy: $UPIC_IMAGE"
echo 'For HTTPS, also verify the public URL after DNS and certificate issuance complete.'
