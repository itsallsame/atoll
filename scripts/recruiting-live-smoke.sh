#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
run_dir=${RECRUITING_LIVE_RUN_DIR:-}
if [[ -z "$run_dir" ]]; then
  run_dir=$(mktemp -d /tmp/atoll-recruiting-live-smoke.XXXXXX)
fi
artifact_dir="$run_dir/artifacts"
report_path="$run_dir/report.json"
mkdir -p "$artifact_dir"

cd "$repo_dir"
go run ./cmd/recruiting-live-smoke -artifact-dir "$artifact_dir" -report "$report_path"

echo "live smoke report: $report_path"
echo "live smoke artifacts: $artifact_dir"
