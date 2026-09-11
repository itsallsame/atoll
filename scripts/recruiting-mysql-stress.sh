#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
total=${RECRUITING_MYSQL_TOTAL_ITERATIONS:-100}
shards=${RECRUITING_MYSQL_SHARDS:-4}
run_dir=${RECRUITING_MYSQL_STRESS_RUN_DIR:-"${repo_dir}/.cache/recruiting-mysql-stress-$(date -u +%Y%m%dT%H%M%SZ)"}

for value in "$total" "$shards"; do
  if [[ ! "$value" =~ ^[1-9][0-9]*$ ]]; then
    echo "recruiting mysql stress: totals and shards must be positive integers" >&2
    exit 1
  fi
done
if (( shards > total )); then
  shards=$total
fi
mkdir -p "$run_dir"

declare -a pids iterations logs
base=$((total / shards))
remainder=$((total % shards))
for ((shard=1; shard<=shards; shard++)); do
  count=$base
  if (( shard <= remainder )); then
    count=$((count + 1))
  fi
  log="$run_dir/shard-${shard}.log"
  iterations[$shard]=$count
  logs[$shard]=$log
  (
    cd "$repo_dir"
    # A fresh container/schema per iteration is part of the P2 contract and
    # prevents InnoDB system files from accumulating across repeated 20K
    # fixtures even after application tables are dropped.
    for iteration in $(seq 1 "$count"); do
      RECRUITING_MYSQL_ITERATIONS=1 ./scripts/recruiting-mysql-test.sh
    done
    echo "recruiting mysql shard: ok (iterations=$count)"
  ) >"$log" 2>&1 &
  pids[$shard]=$!
  echo "recruiting mysql stress: shard=$shard pid=${pids[$shard]} iterations=$count log=$log"
done

failed=0
completed=0
for ((shard=1; shard<=shards; shard++)); do
  if wait "${pids[$shard]}"; then
    marker="iterations=${iterations[$shard]})"
    if ! grep -Fq "$marker" "${logs[$shard]}"; then
      echo "recruiting mysql stress: shard=$shard missing success marker" >&2
      failed=1
    else
      completed=$((completed + iterations[$shard]))
      echo "recruiting mysql stress: shard=$shard passed iterations=${iterations[$shard]}"
    fi
  else
    echo "recruiting mysql stress: shard=$shard failed; tail follows" >&2
    tail -80 "${logs[$shard]}" >&2 || true
    failed=1
  fi
done

if (( failed != 0 || completed != total )); then
  echo "recruiting mysql stress: failed completed=$completed expected=$total logs=$run_dir" >&2
  exit 1
fi

commit=$(git -C "$repo_dir" rev-parse HEAD)
echo "recruiting mysql stress: ok (commit=$commit, iterations=$completed, shards=$shards, logs=$run_dir)"
