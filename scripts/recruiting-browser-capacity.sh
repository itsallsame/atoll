#!/usr/bin/env bash
set -euo pipefail

level="${RECRUITING_BROWSER_CAPACITY_LEVEL:-B0}"
artifact_recovery="${RECRUITING_BROWSER_ARTIFACT_RECOVERY:-0}"
joint_process_recovery="${RECRUITING_BROWSER_JOINT_PROCESS_RECOVERY:-0}"
server_recovery="${RECRUITING_BROWSER_SERVER_RECOVERY:-0}"
case "${level}" in
  B0|B1|B2|BF0|BF1|BF2) ;;
  *)
    echo "recruiting Browser capacity: level must be one of B0, B1, B2, BF0, BF1, BF2" >&2
    exit 2
    ;;
esac
if [[ "${artifact_recovery}" != "0" && "${artifact_recovery}" != "1" ]]; then
  echo "recruiting Browser capacity: RECRUITING_BROWSER_ARTIFACT_RECOVERY must be 0 or 1" >&2
  exit 2
fi
if [[ "${joint_process_recovery}" != "0" && "${joint_process_recovery}" != "1" ]]; then
  echo "recruiting Browser capacity: RECRUITING_BROWSER_JOINT_PROCESS_RECOVERY must be 0 or 1" >&2
  exit 2
fi
if [[ "${server_recovery}" != "0" && "${server_recovery}" != "1" ]]; then
  echo "recruiting Browser capacity: RECRUITING_BROWSER_SERVER_RECOVERY must be 0 or 1" >&2
  exit 2
fi
if [[ "${artifact_recovery}" == "0" && "${joint_process_recovery}" == "1" ]]; then
  echo "recruiting Browser capacity: joint process recovery also requires Artifact recovery" >&2
  exit 2
fi
if [[ "${level}" == "BF0" && ( "${artifact_recovery}" != "1" || "${joint_process_recovery}" != "0" || "${server_recovery}" != "0" ) ]] ||
   [[ "${level}" == "BF1" && ( "${artifact_recovery}" != "1" || "${joint_process_recovery}" != "1" || "${server_recovery}" != "0" ) ]] ||
   [[ "${level}" == "BF2" && ( "${artifact_recovery}" != "0" || "${joint_process_recovery}" != "0" || "${server_recovery}" != "1" ) ]] ||
   [[ "${level}" != "BF0" && "${level}" != "BF1" && "${level}" != "BF2" && ( "${artifact_recovery}" != "0" || "${joint_process_recovery}" != "0" || "${server_recovery}" != "0" ) ]]; then
  echo "recruiting Browser capacity: B0-B2 are normal, BF0 is provider-only, BF1 is joint-process, and BF2 is Server recovery" >&2
  exit 2
fi
if [[ "${level}" == "B2" && "${RECRUITING_BROWSER_CAPACITY_LARGE_ACK:-}" != "isolated-browser-large-load" ]]; then
  echo "recruiting Browser capacity: B2 launches 100 Chrome sessions; set RECRUITING_BROWSER_CAPACITY_LARGE_ACK=isolated-browser-large-load" >&2
  exit 2
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${script_dir}/.." && pwd)
manifest="${repository_root}/docs/experiments/workloads/recruiting-browser-capacity-v1.json"
for command in docker jq go curl; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "recruiting Browser capacity: required command is missing: ${command}" >&2
    exit 1
  fi
done
chrome_bin="${RECRUITING_CHROME_BIN:-}"
if [[ -z "${chrome_bin}" ]]; then
  chrome_bin=$(command -v google-chrome || command -v google-chrome-stable || command -v chromium || command -v chromium-browser || true)
fi
if [[ -z "${chrome_bin}" || ! -x "${chrome_bin}" ]]; then
  echo "recruiting Browser capacity: set RECRUITING_CHROME_BIN to an executable Chrome or Chromium" >&2
  exit 1
fi
if ! jq -e --arg level "${level}" '.version == "recruiting.browser-capacity-workload.v1" and (.levels[$level] != null)' \
  "${manifest}" >/dev/null; then
  echo "recruiting Browser capacity: invalid workload manifest" >&2
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
origin_network="atoll-recruiting-browser-origin-${run_id}"
origin_container="atoll-recruiting-browser-origin-${run_id}"
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
  echo "recruiting Browser capacity: controlled origin network is not internal" >&2
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
  echo "recruiting Browser capacity: controlled origin failed to start" >&2
  docker logs "${origin_container}" >&2 || true
  exit 1
fi

test_name='TestRecruitingBrowserResponseCapacityThroughRealDataPlanes'
if [[ "${artifact_recovery}" == "1" ]]; then
	test_name='TestRecruitingBrowserArtifactProviderRecoveryThroughRealDataPlanes'
fi
if [[ "${joint_process_recovery}" == "1" ]]; then
  test_name='TestRecruitingBrowserJointProcessRecoveryThroughRealDataPlanes'
fi
if [[ "${server_recovery}" == "1" ]]; then
  test_name='TestRecruitingBrowserServerRecoveryThroughRealDataPlanes'
fi
echo "recruiting Browser capacity: level=${level} artifact_recovery=${artifact_recovery} joint_process_recovery=${joint_process_recovery} server_recovery=${server_recovery} items=${items} payload_bytes=${payload_bytes} latency_ms=${latency_ms} executors=${executors} chrome=${chrome_bin} isolated_origin=${origin_url} revision=$(git -C "${repository_root}" rev-parse --short HEAD)"
set +e
ATOLL_RECRUITING_HTTP_CAPACITY=1 \
ATOLL_RECRUITING_BROWSER_CAPACITY=1 \
ATOLL_RECRUITING_BROWSER_ARTIFACT_RECOVERY="${artifact_recovery}" \
ATOLL_RECRUITING_BROWSER_JOINT_PROCESS_RECOVERY="${joint_process_recovery}" \
ATOLL_RECRUITING_BROWSER_SERVER_RECOVERY="${server_recovery}" \
RECRUITING_CHROME_BIN="${chrome_bin}" \
RECRUITING_HTTP_CAPACITY_ITEMS="${items}" \
RECRUITING_HTTP_CAPACITY_PAYLOAD_BYTES="${payload_bytes}" \
RECRUITING_HTTP_CAPACITY_EXECUTORS="${executors}" \
RECRUITING_HTTP_CAPACITY_TIMEOUT_SECONDS="${timeout_seconds}" \
RECRUITING_HTTP_CAPACITY_ORIGIN="${origin_url}" \
  go test ./e2e -run "^${test_name}$" -count=1 -v \
  -timeout "$((timeout_seconds+120))s"
test_status=$?
set -e
if [[ ${test_status} -ne 0 ]]; then
  docker logs "${origin_container}" >&2 || true
fi
exit ${test_status}
