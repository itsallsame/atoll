#!/usr/bin/env bash
set -euo pipefail

tier="${1:-}"
case "${tier}" in
  nightly|weekly) ;;
  *)
    echo "recruiting live tier: expected nightly or weekly" >&2
    exit 2
    ;;
esac

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
manifest="${RECRUITING_LIVE_SITE_MANIFEST:-${repo_dir}/docs/experiments/workloads/recruiting-live-sites-v1.json}"
run_dir="${RECRUITING_LIVE_RUN_DIR:-}"
if [[ -z "${run_dir}" ]]; then
  run_dir=$(mktemp -d "/tmp/atoll-recruiting-live-${tier}.XXXXXX")
fi
artifact_dir="${run_dir}/artifacts"
report_dir="${run_dir}/sites"
summary_path="${run_dir}/report.json"
build_dir=$(mktemp -d /tmp/atoll-recruiting-live-build.XXXXXX)

cleanup() {
  rm -f "${build_dir}/recruiting-live-smoke"
  rmdir "${build_dir}" 2>/dev/null || true
}
trap cleanup EXIT
umask 077
mkdir -p "${artifact_dir}" "${report_dir}"
cd "${repo_dir}"

if ! jq -e --arg tier "${tier}" '
  .schema == "recruiting.live-sites.v1" and
  (.policy.read_only == true) and (.policy.max_listing_pages == 1) and
  (.policy.max_response_bytes <= 2097152) and
  (.tiers[$tier] | type == "array") and
  (if $tier == "nightly" then (.tiers[$tier] | length >= 5 and length <= 10)
   else (.tiers[$tier] | length >= 20 and length <= 50) end) and
  ([.tiers[$tier][].id] | length == (unique | length)) and
  all(.tiers[$tier][];
    (.id | test("^[a-z0-9][a-z0-9-]{0,190}$")) and
    (.provider == "greenhouse" or .provider == "lever") and
    (.source_url | startswith("https://")) and
    (.documentation_url | startswith("https://")) and
    (.terms_reviewed_at | fromdateiso8601 | type == "number"))
' "${manifest}" >/dev/null; then
  echo "recruiting live tier: invalid ${tier} manifest ${manifest}" >&2
  exit 1
fi

git -C "${repo_dir}" diff --quiet -- cmd/recruiting-live-smoke drivers/tools/recruitingexecutor/recipeabi drivers/tools/recruitingexecutor/httpdriver || {
  echo "recruiting live tier: executable inputs have uncommitted changes" >&2
  exit 1
}
go build -o "${build_dir}/recruiting-live-smoke" ./cmd/recruiting-live-smoke

started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
manifest_sha256=$(sha256sum "${manifest}" | awk '{print $1}')
failures=0
index=0
while IFS=$'\t' read -r target_id provider source_url documentation_url terms_reviewed_at; do
  index=$((index+1))
  target_artifacts="${artifact_dir}/${target_id}"
  target_report="${report_dir}/${target_id}.json"
  mkdir -p "${target_artifacts}"
  echo "recruiting live ${tier}: [${index}] ${target_id}"
  if ! "${build_dir}/recruiting-live-smoke" \
    -target-id "${target_id}" -provider "${provider}" -source-url "${source_url}" \
    -documentation-url "${documentation_url}" -terms-reviewed-at "${terms_reviewed_at}" \
    -artifact-dir "${target_artifacts}" -report "${target_report}" >/dev/null; then
    failures=$((failures+1))
    if [[ ! -s "${target_report}" ]]; then
      jq -n --arg id "${target_id}" --arg provider "${provider}" --arg source "${source_url}" \
        '{schema_version:"recruiting.live-smoke.v1",status:"diagnostic_failed",target_id:$id,
          provider:$provider,source_url:$source,eligibility_reasons:["runner exited before producing a report"],artifacts:[]}' \
        >"${target_report}"
    fi
  fi
  sleep 2
done < <(jq -r --arg tier "${tier}" '.tiers[$tier][] |
  [.id, .provider, .source_url, .documentation_url, .terms_reviewed_at] | @tsv' "${manifest}")

finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
jq -s --arg tier "${tier}" --arg started "${started_at}" --arg finished "${finished_at}" \
  --arg manifest_sha256 "${manifest_sha256}" '
  {schema:"recruiting.live-tier-report.v1", tier:$tier, started_at:$started, finished_at:$finished,
   manifest_sha256:$manifest_sha256, target_count:length,
   passed_count:([.[] | select(.status == "diagnostic_pass")] | length),
   failed_count:([.[] | select(.status != "diagnostic_pass")] | length), reports:.}
' "${report_dir}"/*.json >"${summary_path}"
chmod 600 "${summary_path}"

echo "recruiting live ${tier} report: ${summary_path}"
if (( failures > 0 )); then
  echo "recruiting live ${tier}: ${failures} target(s) failed; inspect the aggregate report" >&2
  exit 1
fi
