# Architecture

## Purpose

Watchlet is a small Go program that updates selected Docker Compose services while keeping the update path inspectable and dependency-light.

It preserves the useful core of Watchtower-style image updates without becoming a general Docker automation platform.

## Product constraints

Watchlet must preserve these constraints:

- Docker Compose files are the only supported input.
- Compose file paths are explicit; Watchlet does no project discovery.
- Selected services are explicit and label-driven with `watchlet.enable=true`.
- Registry-backed `image:` services are updated automatically.
- Any selected service with `build:` is skipped and reported, even if it also has `image:`.
- Docker socket access is treated as privileged host control.
- Small dependencies and readable behavior matter more than feature breadth.

## Core engineering principles

- Keep the update path boring and inspectable.
- Prefer one clear implementation path over compatibility layers.
- Use Docker Compose as the action boundary instead of reconstructing containers manually. The one container Watchlet starts itself is the short-lived self-update helper, which only runs the same Compose recreate command.
- Make every skipped service and update action visible in logs.
- Keep scheduling as a wrapper around a single update pass.
- Add features only when they directly protect or improve the core image-update workflow.

## Runtime shape

Watchlet is implemented in Go and distributed as a small container image published to `ghcr.io/mmarfil/watchlet`.

The runtime image includes the Watchlet binary, Docker CLI, and Docker Compose plugin because Watchlet intentionally uses Docker Compose as its action boundary.

The current implementation is organized around these packages:

- `internal/config` — CLI/env parsing, defaults, and Compose path normalization.
- `internal/composefile` — narrow Compose YAML parsing for service names, labels, `image`, and `build`.
- `internal/dockercompose` — the command boundary for Docker and Docker Compose CLI calls.
- `internal/updatepass` — one update pass across configured Compose files.
- `internal/interval` — repeated execution of the same update pass.
- `internal/passresult` — atomic writing of the optional latest-pass result.
- `internal/watchlog` — plain key-value logging helpers.

Dependencies should remain minimal and justified. The Compose parser currently uses `gopkg.in/yaml.v3` directly.

Container publishing is handled by `.github/workflows/container.yml`, which builds the repository Dockerfile and pushes `latest`, commit SHA, and version-tag images to GitHub Container Registry. Publishing waits for `scripts/self-update-e2e.sh`, which runs Watchlet in a throwaway Compose stack against a throwaway local registry, publishes a second image, and checks that Watchlet replaced itself, reported the result, left no stray containers, and removed its previous image.

## Configuration contract

Watchlet supports both CLI flags and environment variables. CLI flags override environment variables.

- `WATCHLET_COMPOSE` — comma-separated Compose file paths.
- `WATCHLET_INTERVAL` — Go duration string such as `24h`, `1h`, or `30m`.
- `WATCHLET_RESULT_FILE` — optional path atomically replaced with the latest completed pass result.
- `--compose PATH` — may be repeated for multiple Compose files.
- `--interval DURATION` — overrides `WATCHLET_INTERVAL`.
- `--result-file PATH` — overrides `WATCHLET_RESULT_FILE`.
- `--once` — runs one update pass and exits.
- `--force` — valid with `--once`; recreates selected image services after pulling even when image identity is unchanged, except Watchlet's own service, which is recreated only when its image changed.

The default interval is `24h`. Force mode intentionally has no environment variable initially.

Multiple Compose paths are processed as independent Compose projects. A failure in one Compose file is logged and reflected in the final status, but does not prevent remaining Compose files from being processed.

## Docker Compose command boundary

Watchlet uses Docker CLI and Docker Compose CLI commands rather than a Docker API client.

For a configured Compose file, Docker Compose commands must run with both:

- `--project-directory <directory-of-compose-file>`
- `-f <compose-file>`

This keeps explicit absolute paths aligned with normal stack-local `.env` and interpolation behavior when Watchlet runs from its own container working directory.

Compose runs inside Watchlet, so it resolves relative bind sources and the default project name from the path Watchlet sees, then sends them to the host daemon. Compose folders must therefore be mounted into Watchlet at their host path; other mount layouts are not supported.

The command boundary owns:

- `docker compose --project-directory <dir> -f <compose> config --format json`
- `docker compose --project-directory <dir> -f <compose> pull <service>`
- `docker compose --project-directory <dir> -f <compose> up -d --no-deps <service>`
- `docker compose --project-directory <dir> -f <compose> up -d --no-deps --force-recreate <service>` for force mode
- `docker image inspect --format {{.Id}} <resolved-image>`
- `docker image rm <old-image-id>`
- the self-update helper commands described in [Self-update handoff](#self-update-handoff)

Structured command output must keep stdout and stderr separate. Machine-readable output, such as Compose config JSON, is parsed from stdout only; stderr is retained for diagnostics in logs.

Registry authentication is user-managed through Docker and Docker Compose configuration. Watchlet does not implement registry login, credential helpers, or custom registry authentication initially.

## Compose parsing and service classification

The parser supports only the subset needed for Watchlet:

- `services`
- service names
- `image`
- `build`
- `labels`

Labels may be map form or list form. A service is selected only when `watchlet.enable=true`.

Selected services are classified as:

- local build skip when `build:` is present
- registry-backed image service when `image:` is present and `build:` is absent
- invalid selected service when neither usable `image:` nor `build:` is present

The parser should fail clearly when the Compose file cannot be understood. It should not attempt full Compose specification support.

## Update pass

The core is one update pass:

1. load the configured Compose file list
2. for each Compose file, load and classify that file
3. log selected services and skipped services with reasons
4. record every selected image service’s current local image identity before any pulls for that Compose file
5. pull every selected image service through Docker Compose for that Compose file
6. inspect each selected image service’s local image identity again
7. classify each service as current, changed, forced, or failed
8. recreate changed services through Docker Compose, or recreate all selected image services when manual force mode is enabled, except an unchanged Watchlet service, which is logged as `skipped reason=force-skips-self` because a forced self-recreate would start a new Watchlet that repeats the same forced pass
9. identify the actual running Watchlet service from the current container's Docker Compose labels and bind mounts
10. defer only that current Watchlet service until all other configured Compose files and services have been processed
11. before deferred self-update work begins, log the owning Compose file's non-self result with `status=self-update-deferred`
12. after all non-self recreates across all Compose files finish, remove non-self previous image IDs before the deferred self-update; there is no cleanup after the handoff, because the old Watchlet and the helper still use the old Watchlet image, which the next Watchlet removes at the start of its first pass after it confirms the self-update succeeded
13. deduplicate cleanup by Compose file and old image ID
14. skip and log images Docker refuses to remove
15. process the deferred Watchlet self-update last by handing its recreate to a self-update helper, because recreating Watchlet stops the updater
16. report one concise overall summary when the pass is not interrupted by the self-update helper stopping the old Watchlet

Image identity checks use Docker’s resolved local image ID. Watchlet does not parse `docker compose pull` text as the source of truth.

An empty pre-pull identity is allowed because the image may not exist locally yet. After a successful pull, an empty post-pull identity is a service failure.

Long-running interval mode calls the same update-pass path repeatedly. It must not duplicate update logic.

When configured, one latest-pass JSON result is atomically replaced after each completed pass. It contains timestamps, status, and aggregate counts only. It is a local supervision seam, not a notification or integration framework. A self-recreate may interrupt the old process before this final write; the next completed pass replaces the result.

Watchlet self-update requires no separate environment variable. If the running Watchlet service is selected with `watchlet.enable=true`, the update pass identifies it from the current container's Docker Compose labels and bind mounts, inspecting cgroup-derived container IDs before hostname fallback. Its pre-pull identity is the image its running container uses, not the local tag, so a self-update that failed after the pull is retried on later passes and the image actually being replaced is the one cleaned up. It handles its pull/recreate last. A diagnostic failure while checking the current container is fatal for the pass; Watchlet must not fall back to normal-order self recreation when self identity is inconclusive. The self-update helper stops the active process during the final self-recreate; this must not happen before other services have completed their normal update checks, and the owning Compose file must have a pre-self-update result log before the handoff starts.

## Self-update handoff

Watchlet must never run the Compose recreate of its own service from inside its own container. Compose replaces a container by creating the new one, stopping the old one, then renaming and starting the new one, all driven by the Compose client. A client running inside the old container is killed when that container stops, which leaves the replacement created but never started and Watchlet down.

Instead, the deferred self-recreate is handed to one short-lived helper container that Docker keeps running after the old Watchlet stops:

```sh
docker run -d --name <watchlet-container-name>-self-update --network none \
  --volumes-from <watchlet-container-id> \
  -e <each Watchlet environment variable name> \
  --label watchlet.self-update.compose=<compose-path> \
  --label watchlet.self-update.service=<service> \
  --label watchlet.self-update.old-image=<previous-image-id> \
  --entrypoint docker <running-watchlet-image-id> \
  compose --project-directory <dir> -f <compose> up -d --no-deps [--force-recreate] <service>
```

- The helper runs the image of the Watchlet that is already running, so the handoff does not depend on the new image.
- `--volumes-from` gives it the Docker socket and every Compose path at the same paths Watchlet sees, with the same access modes.
- Environment variables are passed by name, so Compose interpolation sees the same values as Watchlet without printing them in the command line.
- It has no network because it only talks to the Docker socket. Self-update therefore requires the Docker socket mounted into Watchlet: when `DOCKER_HOST` points anywhere other than a `unix://` socket, such as a TCP socket proxy, the handoff is refused with `recreate-failed` before anything is stopped, and Watchlet must be updated by hand.
- It carries no Compose labels, so Compose never treats it as part of a project.
- The old-image label is set only when the image identity changed and a previous identity existed.

The helper is not removed automatically, so its exit code and Compose output survive the old Watchlet. At the start of every pass, after self-identification, Watchlet looks for a helper by its labels, not by its own container name, because the container that started the helper may have had another name, for example a one-off `docker compose run` or a container whose `container_name` changed. It collects the first helper whose service label is its own service and whose Compose label is one of its own Compose paths; any further helpers are collected on later passes. The name `<watchlet-container-name>-self-update` is reserved: when no matching helper exists but a container with that name does, it is left untouched and logged as a `self-update-failed` name clash, which also makes the next handoff fail safely on the name conflict.

- `docker container ls -a --filter label=watchlet.self-update.service=<service> --format {{.Names}}`
- `docker container inspect <helper>` for each listed helper, and for the reserved name when none matches
- `docker container wait <helper>` for at most 30 seconds when it is still running; a helper still running after that is logged as `status=pending reason=helper-running` and left for the next pass
- `docker container logs <helper>` for its Compose output, only when it exited non-zero; if its output cannot be read, the failure is still reported with its exit code and the helper is still removed
- `docker container rm <helper>`

A helper that exited with code 0 is logged as `operation=self-update status=ok`, and the image in its old-image label is removed right away, before any Compose work in that pass, because removing the helper deleted the only record of it; a refused removal is retried once with the pass's other cleanup. A helper that never ran, such as one left in Docker's `created` state, is a failed self-update even though Docker reports exit code 0, and its old image is not removed. A non-zero exit is logged as `operation=self-update status=failed reason=self-update-failed` with the helper's Compose output and counts as a failure in that pass. Failing to inspect, read, or remove the helper is logged with the same reason and also counts as a failure.

If starting the helper fails, nothing has been stopped yet: the old Watchlet keeps running and logs `recreate-failed`. If Compose fails inside the helper after stopping the old Watchlet, the exited helper stays on the host with its output in `docker logs <watchlet-container-name>-self-update` until a Watchlet collects it.

## Logging

Watchlet emits plain key-value logs suitable for Docker logs and diagnosis.

Logs should include:

- pass start and config context
- configured Compose files
- selected services
- skipped services and reasons
- pull/recreate/cleanup outcomes
- command failures with failure layer and Docker/Compose diagnostics when available
- per-Compose-file results
- final summary counts
- interval pass end and next sleep duration

Service-level logs must include `compose=...` and `service=...` when applicable so failures can be traced to the correct stack.

Important failure reasons include:

- `compose-parse`
- `image-inspect-failed`
- `pull-failed`
- `recreate-failed`
- `self-update-failed`
- `cleanup-failed`

## Cleanup policy

Cleanup is always targeted. Watchlet removes only previous image IDs from selected services that were successfully recreated because their image identity changed.

Cleanup must not run broad host-level prune commands. Cleanup failure is logged but does not make a successful update fail, because old images may be shared or still in use.

## Change policy

When a proposed change conflicts with `docs/vision.md` or this architecture, stop and surface the conflict instead of working around it.

Changes that broaden Watchlet beyond Compose-first image updates require an explicit revision to the vision or architecture.

Features should be rejected when they mainly add compatibility, integrations, or operational breadth without improving the core update workflow.
