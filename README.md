# Watchlet

Watchlet keeps selected Docker Compose services up to date without handing a broad updater control over every container on the host.

If you run a homelab or small server with Compose stacks, you probably have a few services that should track their published image tags and a few services that should not be touched automatically. Watchlet gives you the boring middle path: label the services you want managed, point Watchlet at your Compose files, and let it pull, recreate, clean up, and log exactly what happened.

## What Watchlet does

Watchlet reads one or more explicitly configured Docker Compose files and processes each file as its own Compose project.

For services labeled `watchlet.enable=true`, Watchlet:

1. resolves the service image through Docker Compose
2. records the current local image identity
3. runs `docker compose pull` for that service
4. records the image identity again
5. recreates the service when the identity changed
6. removes only the old image IDs from services it successfully updated

Watchlet is intentionally narrow:

- Docker Compose files only.
- Explicit Compose paths only; no project discovery.
- Explicit opt-in only via `watchlet.enable=true`.
- Registry-backed `image:` services are update candidates.
- Services with `build:` are skipped and logged, even if they also define `image:`.
- Cleanup is targeted; Watchlet does not run broad `docker image prune`.
- Logs are plain key-value lines for easy Docker log inspection.

## Quick Docker Compose setup

Run Watchlet as a small sidecar service with access to Docker and read-only access to the Compose projects you want it to manage:

```yaml
services:
  watchlet:
    image: ghcr.io/mmarfil/watchlet:latest
    container_name: watchlet
    restart: unless-stopped
    volumes:
      # Required: lets Watchlet call Docker and Docker Compose on the host.
      - /var/run/docker.sock:/var/run/docker.sock

      # Mount each Compose project directory read-only.
      # Left side: path on your host.
      # Right side: path inside the Watchlet container.
      - /Volumes/Docker/Media:/media:ro
      - /Volumes/Docker/System:/system:ro
    environment:
      # Container-side paths to the Compose files mounted above.
      # Separate multiple Compose files with commas.
      WATCHLET_COMPOSE: /media/docker-compose.yml,/system/docker-compose.yml
      WATCHLET_INTERVAL: 24h
    labels:
      # Optional: lets Watchlet update its own image too.
      - watchlet.enable=true
```

The published image includes the `watchlet` binary plus Docker CLI and the Docker Compose plugin, because Watchlet performs updates by calling `docker compose` against the mounted Docker socket.

The `watchlet.enable=true` label on the Watchlet service lets Watchlet update itself. Watchlet handles its own service last, after the rest of the pass is finished, because recreating itself restarts the updater. If you prefer to update Watchlet manually, omit that label and run `docker compose pull watchlet && docker compose up -d watchlet` when you want a new version.

Then opt in individual services inside those Compose files:

```yaml
services:
  app:
    image: ghcr.io/example/app:latest
    labels:
      watchlet.enable: "true"
```

List-style labels also work:

```yaml
services:
  app:
    image: ghcr.io/example/app:latest
    labels:
      - watchlet.enable=true
```

Local builds are not supported. Services that define `build:` are skipped and reported, even when they also define `image:`.

## Configuration

Watchlet supports environment variables for container use and CLI flags for local/manual runs. CLI flags override environment variables.

| Setting | CLI | Environment | Default |
| --- | --- | --- | --- |
| Compose files | `--compose PATH` repeated | `WATCHLET_COMPOSE=/path/a.yml,/path/b.yml` | required |
| Interval | `--interval DURATION` | `WATCHLET_INTERVAL=24h` | `24h` |
| One-shot mode | `--once` | none | interval mode |
| Force recreate | `--force` with `--once` | none | disabled |

Durations use Go duration syntax: `24h`, `1h`, `30m`, and so on.

`WATCHLET_COMPOSE` is a comma-separated list. `--compose` may be repeated:

```sh
watchlet --once \
  --compose /media/docker-compose.yml \
  --compose /system/docker-compose.yml
```

## CLI examples

Run one update pass and exit:

```sh
watchlet --once --compose /media/docker-compose.yml
```

Run one forced pass. Force mode still respects labels and skips `build:` services, but recreates selected `image:` services after pulling even when the image identity is unchanged:

```sh
watchlet --once --force --compose /media/docker-compose.yml
```

Run continuously on the default 24-hour interval:

```sh
watchlet --compose /media/docker-compose.yml
```

Run continuously with environment configuration:

```sh
WATCHLET_COMPOSE=/media/docker-compose.yml,/system/docker-compose.yml \
WATCHLET_INTERVAL=24h \
watchlet
```

## How Watchlet handles Compose projects

Watchlet passes each configured Compose file to Docker Compose with that file’s directory as the project directory. For example, `/media/docker-compose.yml` runs with `/media` as the Compose project directory. This keeps stack-local `.env` files and Compose interpolation aligned with normal Compose behavior.

For image identity checks, Watchlet resolves the effective image reference with Docker Compose config output before running `docker image inspect`. It does not parse `docker compose pull` prose to decide whether an update occurred.

If one Compose file or service fails, Watchlet logs the failure, continues with the remaining configured Compose files, and reports a failed final summary for that pass.

## Logs

Watchlet emits key-value logs such as:

```text
level=INFO msg=watchlet action=service-selected compose=/media/docker-compose.yml service=app image=ghcr.io/example/app:latest
level=INFO msg=watchlet action=action-result operation=pull compose=/media/docker-compose.yml service=app status=ok
level=INFO msg=watchlet action=cleanup compose=/media/docker-compose.yml service=app image_id=sha256:old status=ok
```

Read logs with your normal container log tooling, for example `docker logs watchlet` or `docker compose logs watchlet`. Command failures include the failing layer, compose path, service when applicable, and Docker/Compose diagnostics when available, including captured stdout/stderr from Docker or Compose.

## Open source, but intentionally not collaborative

Watchlet is open source so you can read it, trust it, copy it, and run it without mystery.

It is not set up as a contribution-driven project. That is on purpose, and not meant to be unfriendly. The whole point is to keep Watchlet dumb, small, and easy to reason about. More features, more knobs, and more edge cases would quickly turn it into the kind of tool this project is trying not to be.

If Watchlet is almost what you want but not quite, please fork it and make it your own. Add notifications, dashboards, policies, plugins, a mascot, whatever makes your setup happy. This version is just going to stay boring.

## What Watchlet is not

Watchlet is not an orchestrator, dashboard, notification system, registry client, or local build system. It is a small updater for explicitly selected Docker Compose `image:` services.
