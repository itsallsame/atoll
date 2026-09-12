#!/usr/bin/env bash
set -euo pipefail

level="${RECRUITING_HTTP_CAPACITY_LEVEL:-H0}"
case "${level}" in
  H0|H1|H2|H3) ;;
  *)
    echo "recruiting HTTP capacity: level must be one of H0, H1, H2, H3" >&2
    exit 2
    ;;
esac
if [[ "${level}" == "H2" && "${RECRUITING_HTTP_CAPACITY_LARGE_ACK:-}" != "isolated-http-large-load" ]]; then
  echo "recruiting HTTP capacity: H2 captures 1,000 responses; set RECRUITING_HTTP_CAPACITY_LARGE_ACK=isolated-http-large-load" >&2
  exit 2
fi
if [[ "${level}" == "H3" && "${RECRUITING_HTTP_CAPACITY_PEAK_ACK:-}" != "isolated-http-five-times-peak" ]]; then
  echo "recruiting HTTP capacity: H3 captures 5,000 responses; set RECRUITING_HTTP_CAPACITY_PEAK_ACK=isolated-http-five-times-peak" >&2
  exit 2
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${script_dir}/.." && pwd)
manifest="${repository_root}/docs/experiments/workloads/recruiting-http-capacity-v1.json"
for command in docker jq go curl; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "recruiting HTTP capacity: required command is missing: ${command}" >&2
    exit 1
  fi
done
if ! jq -e --arg level "${level}" '.version == "recruiting.http-capacity-workload.v1" and (.levels[$level] != null)' \
  "${manifest}" >/dev/null; then
  echo "recruiting HTTP capacity: invalid workload manifest" >&2
  exit 1
fi

items=$(jq -r --arg level "${level}" '.levels[$level].items' "${manifest}")
payload_bytes=$(jq -r --arg level "${level}" '.levels[$level].payload_bytes' "${manifest}")
latency_ms=$(jq -r --arg level "${level}" '.levels[$level].origin_latency_ms' "${manifest}")
executors=$(jq -r --arg level "${level}" '.levels[$level].executors' "${manifest}")
timeout_seconds=$(jq -r --arg level "${level}" '.levels[$level].timeout_seconds' "${manifest}")

run_id="$$-$(date +%s)"
origin_build_dir=$(mktemp -d)
origin_binary="${origin_build_dir}/recruiting-origin"
origin_network="atoll-recruiting-origin-${run_id}"
origin_container="atoll-recruiting-origin-${run_id}"
subnet_octet=$(( ($$ % 180) + 20 ))
origin_subnet="11.254.${subnet_octet}.0/24"
origin_ip="11.254.${subnet_octet}.10"
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
  echo "recruiting HTTP capacity: controlled origin network is not internal" >&2
  exit 1
fi
docker run -d --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  --name "${origin_container}" --network "${origin_network}" --ip "${origin_ip}" \
  -v "${origin_binary}:/recruiting-origin:ro" --entrypoint /recruiting-origin \
  -e RECRUITING_ORIGIN_PAYLOAD_BYTES="${payload_bytes}" \
  -e RECRUITING_ORIGIN_LATENCY_MS="${latency_ms}" mysql:8.4 >/dev/null
ready=false
for _ in $(seq 1 80); do
  if curl --noproxy '*' --fail --silent "${origin_url}/health" >/dev/null; then
    ready=true
    break
  fi
  sleep 0.25
done
if [[ "${ready}" != "true" ]]; then
  echo "recruiting HTTP capacity: controlled origin failed to start" >&2
  docker logs "${origin_container}" >&2 || true
  exit 1
fi

echo "recruiting HTTP capacity: level=${level} items=${items} payload_bytes=${payload_bytes} latency_ms=${latency_ms} executors=${executors} isolated_origin=${origin_url} revision=$(git -C "${repository_root}" rev-parse --short HEAD)"
set +e
ATOLL_RECRUITING_HTTP_CAPACITY=1 \
RECRUITING_HTTP_CAPACITY_ITEMS="${items}" \
RECRUITING_HTTP_CAPACITY_PAYLOAD_BYTES="${payload_bytes}" \
RECRUITING_HTTP_CAPACITY_EXECUTORS="${executors}" \
RECRUITING_HTTP_CAPACITY_TIMEOUT_SECONDS="${timeout_seconds}" \
RECRUITING_HTTP_CAPACITY_ORIGIN="${origin_url}" \
  go test ./e2e -run '^TestRecruitingHTTPResponseCapacityThroughRealDataPlanes$' -count=1 -v \
  -timeout "$((timeout_seconds+120))s"
test_status=$?
set -e
if [[ ${test_status} -ne 0 ]]; then
  docker logs "${origin_container}" >&2 || true
fi
exit ${test_status}
