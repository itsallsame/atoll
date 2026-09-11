#!/usr/bin/env bash
set -euo pipefail

container_name="atoll-recruiting-mysql-test-$$"
database_name="atoll_recruiting_test_$$_${RANDOM}"
test_password="atoll_recruiting_test_only_$$"
runtime_password="atoll_recruiting_runtime_test_only_$$"
iterations="${RECRUITING_MYSQL_ITERATIONS:-1}"
test_run="${RECRUITING_MYSQL_TEST_RUN:-}"
actor_test_run="${RECRUITING_MYSQL_ACTOR_TEST_RUN:-^(TestRecipeRolloutReconcile|TestRepairRecoveryReconcile)}"
tmpfs_size="${RECRUITING_MYSQL_TMPFS_SIZE:-}"
test_args=()
storage_args=()
if [[ -n "$test_run" ]]; then
  test_args=(-run "$test_run")
fi
if [[ "${RECRUITING_MYSQL_TEST_VERBOSE:-0}" == "1" ]]; then
  test_args+=(-v)
fi
if [[ -n "${tmpfs_size}" ]]; then
  if [[ ! "${tmpfs_size}" =~ ^[1-9][0-9]*[kKmMgG]$ ]]; then
    echo "recruiting mysql test: RECRUITING_MYSQL_TMPFS_SIZE must look like 512m or 4g" >&2
    exit 1
  fi
  storage_args=(--tmpfs "/var/lib/mysql:rw,nosuid,size=${tmpfs_size}")
fi
init_directory=$(mktemp -d /tmp/atoll-recruiting-mysql-init.XXXXXX)
init_sql="${init_directory}/10-recruiting-runtime.sql"

if [[ ! "${iterations}" =~ ^[1-9][0-9]*$ ]]; then
  echo "recruiting mysql test: RECRUITING_MYSQL_ITERATIONS must be a positive integer" >&2
  exit 1
fi

cleanup() {
  # -v matters when this script is interrupted: mysql:8.4 declares an
  # anonymous /var/lib/mysql volume unless the caller supplies tmpfs. A plain
  # docker rm -f leaves that volume behind and repeated stress runs can fill
  # the host even though every container itself has gone away.
  docker rm -f -v "${container_name}" >/dev/null 2>&1 || true
  rm -f "${init_sql}"
  rmdir "${init_directory}" 2>/dev/null || true
}
trap cleanup EXIT

cat >"${init_sql}" <<SQL
CREATE USER 'staircase_runtime'@'%' IDENTIFIED BY '${runtime_password}';
GRANT SELECT, INSERT, UPDATE, DELETE ON \`${database_name}\`.* TO 'staircase_runtime'@'%';
SQL

docker run -d --rm \
  --name "${container_name}" \
  -p 127.0.0.1::3306 \
  -e MYSQL_RANDOM_ROOT_PASSWORD=yes \
  -e MYSQL_DATABASE="${database_name}" \
  -e MYSQL_USER=staircase_migrator \
  -e MYSQL_PASSWORD="${test_password}" \
  -v "${init_sql}:/docker-entrypoint-initdb.d/10-recruiting-runtime.sql:ro" \
  "${storage_args[@]}" \
  mysql:8.4 --default-time-zone=+00:00 >/dev/null

host_port=$(docker port "${container_name}" 3306/tcp | awk -F: 'NR == 1 {print $NF}')
if [[ ! "${host_port}" =~ ^[0-9]+$ ]]; then
  echo "recruiting mysql test: cannot resolve container port" >&2
  exit 1
fi

ready=false
for _ in $(seq 1 45); do
  if mysqladmin ping -h127.0.0.1 -P"${host_port}" -ustaircase_migrator -p"${test_password}" --silent >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "${ready}" != true ]]; then
  docker logs "${container_name}" >&2
  exit 1
fi

export RECRUITING_MYSQL_MIGRATION_TEST_DSN="staircase_migrator:${test_password}@tcp(127.0.0.1:${host_port})/${database_name}"
export RECRUITING_MYSQL_TEST_DSN="staircase_runtime:${runtime_password}@tcp(127.0.0.1:${host_port})/${database_name}"

for iteration in $(seq 1 "${iterations}"); do
  if (( iteration > 1 )); then
    MYSQL_PWD="${test_password}" mysql -h127.0.0.1 -P"${host_port}" -ustaircase_migrator "${database_name}" <<'SQL'
SET FOREIGN_KEY_CHECKS = 0;
DROP TABLE IF EXISTS
  recruiting_execution_dispatch_outbox,
  recruiting_scope_catchup_occurrences,
  recruiting_scope_control_roots,
  recruiting_scope_control_operations,
  recruiting_validation_artifact_pages,
  recruiting_backfill_outputs,
  recruiting_backfill_items,
  recruiting_backfills,
  recruiting_recipe_rollout_items,
  recruiting_recipe_rollout_batches,
  recruiting_profile_repair_sessions,
  recruiting_source_profile_binding_history,
  recruiting_source_profile_bindings,
  recruiting_repair_affected_works,
  recruiting_event_outbox,
  recruiting_command_receipts,
  recruiting_repair_incidents,
  recruiting_source_discovery_candidates,
  recruiting_source_discoveries,
  recruiting_company_import_items,
  recruiting_company_imports,
  recruiting_company_merge_previews,
  recruiting_company_aliases,
  recruiting_budget_permits,
  recruiting_budget_usage,
  recruiting_profiles,
  recruiting_override_heads,
  recruiting_override_versions,
  recruiting_job_detail_versions,
  recruiting_listing_observations,
  recruiting_artifacts,
  recruiting_listing_page_progress,
  recruiting_listing_runs,
  recruiting_recipe_validation_runs,
  recruiting_attempts,
  recruiting_works,
  recruiting_source_occurrences,
  recruiting_daily_runs,
  recruiting_source_jobs,
  recruiting_baseline_detail_items,
  recruiting_baseline_staging,
  recruiting_baseline_generations,
  recruiting_checkpoints,
  recruiting_recipe_proposals,
  recruiting_source_assignment_versions,
  recruiting_source_assignments,
  recruiting_recipes,
  recruiting_sources,
  recruiting_companies,
  recruiting_schema_migrations;
SET FOREIGN_KEY_CHECKS = 1;
SQL
  fi
  go test -race ./drivers/tools/recruiting/store -count=1 "${test_args[@]}"
  env RECRUITING_ACTOR_MYSQL_TEST_DSN="${RECRUITING_MYSQL_TEST_DSN}" \
    go test -race ./drivers/tools/recruiting -run "${actor_test_run}" -count=1
done

echo "recruiting mysql: ok (ephemeral MySQL 8.4, migration/runtime non-root accounts, schema=${database_name}, iterations=${iterations})"
