#!/usr/bin/env bash
set -euo pipefail

level="${RECRUITING_CAPACITY_LEVEL:-L0}"
case "${level}" in
  L0|L1|L2|L3|L4) ;;
  *)
    echo "recruiting capacity: level must be one of L0, L1, L2, L3, L4" >&2
    exit 2
    ;;
esac

if [[ "${level}" == "L2" || "${level}" == "L3" || "${level}" == "L4" ]]; then
  if [[ "${RECRUITING_CAPACITY_LARGE_ACK:-}" != "local-only-large-load" ]]; then
    echo "recruiting capacity: ${level} is a large local database load; set RECRUITING_CAPACITY_LARGE_ACK=local-only-large-load" >&2
    exit 2
  fi
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${script_dir}/.." && pwd)
manifest="${repository_root}/docs/experiments/workloads/recruiting-capacity-v1.json"
if [[ ! -f "${manifest}" ]]; then
  echo "recruiting capacity: workload manifest is missing" >&2
  exit 1
fi

export RECRUITING_CAPACITY_LEVEL="${level}"
export RECRUITING_MYSQL_TEST_RUN='^TestDailyCapacityWorkload$'
export RECRUITING_MYSQL_TEST_VERBOSE=1
case "${level}" in
  L0|L1|L2) export RECRUITING_MYSQL_TMPFS_SIZE=4g ;;
  L3|L4) export RECRUITING_MYSQL_TMPFS_SIZE=8g ;;
esac

echo "recruiting capacity: level=${level} mysql_tmpfs=${RECRUITING_MYSQL_TMPFS_SIZE} manifest=${manifest} revision=$(git -C "${repository_root}" rev-parse --short HEAD)"
"${script_dir}/recruiting-mysql-test.sh"
