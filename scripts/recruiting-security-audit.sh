#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${script_dir}/.." && pwd)
cd "${repository_root}"

findings=$(mktemp /tmp/atoll-recruiting-security-audit.XXXXXX)
cleanup() {
  rm -f "${findings}"
}
trap cleanup EXIT

# These are deliberately high-confidence credential formats. Generic words
# such as password/token are common in negative tests and are not evidence of
# a committed credential. A release environment must still run its dedicated
# history and secret-provider scanner in addition to this repository gate.
credential_pattern='-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----|AKIA[0-9A-Z]{16}|github_pat_[A-Za-z0-9_]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|sk_live_[A-Za-z0-9]{16,}|AIza[0-9A-Za-z_-]{20,}'
if git grep --untracked -n -I -E -e "${credential_pattern}" -- . \
  ':(exclude)scripts/recruiting-security-audit.sh' >"${findings}"; then
  echo "recruiting security: high-confidence credential material found" >&2
  cat "${findings}" >&2
  exit 1
fi

# Evidence and workload reports are intended for source control and must not
# carry even named credential payloads. Opaque secret:// references belong in
# MySQL control facts and are also forbidden from these published reports.
evidence_pattern='"(authorization|cookie|set-cookie|password|secret|secret_ref|credential|credentials)"[[:space:]]*:'
if git grep --untracked -n -I -i -E -e "${evidence_pattern}" -- \
  docs/experiments/evidence docs/experiments/workloads >"${findings}"; then
  echo "recruiting security: published evidence contains a credential-shaped field" >&2
  cat "${findings}" >&2
  exit 1
fi
if git grep --untracked -n -I -F -e 'secret://' -- \
  docs/experiments/evidence docs/experiments/workloads >"${findings}"; then
  echo "recruiting security: published evidence contains an opaque secret reference" >&2
  cat "${findings}" >&2
  exit 1
fi

echo "recruiting security: repository high-confidence scan ok"
