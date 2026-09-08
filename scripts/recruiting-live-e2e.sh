#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

ATOLL_RECRUITING_LIVE_E2E=1 go test -count=1 ./e2e -run '^TestRecruitingLiveExecutionThroughAtoll$' -v -timeout 180s
