package minecraft

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseBedrockLine(t *testing.T) {
	cases := []struct {
		line              string
		event, name, xuid string
		ready             bool
	}{
		{"[2026-11-11 10:00:00:000 INFO] Server started.", "", "", "", true},
		{"[2026-11-11 10:00:05:000 INFO] Player connected: Steve Two, xuid: 2535412345678901", "join", "Steve Two", "2535412345678901", false},
		{"[2026-11-11 10:09:05:000 INFO] Player disconnected: Steve Two, xuid: 2535412345678901, pfid: abc", "leave", "Steve Two", "2535412345678901", false},
		{"[2026-11-11 10:00:00:000 INFO] Version: 1.26.63.1", "", "", "", false},
	}
	for _, c := range cases {
		e, n, x, r := parseBedrockLine(c.line)
		if e != c.event || n != c.name || x != c.xuid || r != c.ready {
			t.Errorf("%q: got %q %q %q %v", c.line, e, n, x, r)
		}
	}
}

func TestBedrockConsoleTracksPlayers(t *testing.T) {
	s := NewServer(ServerConfig{Type: TypeBedrock})
	s.Status = StatusStarting
	s.parseConsoleLine("[x INFO] Server started.")
	if s.Status != StatusRunning {
		t.Fatalf("status = %s", s.Status)
	}
	s.parseConsoleLine("[x INFO] Player connected: Alex, xuid: 1")
	s.parseConsoleLine("[x INFO] Player connected: Alex, xuid: 1") // duplicate
	s.parseConsoleLine("[x INFO] Player connected: Sam, xuid: 2")
	s.parseConsoleLine("[x INFO] Player disconnected: Alex, xuid: 1, pfid: z")
	if s.PlayerCount != 1 || s.CurrentPlayers[0] != "Sam" {
		t.Fatalf("players = %v", s.CurrentPlayers)
	}
}

func TestBedrockMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := ReadBedrockMarker(dir); ok {
		t.Fatal("marker found in empty dir")
	}
	if err := WriteBedrockMarker(dir, "1.26.63.1", 3072); err != nil {
		t.Fatal(err)
	}
	v, ram, ok := ReadBedrockMarker(dir)
	if !ok || v != "1.26.63.1" || ram != 3072 {
		t.Fatalf("got %q %d %v", v, ram, ok)
	}
}

func TestBedrockHostPath(t *testing.T) {
	HostDataRoot, AgentDataRoot = "/srv/hostmode/data", "/data"
	defer func() { HostDataRoot, AgentDataRoot = "", "" }()
	s := NewServer(ServerConfig{DataDir: "/data/servers/my-world", Type: TypeBedrock})
	p, err := s.hostPath()
	if err != nil || p != "/srv/hostmode/data/servers/my-world" {
		t.Fatalf("hostPath = %q %v", p, err)
	}
	s.DataDir = "/elsewhere/x"
	if _, err := s.hostPath(); err == nil {
		t.Fatal("expected error for dir outside data root")
	}
}

func TestEnsureBedrockProperties(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(ServerConfig{DataDir: dir, Type: TypeBedrock, Port: 19140, MaxPlayers: 12})
	// No file yet: BDS creates it, we don't write Java defaults.
	if err := s.EnsureServerProperties(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "server.properties")); !os.IsNotExist(err) {
		t.Fatal("should not create server.properties for Bedrock")
	}
	os.WriteFile(filepath.Join(dir, "server.properties"), []byte("server-port=19132\nserver-portv6=19133\nmax-players=10\n"), 0644)
	if err := s.EnsureServerProperties(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "server.properties"))
	if string(got) != "server-port=19140\nserver-portv6=19141\nmax-players=12\n" {
		t.Fatalf("properties = %q", got)
	}
}
