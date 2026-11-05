package minecraft

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerStaysStartingUntilReadyLine(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'Downloading library foo'\nread go\necho 'Done (1.0s)! For help, type \"help\"'\nread hold\n"
	if err := os.WriteFile(filepath.Join(dir, "startserver.sh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	s := NewServer(ServerConfig{Type: TypeForge, MinecraftVersion: "1.20.1", AllocatedRAM: 512, DataDir: dir})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Kill()

	if s.Status != StatusStarting {
		t.Fatalf("status after launch = %s, want starting", s.Status)
	}
	if !s.IsActive() {
		t.Error("a starting server should be active")
	}

	if err := s.SendCommand("go"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.Status != StatusRunning && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Status != StatusRunning {
		t.Fatalf("status after ready line = %s, want running", s.Status)
	}
}

func TestStopWorksWhileStarting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "startserver.sh"), []byte("#!/bin/sh\nread line\n"), 0755); err != nil {
		t.Fatal(err)
	}

	s := NewServer(ServerConfig{Type: TypeForge, MinecraftVersion: "1.20.1", AllocatedRAM: 512, DataDir: dir})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if s.IsActive() {
		t.Errorf("status after stop = %s", s.Status)
	}
}

func TestOnlyGameServersWaitForReadyLine(t *testing.T) {
	cases := map[ServerType]bool{
		TypeVanilla: true, TypePaper: true, TypeSpigot: true, TypeForge: true, TypeNeoForge: true, TypeFabric: true,
		TypeBungeeCord: false, TypeVelocity: false, TypeOther: false,
	}
	for serverType, want := range cases {
		if got := (&Server{Type: serverType}).logsReadyLine(); got != want {
			t.Errorf("%s = %v, want %v", serverType, got, want)
		}
	}
}
