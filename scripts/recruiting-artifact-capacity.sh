#!/usr/bin/env bash
set -euo pipefail

level="${RECRUITING_ARTIFACT_CAPACITY_LEVEL:-A0}"
case "${level}" in
  A0|A1|A2) ;;
  *)
    echo "recruiting Artifact capacity: level must be one of A0, A1, A2" >&2
    exit 2
    ;;
esac
if [[ "${level}" == "A2" && "${RECRUITING_ARTIFACT_CAPACITY_LARGE_ACK:-}" != "local-artifact-large-load" ]]; then
  echo "recruiting Artifact capacity: A2 creates and verifies 1,000 response and derived Resources; set RECRUITING_ARTIFACT_CAPACITY_LARGE_ACK=local-artifact-large-load" >&2
  exit 2
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${script_dir}/.." && pwd)
manifest="${repository_root}/docs/experiments/workloads/recruiting-artifact-capacity-v1.json"
for command in docker jq go; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "recruiting Artifact capacity: required command is missing: ${command}" >&2
    exit 1
  fi
done
if ! jq -e --arg level "${level}" '.version == "recruiting.artifact-capacity-workload.v1" and (.levels[$level] != null)' \
  "${manifest}" >/dev/null; then
  echo "recruiting Artifact capacity: invalid workload manifest" >&2
  exit 1
fi

items=$(jq -r --arg level "${level}" '.levels[$level].items' "${manifest}")
payload_bytes=$(jq -r --arg level "${level}" '.levels[$level].payload_bytes' "${manifest}")
executors=$(jq -r --arg level "${level}" '.levels[$level].executors' "${manifest}")
timeout_seconds=$(jq -r --arg level "${level}" '.levels[$level].timeout_seconds' "${manifest}")

echo "recruiting Artifact capacity: level=${level} items=${items} payload_bytes=${payload_bytes} executors=${executors} revision=$(git -C "${repository_root}" rev-parse --short HEAD)"
cd "${repository_root}"
ATOLL_RECRUITING_ARTIFACT_CAPACITY=1 \
RECRUITING_ARTIFACT_CAPACITY_ITEMS="${items}" \
RECRUITING_ARTIFACT_CAPACITY_PAYLOAD_BYTES="${payload_bytes}" \
RECRUITING_ARTIFACT_CAPACITY_EXECUTORS="${executors}" \
RECRUITING_ARTIFACT_CAPACITY_TIMEOUT_SECONDS="${timeout_seconds}" \
  go test ./e2e -run '^TestRecruitingArtifactRecomputeCapacityThroughRealDataPlanes$' -count=1 -v \
  -timeout "$((timeout_seconds+120))s"
