#!/usr/bin/env bash
set -euo pipefail

container_name="atoll-recruiting-mysql-test-$$"
database_name="atoll_recruiting_test_$$"
test_password="atoll_recruiting_test_only_$$"

cleanup() {
  docker rm -f "${container_name}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --rm \
  --name "${container_name}" \
  -p 127.0.0.1::3306 \
  -e MYSQL_RANDOM_ROOT_PASSWORD=yes \
  -e MYSQL_DATABASE="${database_name}" \
  -e MYSQL_USER=staircase \
  -e MYSQL_PASSWORD="${test_password}" \
  mysql:8.4 --default-time-zone=+00:00 >/dev/null

host_port=$(docker port "${container_name}" 3306/tcp | awk -F: 'NR == 1 {print $NF}')
if [[ ! "${host_port}" =~ ^[0-9]+$ ]]; then
  echo "recruiting mysql test: cannot resolve container port" >&2
  exit 1
fi

ready=false
for _ in $(seq 1 45); do
  if mysqladmin ping -h127.0.0.1 -P"${host_port}" -ustaircase -p"${test_password}" --silent >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "${ready}" != true ]]; then
  docker logs "${container_name}" >&2
  exit 1
fi

export RECRUITING_MYSQL_TEST_DSN="staircase:${test_password}@tcp(127.0.0.1:${host_port})/${database_name}"
go test -race ./drivers/tools/recruiting/store -count=1

echo "recruiting mysql: ok (ephemeral MySQL 8.4, non-root account, schema=${database_name})"
