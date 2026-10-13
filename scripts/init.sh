#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
umask 077
public_url=http://localhost:8080
if [[ $# -gt 0 ]]; then
  [[ $# == 2 && $1 == --public-url ]] || { echo 'Usage: scripts/init.sh [--public-url https://images.example.com]' >&2; exit 2; }
  public_url=${2%/}
fi
[[ $public_url =~ ^https?://[a-zA-Z0-9.-]+(:[0-9]{1,5})?$ ]] || { echo 'PUBLIC_BASE_URL must be an http(s) origin without a path.' >&2; exit 2; }
[[ ! -L .env && ! -L .secrets ]] || { echo 'Refusing symlinked configuration.' >&2; exit 1; }
mkdir -p .secrets .deploy
chmod 700 .secrets .deploy
[[ ! -L .secrets/upload_token ]] || { echo 'Refusing symlinked token.' >&2; exit 1; }
if [[ ! -e .secrets/upload_token ]]; then
  command -v openssl >/dev/null || { echo 'OpenSSL is required to generate an upload token.' >&2; exit 1; }
  token_tmp=$(mktemp .secrets/token.XXXXXX)
  trap 'rm -f -- "${token_tmp:-}"' EXIT
  openssl rand -hex 32 > "$token_tmp"
  # Docker Compose local secrets preserve host file modes. The parent directory
  # is owner-only; the mounted file must be readable by container UID 10001.
  chmod 444 "$token_tmp"
  ln "$token_tmp" .secrets/upload_token
  rm -f -- "$token_tmp"
fi
[[ -s .secrets/upload_token ]] || { echo 'Existing upload token is empty; refusing to overwrite it.' >&2; exit 1; }
if [[ ! -e .env ]]; then
  env_tmp=$(mktemp .deploy/env.XXXXXX)
  sed -e "s|^PUBLIC_BASE_URL=.*|PUBLIC_BASE_URL=$public_url|" -e "s|^SITE_ADDRESS=.*|SITE_ADDRESS=${public_url#*://}|" .env.example > "$env_tmp"
  ln "$env_tmp" .env
  rm -f -- "$env_tmp"
else
  echo 'Existing .env preserved; edit it to change the public URL.'
fi
echo 'Configuration ready. Upload token: .secrets/upload_token (not printed).'
