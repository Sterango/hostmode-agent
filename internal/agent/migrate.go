package agent

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Installs made before the HostMode rename run as container "redstonecore" from
// image redstonecore-agent, with RSC_* env vars in their compose file. Once a
// 2.x agent is running there and has proven itself, it moves the install to the
// HostMode names in place: same folder, data and credentials, new container,
// image, compose project and env var names. Every step can roll back.

const (
	legacyContainer    = "redstonecore"
	migrationDelay     = 3 * time.Minute
	migrationMarker    = ".hostmode-migration"
	migrationOptOutEnv = "HOSTMODE_SKIP_MIGRATION"
)

// migrateScript runs inside a docker:cli helper container whose working dir is
// the host folder holding the compose file ($1 is the compose file name). It must
// not depend on this agent process, which it stops halfway through.
const migrateScript = `
set -u
F="$1"
log() { echo "[HostMode migration] $*"; }
rollback() {
  log "failed: $1, rolling back"
  cat "$F.pre-hostmode" > "$F"
  [ -f .env.pre-hostmode ] && cat .env.pre-hostmode > .env
  docker rm -f hostmode >/dev/null 2>&1
  docker compose -p redstonecore up -d
  exit 1
}

cp -p "$F" "$F.pre-hostmode" || { log "cannot back up $F"; exit 1; }
[ -f .env ] && cp -p .env .env.pre-hostmode

sed -i \
  -e 's#ghcr.io/sterango/redstonecore-agent#ghcr.io/sterango/hostmode-agent#g' \
  -e 's/^\(  \)redstonecore:/\1hostmode:/' \
  -e 's/container_name: *redstonecore *$/container_name: hostmode/' \
  -e 's/RSC_/HOSTMODE_/g' \
  -e 's#redstonecore\.net#hostmode.net#g' \
  -e 's/RedstoneCore/HostMode/g' \
  "$F" || rollback "rewrite compose file"
# Pin the project name so plain "docker compose" in this folder (the rsc helper,
# Windows autostart) targets the new project even though the folder keeps its name.
# Write back into the same file (cat >, not mv) so the owner and mode are kept;
# the helper runs as root and a root-owned compose file would lock out ./rsc.
grep -q '^name:' "$F" || { { echo "name: hostmode"; cat "$F"; } > "$F.tmp" && cat "$F.tmp" > "$F" && rm -f "$F.tmp"; } || rollback "set project name"
if [ -f .env ]; then
  sed -i -e 's/^RSC_/HOSTMODE_/' -e 's/RedstoneCore/HostMode/g' .env || rollback "rewrite .env"
fi

docker compose config -q || rollback "new compose file is invalid"
docker compose pull --ignore-pull-failures || log "pull failed; using a local image if there is one"

log "replacing container redstonecore with hostmode"
docker stop -t 90 redstonecore
docker rm redstonecore
docker compose up -d || rollback "could not start the hostmode container"

# Up for 20s without restarting; a crash-looping container also reports Running.
sleep 20
[ "$(docker inspect -f '{{.State.Running}} {{.State.Restarting}} {{.RestartCount}}' hostmode 2>/dev/null)" = "true false 0" ] || rollback "hostmode container is not staying up"

# Replace the old ./rsc helper with ./hostmode (same commands, new names).
if [ -f rsc ] && [ ! -e hostmode ]; then
  sed -e 's/RedstoneCore/HostMode/g' -e 's/REDSTONECORE/HOSTMODE/g' \
      -e 's#ghcr.io/sterango/redstonecore-agent#ghcr.io/sterango/hostmode-agent#g' \
      -e 's#\./rsc#./hostmode#g' rsc > hostmode.tmp && cat hostmode.tmp > rsc && rm -f hostmode.tmp && mv rsc hostmode
fi
log "done"
`

// renameLegacyGameContainers renames game containers created before the rename
// (rsc-sat-<uuid>, rsc-gs-<uuid>) to the HostMode names. docker rename keeps the
// container's data, mounts and running state, so this is safe at any time.
func renameLegacyGameContainers() {
	out, err := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}").Output()
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(out)) {
		var newName string
		switch {
		case strings.HasPrefix(name, "rsc-sat-"):
			newName = "hostmode-sat-" + strings.TrimPrefix(name, "rsc-sat-")
		case strings.HasPrefix(name, "rsc-gs-"):
			newName = "hostmode-gs-" + strings.TrimPrefix(name, "rsc-gs-")
		default:
			continue
		}
		if err := exec.Command("docker", "rename", name, newName).Run(); err != nil {
			log.Printf("[Migration] Could not rename container %s: %v", name, err)
			continue
		}
		log.Printf("[Migration] Renamed container %s to %s", name, newName)
	}
}

// startLegacyMigration schedules the one-time rename for pre-HostMode installs.
func (a *Agent) startLegacyMigration() {
	if os.Getenv(migrationOptOutEnv) == "1" {
		return
	}
	go func() {
		time.Sleep(migrationDelay)
		a.migrateLegacyInstall()
	}()
}

func (a *Agent) migrateLegacyInstall() {
	if a.selfContainer() != legacyContainer {
		return
	}
	marker := filepath.Join(a.config.ConfigDir, migrationMarker)
	if _, err := os.Stat(marker); err == nil {
		// Attempted before. If it rolled back, don't retry on every restart.
		return
	}
	hostComposePath := a.findHostComposePath()
	if hostComposePath == "" {
		log.Printf("[Migration] Can't find this install's compose file; keeping the RedstoneCore names")
		return
	}

	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0644); err != nil {
		log.Printf("[Migration] Can't write %s, skipping: %v", marker, err)
		return
	}

	log.Printf("[Migration] Moving this install to the HostMode names (%s)", hostComposePath)
	a.saveResumeList()
	a.saveAllServers()

	hostComposeDir := filepath.Dir(hostComposePath)
	cmd := exec.Command("docker", "run", "--rm", "-d",
		"--name", "hostmode-migration",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", hostComposeDir+":"+hostComposeDir,
		"-w", hostComposeDir,
		"docker:cli",
		"sh", "-c", migrateScript, "migrate", filepath.Base(hostComposePath))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("[Migration] Could not start the migration helper: %v", err)
	}
}
