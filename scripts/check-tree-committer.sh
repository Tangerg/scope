#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
container=
cleanup() {
  status=$?
  if [[ -n "$container" ]]; then
    if ! docker rm -fv "$container" >/dev/null; then
      status=1
    fi
  fi
  exit "$status"
}
trap cleanup EXIT

# The named database has no host mount or shared service. Its only published
# port is an ephemeral loopback port; teardown removes this container's volume.
container=$(docker run -d --pull=missing --label scope.tree-committer-test=true \
  -e POSTGRES_USER=scope_test -e POSTGRES_PASSWORD=scope_test -e POSTGRES_DB=scope_treecommit_test \
  -p 127.0.0.1::5432 \
  --health-cmd='pg_isready -U scope_test -d scope_treecommit_test' \
  --health-interval=1s --health-timeout=3s --health-retries=30 \
  postgres:15-alpine@sha256:2e7b888f221193cddee9ce554d3311dc8be197a5d0c904fe30f5f568e0f99851 \
  -c fsync=on -c synchronous_commit=on -c full_page_writes=on)

ready=0
for _ in {1..40}; do
  health=$(docker inspect --format '{{.State.Health.Status}}' "$container")
  if [[ "$health" == "healthy" ]]; then
    ready=1
    break
  fi
  if [[ "$health" == "unhealthy" ]]; then
    break
  fi
  sleep 1
done
if [[ "$ready" != 1 ]]; then
  docker logs "$container" >&2
  echo "isolated PostgreSQL did not become healthy" >&2
  exit 1
fi

address=$(docker port "$container" 5432/tcp)
if [[ ! "$address" =~ ^127\.0\.0\.1:[0-9]+$ ]]; then
  echo "unexpected PostgreSQL test port binding" >&2
  exit 1
fi
export SCOPE_TREECOMMITTER_POSTGRES_DSN="postgres://scope_test:scope_test@$address/scope_treecommit_test?sslmode=disable"
docker exec "$container" postgres --version
cd "$root/dev/treecommitterconformance"
go vet -tags=integration ./...
golangci-lint run --build-tags=integration --allow-parallel-runners --config="$root/.golangci.yml" ./...
govulncheck -tags=integration -test ./...
go test -tags=integration -race -count=1 -timeout=3m -v ./...
GOWORK=off go test -tags=integration -race -count=1 -timeout=3m -v ./...
