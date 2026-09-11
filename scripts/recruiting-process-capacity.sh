#!/usr/bin/env bash
set -euo pipefail

level="${RECRUITING_PROCESS_CAPACITY_LEVEL:-P0}"
case "${level}" in
  P0|P1|P2) ;;
  *)
    echo "recruiting process capacity: level must be one of P0, P1, P2" >&2
    exit 2
    ;;
esac
if [[ "${level}" == "P2" && "${RECRUITING_PROCESS_CAPACITY_LARGE_ACK:-}" != "local-only-large-load" ]]; then
  echo "recruiting process capacity: P2 creates 10,000 Companies through real processes; set RECRUITING_PROCESS_CAPACITY_LARGE_ACK=local-only-large-load" >&2
  exit 2
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${script_dir}/.." && pwd)
manifest="${repository_root}/docs/experiments/workloads/recruiting-process-capacity-v1.json"
for command in docker jq go; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "recruiting process capacity: required command is missing: ${command}" >&2
    exit 1
  fi
done
if ! jq -e --arg level "${level}" '.version == "recruiting.process-capacity-workload.v1" and (.levels[$level] != null)' \
  "${manifest}" >/dev/null; then
  echo "recruiting process capacity: invalid workload manifest" >&2
  exit 1
fi

imports=$(jq -r --arg level "${level}" '.levels[$level].imports' "${manifest}")
rows=$(jq -r --arg level "${level}" '.levels[$level].rows_per_import' "${manifest}")
executors=$(jq -r --arg level "${level}" '.levels[$level].executors' "${manifest}")
timeout_seconds=$(jq -r --arg level "${level}" '.levels[$level].timeout_seconds' "${manifest}")

echo "recruiting process capacity: level=${level} imports=${imports} rows_per_import=${rows} executors=${executors} revision=$(git -C "${repository_root}" rev-parse --short HEAD)"
cd "${repository_root}"
ATOLL_RECRUITING_PROCESS_CAPACITY=1 \
RECRUITING_PROCESS_CAPACITY_IMPORTS="${imports}" \
RECRUITING_PROCESS_CAPACITY_ROWS="${rows}" \
RECRUITING_PROCESS_CAPACITY_EXECUTORS="${executors}" \
RECRUITING_PROCESS_CAPACITY_TIMEOUT_SECONDS="${timeout_seconds}" \
  go test ./e2e -run '^TestRecruitingProcessCapacityThroughRealDataPlanes$' -count=1 -v \
  -timeout "$((timeout_seconds+120))s"
