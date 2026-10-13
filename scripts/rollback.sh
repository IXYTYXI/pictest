#!/usr/bin/env bash
set -euo pipefail
source "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
[[ -s .deploy/previous-image ]] || { echo 'No previous image is recorded.' >&2; exit 1; }
exec bash scripts/deploy.sh --image "$(cat .deploy/previous-image)"
