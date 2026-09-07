#!/usr/bin/env bash
set -euo pipefail

coverage_file=$(mktemp /tmp/atoll-recruiting-model-cover.XXXXXX)
trap 'rm -f "${coverage_file}"' EXIT

go test -race ./drivers/tools/recruiting/...
go test ./drivers/tools/recruiting/model -run '^$' -fuzz '^FuzzCanonicalHTTPURLIdempotent$' -fuzztime=2s
go test ./drivers/tools/recruiting/model -run '^$' -fuzz '^FuzzCommandReceiptReplay$' -fuzztime=2s
go test ./drivers/tools/recruiting/model -run '^$' -fuzz '^FuzzWorkTerminalMonotonic$' -fuzztime=2s
go test ./drivers/tools/recruiting/model -run '^$' -fuzz '^FuzzSourceNeverBecomesEligibleWithoutPublishedProductionFacts$' -fuzztime=2s
go test -coverprofile="${coverage_file}" ./drivers/tools/recruiting/model

coverage=$(go tool cover -func="${coverage_file}" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')
awk -v coverage="${coverage}" 'BEGIN { if (coverage < 75) exit 1 }'
echo "recruiting model: ok (statement coverage=${coverage}%, minimum=75%)"
