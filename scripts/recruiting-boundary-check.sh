#!/usr/bin/env bash
set -euo pipefail

base_ref=${1:-HEAD}

if ! git rev-parse --verify "${base_ref}^{commit}" >/dev/null 2>&1; then
  echo "recruiting boundary: unknown base ref: ${base_ref}" >&2
  exit 2
fi

changed=$(
  {
    git diff --name-only "${base_ref}" --
    git ls-files --others --exclude-standard
  } | sort -u
)

violations=$(printf '%s\n' "${changed}" | awk '
  /^(protocol|runtime|lib|platform|registry)\// { print }
')

if [[ -n "${violations}" ]]; then
  echo "recruiting boundary: Atoll core is frozen; forbidden changes:" >&2
  printf '  %s\n' ${violations} >&2
  exit 1
fi

echo "recruiting boundary: ok (base=${base_ref})"
