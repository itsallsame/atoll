#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repository_root=$(cd "${script_dir}/.." && pwd)
matrix_case="${RECRUITING_DAILY_PROCESS_CASE:-all}"
case "${matrix_case}" in
  all|executor|actor) ;;
  *)
    echo "recruiting daily process matrix: RECRUITING_DAILY_PROCESS_CASE must be all, executor, or actor" >&2
    exit 2
    ;;
esac
for command in docker go curl sha256sum date; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "recruiting daily process matrix: required command is missing: ${command}" >&2
    exit 1
  fi
done

run_id="$$-$(date +%s)"
journey_token=$(printf '%s' "${run_id}-${RANDOM}-${RANDOM}" | sha256sum | awk '{print $1}')
origin_build_dir=$(mktemp -d)
origin_binary="${origin_build_dir}/recruiting-origin"
origin_network="atoll-recruiting-daily-process-${run_id}"
origin_container="atoll-recruiting-daily-process-${run_id}"
subnet_octet=$(( ($$ % 180) + 20 ))
origin_subnet="11.251.${subnet_octet}.0/24"
origin_ip="11.251.${subnet_octet}.10"
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
  echo "recruiting daily process matrix: controlled origin network is not internal" >&2
  exit 1
fi
docker run -d --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  --name "${origin_container}" --network "${origin_network}" --ip "${origin_ip}" \
  -v "${origin_binary}:/recruiting-origin:ro" --entrypoint /recruiting-origin \
  -e RECRUITING_ORIGIN_PAYLOAD_BYTES=1024 \
  -e RECRUITING_ORIGIN_LATENCY_MS=5 \
  -e RECRUITING_ORIGIN_LISTING_LATENCY_MS=100 \
  -e RECRUITING_ORIGIN_ACTIVITY_UNIX=2051222400 \
  -e RECRUITING_ORIGIN_DAILY_JOURNEY_TOKEN="${journey_token}" mysql:8.4 >/dev/null

ready=false
for _ in $(seq 1 80); do
  if curl --noproxy '*' --fail --silent "${origin_url}/health" >/dev/null; then
    ready=true
    break
  fi
  sleep 0.25
done
if [[ "${ready}" != "true" ]]; then
  echo "recruiting daily process matrix: controlled origin failed to start" >&2
  docker logs "${origin_container}" >&2 || true
  exit 1
fi

echo "recruiting daily process matrix: isolated_origin=${origin_url} revision=$(git rev-parse --short HEAD)"
set +e
ATOLL_RECRUITING_DAILY_PROCESS_FAILURE_MATRIX=1 \
RECRUITING_DAILY_PROCESS_ORIGIN="${origin_url}" \
RECRUITING_DAILY_PROCESS_TOKEN="${journey_token}" \
RECRUITING_DAILY_PROCESS_CASE="${matrix_case}" \
  go test ./e2e -run '^TestRecruitingDailyIncrementalProcessFailureMatrix$' -count=1 -v -timeout 20m
test_status=$?
set -e
if [[ ${test_status} -ne 0 ]]; then
  docker logs "${origin_container}" >&2 || true
fi
exit ${test_status}
