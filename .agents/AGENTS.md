# Watchlet

Watchlet is a small, Compose-native updater for selected Docker image services.

## Project contract

- Treat `docs/vision.md` and `docs/architecture.md` as the current product and architecture contract.
- Read `docs/vision.md` before product-shaping, scope, UX, or feature work.
- Read `docs/architecture.md` before implementation, dependency, runtime, or architectural changes.
- When a request conflicts with or goes beyond either document, say so and propose a revision rather than building around it.

## Current product surface

- Watchlet targets explicitly configured Docker Compose files only.
- Services opt in with `watchlet.enable=true`.
- Registry-backed `image:` services are update candidates.
- Any selected service with `build:` is skipped and reported, even if it also has `image:`.
- Multiple Compose files are supported through repeated `--compose` flags or comma-separated `WATCHLET_COMPOSE`.
- CLI flags override environment variables.
- Default interval is `24h`.
- `watchlet` runs interval mode; `watchlet --once` runs one pass.
- `watchlet --once --force` recreates selected image services after pulling even when identity is unchanged.
- A selected Watchlet service updates itself automatically without an environment variable and is handled last in the pass.
- Watchlet uses Docker Compose as the action boundary and anchors commands with the Compose file’s project directory.
- Cleanup is targeted to old image IDs from successfully changed/recreated services; never add broad prune behavior without revising the architecture.
- Logs are plain key-value lines with compose/service attribution and command diagnostics when available.
- An optional result file records timestamps, status, and counts for the latest completed pass.

## Shipping convention

- When the user asks to commit or push release-worthy changes, ask whether to bump the release version before committing.
- When the user says `ship`, commit the completed work, push `main`, and create the next `v1.x.y` GitHub release.
- Use a concise release title and notes that summarize user-facing changes and image tags.
- Creating a release is expected to trigger the GHCR publish workflow for versioned tags.

## Implementation guidance

- Keep the project small, auditable, and dependency-light.
- Prefer container configuration through `WATCHLET_COMPOSE`, `WATCHLET_INTERVAL`, and `WATCHLET_RESULT_FILE`; keep CLI flags for local/manual runs.
- Keep interval mode as a wrapper around the single update-pass path.
- Keep Docker/Compose execution isolated behind `internal/dockercompose`.
- Keep parser/classifier ownership in `internal/composefile`; do not push build/image/label invariants downstream.
- Keep update decisions in `internal/updatepass`; do not parse Docker Compose pull prose as truth.
- Update `README.md`, this file, `AGENTS.md`, `docs/vision.md`, and `docs/architecture.md` together when product behavior changes.
