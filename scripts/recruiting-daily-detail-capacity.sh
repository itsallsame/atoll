#!/usr/bin/env bash
set -euo pipefail

level="${RECRUITING_DAILY_DETAIL_CAPACITY_LEVEL:-D0}"
failure_matrix="${RECRUITING_DAILY_DETAIL_FAILURE_MATRIX:-0}"
artifact_recovery="${RECRUITING_DAILY_ARTIFACT_RECOVERY:-0}"
server_cutoff_recovery="${RECRUITING_DAILY_SERVER_CUTOFF_RECOVERY:-0}"
case "${level}" in
  D0|D1|D2|D3) ;;
  *)
    echo "recruiting daily Detail capacity: level must be one of D0, D1, D2, D3" >&2
    exit 2
    ;;
esac
if [[ "${failure_matrix}" != "0" && "${failure_matrix}" != "1" ]]; then
  echo "recruiting daily Detail capacity: RECRUITING_DAILY_DETAIL_FAILURE_MATRIX must be 0 or 1" >&2
  exit 2
fi
if [[ "${artifact_recovery}" != "0" && "${artifact_recovery}" != "1" ]]; then
  echo "recruiting daily Detail capacity: RECRUITING_DAILY_ARTIFACT_RECOVERY must be 0 or 1" >&2
  exit 2
fi
if [[ "${server_cutoff_recovery}" != "0" && "${server_cutoff_recovery}" != "1" ]]; then
  echo "recruiting daily Detail capacity: RECRUITING_DAILY_SERVER_CUTOFF_RECOVERY must be 0 or 1" >&2
  exit 2
fi
if (( failure_matrix + artifact_recovery + server_cutoff_recovery > 1 )); then
  echo "recruiting daily Detail capacity: failure matrix, Artifact recovery, and Server cutoff recovery are separate fault axes" >&2
  exit 2
fi
if [[ "${failure_matrix}" == "1" && "${level}" != "D0" ]]; then
  echo "recruiting daily Detail capacity: the failure matrix uses the bounded D0 workload" >&2
  exit 2
fi
if [[ "${artifact_recovery}" == "1" && "${level}" != "D0" ]]; then
  echo "recruiting daily Detail capacity: Artifact recovery uses the bounded D0 workload" >&2
  exit 2
fi
if [[ "${server_cutoff_recovery}" == "1" && "${level}" != "D0" ]]; then
  echo "recruiting daily Detail capacity: Server cutoff recovery uses the bounded D0 workload" >&2
  exit 2
fi
if [[ "${level}" == "D2" && "${RECRUITING_DAILY_DETAIL_CAPACITY_LARGE_ACK:-}" != "isolated-daily-detail-large-load" ]]; then
  echo "recruiting daily Detail capacity: D2 captures 1,000 responses; set RECRUITING_DAILY_DETAIL_CAPACITY_LARGE_ACK=isolated-daily-detail-large-load" >&2
  exit 2
fi
if [[ "${level}" == "D3" && "${RECRUITING_DAILY_DETAIL_CAPACITY_PEAK_ACK:-}" != "isolated-daily-detail-five-times-peak" ]]; then
  echo "recruiting daily Detail capacity: D3 captures 5,000 responses; set RECRUITING_DAILY_DETAIL_CAPACITY_PEAK_ACK=isolated-daily-detail-five-times-peak" >&2
  exit 2
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${script_dir}/.." && pwd)
manifest="${repository_root}/docs/experiments/workloads/recruiting-daily-detail-capacity-v1.json"
for command in docker jq go curl date; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "recruiting daily Detail capacity: required command is missing: ${command}" >&2
    exit 1
  fi
done
if ! jq -e --arg level "${level}" '.version == "recruiting.daily-detail-capacity-workload.v1" and (.levels[$level] != null)' \
  "${manifest}" >/dev/null; then
  echo "recruiting daily Detail capacity: invalid workload manifest" >&2
  exit 1
fi

items=$(jq -r --arg level "${level}" '.levels[$level].items' "${manifest}")
payload_bytes=$(jq -r --arg level "${level}" '.levels[$level].payload_bytes' "${manifest}")
latency_ms=$(jq -r --arg level "${level}" '.levels[$level].origin_latency_ms' "${manifest}")
listing_latency_ms="${latency_ms}"
if [[ "${artifact_recovery}" == "1" ]]; then
  # Leave a deterministic interval after the HTTP request starts in which the
  # test can kill the already-initialized, separate File provider process.
  listing_latency_ms=5000
fi
executors=$(jq -r --arg level "${level}" '.levels[$level].executors' "${manifest}")
timeout_seconds=$(jq -r --arg level "${level}" '.levels[$level].timeout_seconds' "${manifest}")
activity_unix=$(date +%s)

run_id="$$-$(date +%s)"
origin_build_dir=$(mktemp -d)
origin_binary="${origin_build_dir}/recruiting-origin"
origin_network="atoll-recruiting-daily-origin-${run_id}"
origin_container="atoll-recruiting-daily-origin-${run_id}"
subnet_octet=$(( ($$ % 180) + 20 ))
origin_subnet="11.253.${subnet_octet}.0/24"
origin_ip="11.253.${subnet_octet}.10"
origin_url="http://${origin_ip}:8080"
cleanup() {
  docker stop "${origin_container}" >/dev/null 2>&1 || true
  docker network rm "${origin_network}" >/dev/null 2>&1 || true
  unlink "${origin_binary}" >/dev/null 2>&1 || true
  rmdir "${origin_build_dir}" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

cd "${repository_root}"
CGO_ENABLED=0 go build -o "${origin_binary}" ./e2e/fixtures/recruitingorigin
docker network create --internal --subnet "${origin_subnet}" "${origin_network}" >/dev/null
if [[ "$(docker network inspect --format '{{.Internal}}' "${origin_network}")" != "true" ]]; then
  echo "recruiting daily Detail capacity: controlled origin network is not internal" >&2
  exit 1
fi
docker run -d --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  --name "${origin_container}" --network "${origin_network}" --ip "${origin_ip}" \
  -v "${origin_binary}:/recruiting-origin:ro" --entrypoint /recruiting-origin \
  -e RECRUITING_ORIGIN_PAYLOAD_BYTES="${payload_bytes}" \
  -e RECRUITING_ORIGIN_LATENCY_MS="${latency_ms}" \
  -e RECRUITING_ORIGIN_LISTING_LATENCY_MS="${listing_latency_ms}" \
  -e RECRUITING_ORIGIN_LISTING_ITEMS="${items}" \
  -e RECRUITING_ORIGIN_FAILURE_MATRIX="${failure_matrix}" \
  -e RECRUITING_ORIGIN_ACTIVITY_UNIX="${activity_unix}" mysql:8.4 >/dev/null
ready=false
for _ in $(seq 1 80); do
  if curl --noproxy '*' --fail --silent "${origin_url}/health" >/dev/null; then
    ready=true
    break
  fi
  sleep 0.25
done
if [[ "${ready}" != "true" ]]; then
  echo "recruiting daily Detail capacity: controlled origin failed to start" >&2
  docker logs "${origin_container}" >&2 || true
  exit 1
fi

test_name='TestRecruitingScheduledDailyDetailCapacityThroughRealDataPlanes'
if [[ "${failure_matrix}" == "1" ]]; then
  test_name='TestRecruitingScheduledDailyDetailFailureMatrixThroughRealDataPlanes'
fi
if [[ "${artifact_recovery}" == "1" ]]; then
  test_name='TestRecruitingScheduledDailyArtifactProviderRecoveryThroughRealDataPlanes'
fi
if [[ "${server_cutoff_recovery}" == "1" ]]; then
  test_name='TestRecruitingScheduledDailyServerCutoffRecoveryThroughRealDataPlanes'
fi
echo "recruiting daily Detail capacity: level=${level} failure_matrix=${failure_matrix} artifact_recovery=${artifact_recovery} server_cutoff_recovery=${server_cutoff_recovery} items=${items} payload_bytes=${payload_bytes} latency_ms=${latency_ms} listing_latency_ms=${listing_latency_ms} executors=${executors} isolated_origin=${origin_url} revision=$(git -C "${repository_root}" rev-parse --short HEAD)"
set +e
ATOLL_RECRUITING_DAILY_DETAIL_CAPACITY=1 \
ATOLL_RECRUITING_DAILY_DETAIL_FAILURE_MATRIX="${failure_matrix}" \
ATOLL_RECRUITING_DAILY_ARTIFACT_RECOVERY="${artifact_recovery}" \
ATOLL_RECRUITING_DAILY_SERVER_CUTOFF_RECOVERY="${server_cutoff_recovery}" \
RECRUITING_DAILY_DETAIL_CAPACITY_ITEMS="${items}" \
RECRUITING_DAILY_DETAIL_CAPACITY_PAYLOAD_BYTES="${payload_bytes}" \
RECRUITING_DAILY_DETAIL_CAPACITY_EXECUTORS="${executors}" \
RECRUITING_DAILY_DETAIL_CAPACITY_TIMEOUT_SECONDS="${timeout_seconds}" \
RECRUITING_DAILY_DETAIL_CAPACITY_ORIGIN="${origin_url}" \
RECRUITING_DAILY_DETAIL_CAPACITY_ACTIVITY_UNIX="${activity_unix}" \
  go test ./e2e -run "^${test_name}$" -count=1 -v \
  -timeout "$((timeout_seconds+120))s"
test_status=$?
set -e
if [[ ${test_status} -ne 0 ]]; then
  docker logs "${origin_container}" >&2 || true
fi
exit ${test_status}
