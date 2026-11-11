package minecraft

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Minecraft Bedrock Dedicated Server is a glibc binary, and the agent image is
// Alpine (musl), so it can't run in-process like a Java server. Instead the
// agent runs `docker run -i` of the community image itzg/minecraft-bedrock-server
// (which downloads the requested BDS version and forwards stdin to the server)
// as the server's child process. That keeps stdin console, the `stop` command,
// log parsing and exit/crash detection identical to Java servers.

// BedrockImage is the Bedrock Dedicated Server image.
const BedrockImage = "itzg/minecraft-bedrock-server:latest"

// HostDataRoot is the host path backing the agent's data dir (AgentDataRoot);
// sibling containers bind-mount host paths. Set by the agent at startup.
var (
	HostDataRoot  string
	AgentDataRoot string
)

// BedrockContainerName is the docker container name for a Bedrock server.
func BedrockContainerName(uuid string) string { return "hostmode-mcb-" + uuid }

// IsBedrock reports whether the server is a Bedrock server.
func (s *Server) IsBedrock() bool { return s.Type == TypeBedrock }

// hostPath maps the server's in-agent data dir to its host path.
func (s *Server) hostPath() (string, error) {
	if HostDataRoot == "" || AgentDataRoot == "" {
		return "", fmt.Errorf("host data dir unknown; set HOSTMODE_HOST_DATA_DIR")
	}
	rel, err := filepath.Rel(AgentDataRoot, s.DataDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("server dir %s is outside %s", s.DataDir, AgentDataRoot)
	}
	return filepath.Join(HostDataRoot, rel), nil
}

// bedrockVersionEnv maps the panel's version to the image's VERSION value.
func bedrockVersionEnv(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "latest":
		return "LATEST"
	case "preview":
		return "PREVIEW"
	}
	return v
}

// bedrockCommand builds the foreground `docker run -i` for this server.
func (s *Server) bedrockCommand() (*exec.Cmd, error) {
	host, err := s.hostPath()
	if err != nil {
		return nil, err
	}
	name := BedrockContainerName(s.UUID)
	_ = exec.Command("docker", "rm", "-f", name).Run() // stale container from a crash

	args := []string{
		"run", "--rm", "-i",
		"--name", name,
		"--network", "host", // Bedrock is UDP; host networking avoids remaps
		"-v", host + ":/data",
		"-e", "EULA=TRUE",
		"-e", "VERSION=" + bedrockVersionEnv(s.MinecraftVersion),
		"-e", fmt.Sprintf("SERVER_PORT=%d", s.Port),
		"-e", fmt.Sprintf("SERVER_PORT_V6=%d", s.Port+1),
		"-e", fmt.Sprintf("MAX_PLAYERS=%d", s.MaxPlayers),
	}
	if s.AllocatedRAM > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", s.AllocatedRAM))
	}
	args = append(args, BedrockImage)
	return exec.Command("docker", args...), nil
}

// removeBedrockContainer force-removes the container (used by Kill: killing the
// `docker run` client alone would leave the server running).
func (s *Server) removeBedrockContainer() {
	_ = exec.Command("docker", "rm", "-f", BedrockContainerName(s.UUID)).Run()
}

// Bedrock log lines look like:
//
//	[2026-11-11 10:00:00:000 INFO] Server started.
//	[2026-11-11 10:00:05:000 INFO] Player connected: Steve, xuid: 2535412345678901
//	[2026-11-11 10:09:05:000 INFO] Player disconnected: Steve, xuid: 2535412345678901, pfid: abc
var (
	bedrockJoinRe  = regexp.MustCompile(`Player connected: (.+?), xuid: (\d*)`)
	bedrockLeaveRe = regexp.MustCompile(`Player disconnected: (.+?), xuid: (\d*)`)
)

// parseBedrockLine extracts a player event or the ready signal from one line.
func parseBedrockLine(line string) (event, name, xuid string, ready bool) {
	if m := bedrockJoinRe.FindStringSubmatch(line); m != nil {
		return "join", m[1], m[2], false
	}
	if m := bedrockLeaveRe.FindStringSubmatch(line); m != nil {
		return "leave", m[1], m[2], false
	}
	return "", "", "", strings.Contains(line, "Server started.")
}

// ensureBedrockProperties keeps port/IPv6 port/max-players pinned. When the file
// doesn't exist yet BDS creates it on first start from the image's env.
func (s *Server) ensureBedrockProperties() error {
	propsPath := filepath.Join(s.DataDir, "server.properties")
	data, err := os.ReadFile(propsPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "server-port="):
			lines[i] = fmt.Sprintf("server-port=%d", s.Port)
		case strings.HasPrefix(line, "server-portv6="):
			lines[i] = fmt.Sprintf("server-portv6=%d", s.Port+1)
		case strings.HasPrefix(line, "max-players="):
			lines[i] = fmt.Sprintf("max-players=%d", s.MaxPlayers)
		}
	}
	return os.WriteFile(propsPath, []byte(strings.Join(lines, "\n")), 0644)
}

// parseBedrockConsoleLine tracks players and readiness from Bedrock's log format.
func (s *Server) parseBedrockConsoleLine(line string) {
	event, name, xuid, ready := parseBedrockLine(line)
	if ready {
		s.Status = StatusRunning
		return
	}
	if event == "" {
		return
	}
	s.playersMutex.Lock()
	idx := -1
	for i, p := range s.CurrentPlayers {
		if p == name {
			idx = i
			break
		}
	}
	if event == "join" && idx < 0 {
		s.CurrentPlayers = append(s.CurrentPlayers, name)
	} else if event == "leave" && idx >= 0 {
		s.CurrentPlayers = append(s.CurrentPlayers[:idx], s.CurrentPlayers[idx+1:]...)
	}
	s.PlayerCount = len(s.CurrentPlayers)
	s.playersMutex.Unlock()
	if s.onPlayerEvent != nil {
		s.onPlayerEvent(event, name, xuid)
	}
}

// bedrockMarker persists what rediscovery can't infer from a Bedrock server dir
// (there's no JAR and no -Xmx), so an agent restart doesn't bring it back as Paper.
const bedrockMarker = ".bedrock"

type bedrockMeta struct {
	Version      string `json:"version"`
	AllocatedRAM int    `json:"allocated_ram_mb"`
}

// WriteBedrockMarker records a Bedrock server's version and RAM.
func WriteBedrockMarker(dir, version string, ramMB int) error {
	data, err := json.Marshal(bedrockMeta{Version: version, AllocatedRAM: ramMB})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, bedrockMarker), data, 0644)
}

// ReadBedrockMarker returns the saved version/RAM; ok is false for non-Bedrock dirs.
func ReadBedrockMarker(dir string) (version string, ramMB int, ok bool) {
	data, err := os.ReadFile(filepath.Join(dir, bedrockMarker))
	if err != nil {
		return "", 0, false
	}
	var m bedrockMeta
	_ = json.Unmarshal(data, &m)
	return m.Version, m.AllocatedRAM, true
}

// RemoveBedrockMarker clears the marker (server switched away from Bedrock).
func RemoveBedrockMarker(dir string) { _ = os.Remove(filepath.Join(dir, bedrockMarker)) }
