#!/usr/bin/env bash
set -euo pipefail

started_ms=$(date +%s%3N)
container_name="atoll-recruiting-backup-restore-$$"
source_database="atoll_recruiting_backup_source_$$_${RANDOM}"
restore_database="atoll_recruiting_backup_restore_$$_${RANDOM}"
migration_password="atoll_recruiting_migration_backup_$$_${RANDOM}"
runtime_password="atoll_recruiting_runtime_backup_$$_${RANDOM}"
root_password="atoll_recruiting_root_backup_$$_${RANDOM}"
temporary_directory=$(mktemp -d /tmp/atoll-recruiting-backup-restore.XXXXXX)
init_sql="${temporary_directory}/10-backup-restore.sql"
dump_file="${temporary_directory}/recruiting.sql"

cleanup() {
  docker rm -f "${container_name}" >/dev/null 2>&1 || true
  rm -f "${init_sql}" "${dump_file}"
  rmdir "${temporary_directory}" 2>/dev/null || true
}
trap cleanup EXIT
chmod 700 "${temporary_directory}"

cat >"${init_sql}" <<SQL
CREATE DATABASE ${restore_database};
CREATE USER 'staircase_runtime'@'%' IDENTIFIED BY '${runtime_password}';
GRANT SELECT, INSERT, UPDATE, DELETE ON ${source_database}.* TO 'staircase_runtime'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON ${restore_database}.* TO 'staircase_runtime'@'%';
GRANT ALL PRIVILEGES ON ${restore_database}.* TO 'staircase_migrator'@'%';
SQL
chmod 644 "${init_sql}"

docker run -d \
  --name "${container_name}" \
  -p 127.0.0.1::3306 \
  --tmpfs /var/lib/mysql:rw,nosuid,size=1g \
  -e MYSQL_ROOT_PASSWORD="${root_password}" \
  -e MYSQL_DATABASE="${source_database}" \
  -e MYSQL_USER=staircase_migrator \
  -e MYSQL_PASSWORD="${migration_password}" \
  -v "${init_sql}:/docker-entrypoint-initdb.d/10-backup-restore.sql:ro" \
  mysql:8.4 --default-time-zone=+00:00 >/dev/null

host_port=$(docker port "${container_name}" 3306/tcp | awk -F: 'NR == 1 {print $NF}')
if [[ ! "${host_port}" =~ ^[0-9]+$ ]]; then
  echo "recruiting backup restore: cannot resolve MySQL port" >&2
  exit 1
fi

ready=false
for _ in $(seq 1 45); do
  if MYSQL_PWD="${migration_password}" mysqladmin ping -h127.0.0.1 -P"${host_port}" \
    -ustaircase_migrator --silent >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "${ready}" != true ]]; then
  docker logs "${container_name}" >&2
  exit 1
fi

source_migration_dsn="staircase_migrator:${migration_password}@tcp(127.0.0.1:${host_port})/${source_database}"
source_runtime_dsn="staircase_runtime:${runtime_password}@tcp(127.0.0.1:${host_port})/${source_database}"
restore_runtime_dsn="staircase_runtime:${runtime_password}@tcp(127.0.0.1:${host_port})/${restore_database}"

RECRUITING_BACKUP_RESTORE_MODE=seed \
RECRUITING_MYSQL_MIGRATION_TEST_DSN="${source_migration_dsn}" \
RECRUITING_MYSQL_TEST_DSN="${source_runtime_dsn}" \
  go test ./drivers/tools/recruiting/store -run '^TestBackupRestoreContract$' -count=1
seed_completed_ms=$(date +%s%3N)

umask 077
MYSQL_PWD="${migration_password}" mysqldump \
  -h127.0.0.1 -P"${host_port}" -ustaircase_migrator \
  --single-transaction --quick --routines --triggers --set-gtid-purged=OFF --no-tablespaces \
  "${source_database}" >"${dump_file}"
if [[ ! -s "${dump_file}" ]]; then
  echo "recruiting backup restore: dump is empty" >&2
  exit 1
fi
if rg -F -e "${migration_password}" -e "${runtime_password}" "${dump_file}" >/dev/null; then
  echo "recruiting backup restore: database credentials leaked into dump" >&2
  exit 1
fi
dump_sha256=$(sha256sum "${dump_file}" | awk '{print $1}')
dump_bytes=$(stat -c '%s' "${dump_file}")
dump_completed_ms=$(date +%s%3N)

MYSQL_PWD="${migration_password}" mysql \
  -h127.0.0.1 -P"${host_port}" -ustaircase_migrator "${restore_database}" <"${dump_file}"
restore_completed_ms=$(date +%s%3N)

RECRUITING_BACKUP_RESTORE_MODE=verify \
RECRUITING_MYSQL_TEST_DSN="${restore_runtime_dsn}" \
  go test ./drivers/tools/recruiting/store -run '^TestBackupRestoreContract$' -count=1
verified_ms=$(date +%s%3N)

printf 'recruiting backup restore: {"schema":"recruiting.backup-restore-result.v1","dump_bytes":%s,"dump_sha256":"%s","seed_ms":%s,"dump_ms":%s,"restore_ms":%s,"verify_ms":%s,"total_ms":%s,"runtime_identity":"non-root","result":"pass"}\n' \
  "${dump_bytes}" "${dump_sha256}" "$((seed_completed_ms-started_ms))" "$((dump_completed_ms-seed_completed_ms))" \
  "$((restore_completed_ms-dump_completed_ms))" "$((verified_ms-restore_completed_ms))" "$((verified_ms-started_ms))"
