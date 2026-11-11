package gameserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContainerSpec runs a game from its own (non-LinuxGSM) image, e.g. a
// Proton/Wine image for a Windows-only dedicated server. Everything else —
// create, restore, status, logs, settings — reuses the generic game-server
// plumbing; only how the container is built, stopped and polled differs.
type ContainerSpec struct {
	Image string
	UID   int // owner the image's server process runs as

	// Subdirectories of the server's data dir to create and chown to UID.
	DataDirs []string
	// Mounts returns `-v` values given the host path of the data dir.
	Mounts func(hostData string) []string
	// Env returns `-e` values. settings are the panel's saved game settings.
	Env func(s *Server, settings map[string]interface{}) []string

	RCONOffset     int    // RCON port = base + RCONOffset; -1 if none
	PlayersCommand string // RCON command whose output CountPlayers parses
	CountPlayers   func(out string) int
	SaveCommand    string // RCON command run before a graceful stop
	StopTimeout    int    // seconds docker waits on stop before SIGKILL
}

const (
	secretsFile  = "hostmode-secrets.json"
	settingsFile = "hostmode-game-settings.json" // written by the agent's game panel
)

// Secret returns a per-server generated secret (e.g. an RCON/admin password),
// creating and persisting it on first use so it survives agent restarts.
func (s *Server) Secret(key string) string {
	path := filepath.Join(s.DataDir, secretsFile)
	m := map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	if v := m[key]; v != "" {
		return v
	}
	m[key] = randomToken()
	if data, err := json.Marshal(m); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
	return m[key]
}

// Settings reads the panel's saved game settings (empty when none yet).
func (s *Server) Settings() map[string]interface{} {
	m := map[string]interface{}{}
	if data, err := os.ReadFile(filepath.Join(s.DataDir, settingsFile)); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func settingStr(m map[string]interface{}, key, def string) string {
	if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func settingBool(m map[string]interface{}, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

// prepareContainerDirs creates and chowns the image's data dirs.
func (s *Server) prepareContainerDirs() error {
	c := s.spec.Container
	for _, d := range append([]string{""}, c.DataDirs...) {
		p := filepath.Join(s.DataDir, d)
		if err := os.MkdirAll(p, 0o777); err != nil {
			return err
		}
		_ = os.Chown(p, c.UID, c.UID)
	}
	return nil
}

func (s *Server) containerRunArgs() []string {
	c := s.spec.Container
	args := []string{
		"run", "-d",
		"--name", s.container(),
		"--restart", "unless-stopped",
		"--network", "host",
	}
	if c.StopTimeout > 0 {
		args = append(args, "--stop-timeout", fmt.Sprintf("%d", c.StopTimeout))
	}
	for _, m := range c.Mounts(s.HostData) {
		args = append(args, "-v", m)
	}
	for _, e := range c.Env(s, s.Settings()) {
		args = append(args, "-e", e)
	}
	if s.AllocatedRAM > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", s.AllocatedRAM))
	}
	return append(args, c.Image)
}

// rcon runs a command over the game's RCON port with the generated admin password.
func (s *Server) rcon(command string) (string, error) {
	c := s.spec.Container
	if c == nil || c.RCONOffset < 0 {
		return "", fmt.Errorf("%s has no RCON", s.spec.Label)
	}
	return RCON(fmt.Sprintf("127.0.0.1:%d", s.BasePort+c.RCONOffset), s.Secret("admin_password"), command)
}

// SendCommand runs a console command over RCON (container games only).
func (s *Server) SendCommand(command string) (string, error) { return s.rcon(command) }
