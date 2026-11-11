# HostMode Agent (formerly RedstoneCore)

## Versioning Scheme

Use semantic versioning with patch numbers 0-9:
- `1.3.0` → `1.3.1` → ... → `1.3.9` → `1.4.0`
- Increment the patch number (last digit) for minor fixes/features
- When patch reaches 9, bump the minor version and reset patch to 0
- Example: Current is `1.3.0`, next would be `1.3.1`, not `1.4.0`

## Releasing a New Version

To release a new version:

1. Update the version in `Dockerfile` (line ~20):
   ```dockerfile
   -ldflags="-w -s -X github.com/sterango/hostmode-agent/internal/version.Version=X.Y.Z -X github.com/sterango/hostmode-agent/internal/version.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
   ```

2. Update `AGENT_LATEST_VERSION` in the web app `.env` (`/mnt/Drive1/palahace/websites/redstonecore.net/.env`):
   ```
   AGENT_LATEST_VERSION=X.Y.Z
   ```

3. Commit and push both repos - GitHub Action will build and push to GHCR

4. Users can update via UI "Update Now" button or `./hostmode update` (`./rsc update` on pre-rename installs)

## How Self-Update Works

The agent self-update process:
1. Web panel sends `update_agent` command via WebSocket
2. Agent finds its own container (`hostmode`, or `redstonecore` on pre-rename installs) and pulls the image it was started from
3. Agent restarts itself with that container's compose project: `docker compose -p <project> up -d --force-recreate`

## Rename compatibility (do not break)

CI publishes every build to BOTH `ghcr.io/sterango/hostmode-agent` and `ghcr.io/sterango/redstonecore-agent`.
Installs made before the HostMode rename have `redstonecore-agent:latest`, container `redstonecore`,
project `redstonecore` and `RSC_*` env vars baked into their compose file. Keep publishing the old image
name, keep the `RSC_*` env fallback in `config.getenv`, and keep `redstonecore` in `agentContainerNames`.

## Legacy install migration (`internal/agent/migrate.go`)

3 minutes after a 2.x agent starts inside a `redstonecore` container, it moves the install to the HostMode
names once: a `docker:cli` helper backs up `docker-compose.yml` → `.pre-hostmode` (and `.env`), rewrites
image/service/container/`RSC_*` names, adds `name: hostmode` (so plain `docker compose` and `./rsc` in the old
folder still work), swaps the container and checks it stays up (not restarting). Any failure restores the backups and the
old container. The folder keeps its name. `/config/.hostmode-migration` marks the attempt so it never
loops. Opt out with `HOSTMODE_SKIP_MIGRATION=1`. It needs the `hostmode-agent` GHCR package to be public.

Requirements for self-update:
- Container must run as `user: root` (for Docker socket access)
- Docker socket mounted: `/var/run/docker.sock:/var/run/docker.sock`
- Compose file mounted: `./docker-compose.yml:/docker-compose.yml:ro`

## Server Types

Supported server types and their download sources:
- **vanilla** - Mojang official launcher manifest
- **paper** - PaperMC API
- **fabric** - Fabric installer API
- **forge** - Forge Maven repository
- **neoforge** - NeoForge Maven repository
- **velocity** - PaperMC API (proxy)
- **bungeecord** - Waterfall via PaperMC API (proxy)
- **spigot** - BuildTools (compiled, cached in `/data/.spigot-cache`)
