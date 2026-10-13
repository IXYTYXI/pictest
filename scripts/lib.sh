#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
[[ -f .env && -s .secrets/upload_token ]] || { echo 'Run scripts/init.sh first.' >&2; exit 1; }
command -v docker >/dev/null || { echo 'Docker with Compose is required.' >&2; exit 1; }
docker compose version >/dev/null
umask 077
mkdir -p .deploy
chmod 700 .deploy
deploy_mode=http
if [[ -f .deploy/mode ]]; then deploy_mode=$(cat .deploy/mode); fi
[[ $deploy_mode == http || $deploy_mode == https ]] || { echo 'Invalid saved deployment mode.' >&2; exit 1; }
if [[ -z ${UPIC_IMAGE:-} && -f .deploy/current-image ]]; then UPIC_IMAGE=$(cat .deploy/current-image); fi
export UPIC_IMAGE=${UPIC_IMAGE:-upic-server:local}
compose() {
  local args=(--env-file .env -f compose.yaml)
  if [[ $deploy_mode == https ]]; then args+=(--profile https); fi
  docker compose "${args[@]}" "$@"
}
lock_deployment() {
  mkdir .deploy/lock 2>/dev/null || { echo 'Another deployment/backup is active (.deploy/lock).' >&2; exit 1; }
}
save_state() {
  printf '%s\n' "$2" > ".deploy/$1.tmp"
  mv ".deploy/$1.tmp" ".deploy/$1"
}
wait_healthy() {
  local container=$1 status attempt
  for ((attempt=0; attempt<45; attempt++)); do
    status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$container") || return 1
    [[ $status == healthy ]] && return 0
    [[ $status != missing ]] || { echo 'Container has no healthcheck.' >&2; return 1; }
    sleep 2
  done
  echo 'Original container did not become healthy within 90 seconds.' >&2
  return 1
}
