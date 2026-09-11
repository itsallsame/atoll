#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${script_dir}/.." && pwd)

for command in docker mysql mysqldump go; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "recruiting joint restore: required command is missing: ${command}" >&2
    exit 1
  fi
done

echo "recruiting joint restore: revision=$(git -C "${repository_root}" rev-parse --short HEAD) planes=mysql,atoll-ledger,artifact"
cd "${repository_root}"
ATOLL_RECRUITING_JOINT_RESTORE=1 go test ./e2e \
  -run '^TestRecruitingJointColdRestoreAcrossDataPlanes$' -count=1 -v -timeout 10m
