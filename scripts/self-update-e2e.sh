#!/usr/bin/env bash
# End-to-end check that a running Watchlet container replaces itself with a
# newly published image and comes back up. Needs a Docker daemon with Compose.
# Builds two images from the working tree, serves them from a throwaway local
# registry, and removes everything it created on exit.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
run_id="watchlet-e2e-$$-$RANDOM"
work_dir="$(cd "$(mktemp -d "/tmp/${run_id}.XXXXXX")" && pwd -P)"
compose_file="$work_dir/docker-compose.yml"
registry="${run_id}-registry"
container="${run_id}-watchlet"
timeout_seconds="${WATCHLET_E2E_TIMEOUT:-120}"
v1_id=""
v2_id=""

log() { printf 'self-update-e2e: %s\n' "$*"; }

cleanup() {
	set +e
	if [ -f "$compose_file" ]; then
		docker compose -f "$compose_file" down --remove-orphans >/dev/null 2>&1
	fi
	for leftover in $(docker ps -aq --filter "name=${container}"); do
		docker rm -f -v "$leftover" >/dev/null 2>&1
	done
	docker rm -f -v "$registry" >/dev/null 2>&1
	for image in "${local_ref:-}" "${publish_ref:-}" "$v1_id" "$v2_id"; do
		[ -n "$image" ] && docker image rm -f "$image" >/dev/null 2>&1
	done
	rm -rf "$work_dir"
}
trap cleanup EXIT

fail() {
	set +e
	log "FAIL: $*"
	log "containers:"
	docker ps -a --filter "name=${container}" --format '  {{.Names}} {{.Status}} {{.Image}}'
	log "watchlet self-update, cleanup and failure logs:"
	docker logs "$container" 2>&1 | grep -E 'operation=self-update|action=cleanup|status=failed|level=(WARN|ERROR)' | sed 's/^/  /'
	log "watchlet latest logs:"
	docker logs "$container" 2>&1 | tail -10 | sed 's/^/  /'
	if docker container inspect "${container}-self-update" >/dev/null 2>&1; then
		log "helper logs:"
		docker logs "${container}-self-update" 2>&1 | sed 's/^/  /'
	fi
	exit 1
}

wait_for() {
	local description="$1"
	shift
	local deadline=$((SECONDS + timeout_seconds))
	until "$@"; do
		if [ "$SECONDS" -ge "$deadline" ]; then
			fail "timed out after ${timeout_seconds}s waiting for $description"
		fi
		sleep 2
	done
}

docker run -d -q --name "$registry" -p 127.0.0.1::5000 registry:2 >/dev/null
port="$(docker port "$registry" 5000/tcp | head -n1 | sed 's/.*://')"
# Compose and the running service use the localhost name. The second image is
# pushed through 127.0.0.1, the same registry under another local name, so the
# local localhost tag keeps pointing at the first image until Watchlet pulls.
local_ref="localhost:${port}/watchlet-e2e:latest"
publish_ref="127.0.0.1:${port}/watchlet-e2e:latest"
wait_for "registry" curl -fsS "http://127.0.0.1:${port}/v2/" -o /dev/null

log "building first image"
docker build -q --label "watchlet.e2e=${run_id}-v1" -t "$local_ref" "$repo_root" >/dev/null
v1_id="$(docker image inspect -f '{{.Id}}' "$local_ref")"
docker push -q "$local_ref" >/dev/null

cat >"$compose_file" <<EOF
name: ${run_id}
services:
  watchlet:
    image: ${local_ref}
    container_name: ${container}
    restart: unless-stopped
    environment:
      WATCHLET_COMPOSE: /stack/docker-compose.yml
      WATCHLET_INTERVAL: 5s
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ${work_dir}:/stack:ro
    labels:
      - watchlet.enable=true
EOF

docker compose -f "$compose_file" up -d >/dev/null 2>&1
first_pass_done() { docker logs "$container" 2>&1 | grep 'action=pass-end status=ok' >/dev/null; }
wait_for "first pass" first_pass_done

log "publishing second image"
docker build -q --label "watchlet.e2e=${run_id}-v2" -t "$publish_ref" "$repo_root" >/dev/null
v2_id="$(docker image inspect -f '{{.Id}}' "$publish_ref")"
docker push -q "$publish_ref" >/dev/null

running_image() { docker inspect -f '{{if .State.Running}}{{.Image}}{{end}}' "$container" 2>/dev/null; }
replaced() { [ "$(running_image)" = "$v2_id" ]; }
wait_for "the Watchlet container to run the second image" replaced

successor_reported() { docker logs "$container" 2>&1 | grep 'operation=self-update .*status=ok' >/dev/null; }
wait_for "the new Watchlet to report the self-update result" successor_reported

leftovers="$(docker ps -a --filter "name=${container}" --format '{{.Names}}' | grep -vx "$container" || true)"
[ -z "$leftovers" ] || fail "leftover containers: $leftovers"

old_image_removed() { ! docker image inspect "$v1_id" >/dev/null 2>&1; }
wait_for "the previous Watchlet image to be removed" old_image_removed

log "new Watchlet logs:"
docker logs "$container" 2>&1 | grep -E 'operation=self-update|action=cleanup' | sed 's/^/  /'
log "PASS: Watchlet replaced itself and cleaned up"
