# Vision

## Why Watchlet exists

Watchlet exists because automatic image updates are useful, but a large updater with broad Docker access is a lot to trust.

Watchtower proved the value of this workflow for homelabs and small servers. Watchlet keeps the useful core in a smaller, more readable project: update selected Docker Compose services, skip what is outside scope, and log exactly what happened.

## Who it is for

Watchlet is for trusted homelab and small-server operators who already manage services with Docker Compose.

The intended user has one or more Compose stacks and wants selected registry-backed services to stay current without manually checking, pulling, recreating, and cleaning up images.

## Product promise

Watchlet should feel controlled, readable, and boring.

It should:

- work only from explicitly configured Docker Compose files
- update only services labeled `watchlet.enable=true`
- update registry-backed `image:` services
- skip and report local `build:` services
- run once for manual use or repeatedly on a simple interval
- use Docker Compose as the action boundary
- clean up only old image IDs from services it successfully updated
- update its own selected Watchlet service without a separate self-update setting, handling that recreate last
- emit clear key-value logs for every meaningful decision and action

The value is confidence: users should understand what Watchlet can touch, why it acts, and what it refuses to touch.

## What Watchlet refuses to become

Watchlet is not a general Docker automation platform.

It should not become:

- a full container orchestrator
- a replacement for Docker Compose
- a local app build system
- a notification-first monitoring tool
- a broad plugin framework
- a compatibility clone of every Watchtower feature
- a broad host cleanup tool

Local `build:` services are skipped and reported rather than rebuilt automatically.

## Boundaries

Watchlet starts and stays Compose-first.

It prefers explicit paths, explicit labels, small dependencies, simple behavior, and clear logs over feature breadth.

Because Docker socket access is privileged host control, Watchlet keeps its trust surface narrow. Features that make actions harder to inspect or expand Watchlet beyond selected Compose image updates should be rejected unless they directly protect the core workflow.
