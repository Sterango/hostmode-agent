package agent

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
  cp "$F.pre-hostmode" "$F"
  [ -f .env.pre-hostmode ] && cp .env.pre-hostmode .env
  docker rm -f hostmode >/dev/null 2>&1
  docker compose -p redstonecore up -d
  exit 1
}

cp "$F" "$F.pre-hostmode" || { log "cannot back up $F"; exit 1; }
[ -f .env ] && cp .env .env.pre-hostmode

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
grep -q '^name:' "$F" || { { echo "name: hostmode"; cat "$F"; } > "$F.tmp" && mv "$F.tmp" "$F"; } || rollback "set project name"
if [ -f .env ]; then
  sed -i -e 's/^RSC_/HOSTMODE_/' -e 's/RedstoneCore/HostMode/g' .env || rollback "rewrite .env"
fi

docker compose config -q || rollback "new compose file is invalid"
docker compose pull --ignore-pull-failures || log "pull failed; using a local image if there is one"

log "replacing container redstonecore with hostmode"
docker stop -t 90 redstonecore
docker rm redstonecore
docker compose up -d || rollback "could not start the hostmode container"

sleep 20
[ "$(docker inspect -f '{{.State.Running}}' hostmode 2>/dev/null)" = "true" ] || rollback "hostmode container is not running"

[ -e rsc ] && [ ! -e hostmode ] && ln -s rsc hostmode
log "done"
`

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
