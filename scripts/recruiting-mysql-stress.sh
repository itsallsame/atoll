#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [[ "${1:-}" == "--run-shard" ]]; then
  count="${2:-}"
  if [[ ! "$count" =~ ^[1-9][0-9]*$ ]]; then
    echo "recruiting mysql shard: count must be a positive integer" >&2
    exit 1
  fi
  cd "$repo_dir"
  # A fresh container/schema per iteration is part of the P2 contract and
  # prevents InnoDB system files from accumulating across repeated 20K
  # fixtures even after application tables are dropped. Keep MySQL storage in
  # memory by default for the stress harness; callers can override its bound.
  for iteration in $(seq 1 "$count"); do
    RECRUITING_MYSQL_ITERATIONS=1 \
      RECRUITING_MYSQL_TMPFS_SIZE="${RECRUITING_MYSQL_TMPFS_SIZE:-2g}" \
      ./scripts/recruiting-mysql-test.sh
  done
  echo "recruiting mysql shard: ok (iterations=$count)"
  exit 0
fi

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

start_commit=$(git -C "$repo_dir" rev-parse HEAD)
if [[ -n "$(git -C "$repo_dir" status --porcelain --untracked-files=all)" ]]; then
  echo "recruiting mysql stress: worktree must be clean so evidence is attributable to one revision" >&2
  exit 1
fi
mkdir -p "$run_dir"

declare -a pids iterations logs
declare -A active_shards

cancel_active_shards() {
  local pid pgid attempt
  for pid in "${!active_shards[@]}"; do
    if [[ "$pid" =~ ^[1-9][0-9]*$ ]]; then
      pgid=$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d ' ' || true)
      if [[ "$pgid" == "$pid" ]]; then
        kill -TERM -- "-$pid" 2>/dev/null || true
      else
        kill -TERM "$pid" 2>/dev/null || true
      fi
    fi
  done
  # The setsid wrapper can reap its direct shell before a signal-interrupted
  # inner test shell has finished the docker cleanup trap. Wait for every
  # member of the dedicated process group, not only the wrapper PID, so a
  # failed stress command never returns while its MySQL containers linger.
  for attempt in $(seq 1 100); do
    remaining=false
    for pid in "${!active_shards[@]}"; do
      if pgrep -g "$pid" >/dev/null 2>&1; then
        remaining=true
        break
      fi
    done
    if [[ "$remaining" == false ]]; then
      break
    fi
    sleep 0.1
  done
  for pid in "${!active_shards[@]}"; do
    if pgrep -g "$pid" >/dev/null 2>&1; then
      kill -KILL -- "-$pid" 2>/dev/null || true
    fi
  done
  for pid in "${!active_shards[@]}"; do
    wait "$pid" 2>/dev/null || true
  done
  active_shards=()
}

trap 'cancel_active_shards' EXIT
trap 'cancel_active_shards; exit 130' INT TERM
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
  setsid --wait "${BASH_SOURCE[0]}" --run-shard "$count" >"$log" 2>&1 &
  pids[$shard]=$!
  active_shards[${pids[$shard]}]=$shard
  echo "recruiting mysql stress: shard=$shard pid=${pids[$shard]} iterations=$count log=$log"
done

failed=0
completed=0
while (( ${#active_shards[@]} > 0 )); do
  finished_pid=""
  if wait -n -p finished_pid "${!active_shards[@]}"; then
    shard=${active_shards[$finished_pid]}
    unset 'active_shards[$finished_pid]'
    marker="iterations=${iterations[$shard]})"
    if ! grep -Fq "$marker" "${logs[$shard]}"; then
      echo "recruiting mysql stress: shard=$shard missing success marker" >&2
      failed=1
      cancel_active_shards
      break
    else
      completed=$((completed + iterations[$shard]))
      echo "recruiting mysql stress: shard=$shard passed iterations=${iterations[$shard]}"
    fi
  else
    shard=${active_shards[$finished_pid]}
    unset 'active_shards[$finished_pid]'
    echo "recruiting mysql stress: shard=$shard failed; tail follows" >&2
    tail -80 "${logs[$shard]}" >&2 || true
    failed=1
    cancel_active_shards
    break
  fi
done

trap - EXIT INT TERM

if (( failed != 0 || completed != total )); then
  echo "recruiting mysql stress: failed completed=$completed expected=$total logs=$run_dir" >&2
  exit 1
fi

end_commit=$(git -C "$repo_dir" rev-parse HEAD)
if [[ "$end_commit" != "$start_commit" || -n "$(git -C "$repo_dir" status --porcelain --untracked-files=all)" ]]; then
  echo "recruiting mysql stress: revision or worktree changed during run; result is not valid acceptance evidence (start=$start_commit end=$end_commit)" >&2
  exit 1
fi
echo "recruiting mysql stress: ok (commit=$start_commit, iterations=$completed, shards=$shards, logs=$run_dir)"
