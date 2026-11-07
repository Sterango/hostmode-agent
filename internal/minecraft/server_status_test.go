package minecraft

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

// A pack's launch script can exit 0 after the game inside it fails to boot, as
// Craftoria's did. That must read as a crash so the panel can explain it.
func TestCleanExitBeforeReadyIsCrash(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'Failed to start the minecraft server'\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	s := &Server{Status: StatusStarting, cmd: exec.Command("/bin/sh", script), stopChan: make(chan struct{})}
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s.monitorProcess()

	if s.Status != StatusCrashed {
		t.Errorf("status = %q, want crashed", s.Status)
	}
}

// A kill exits with an error, but it's what was asked for.
func TestKilledServerIsStoppedNotCrashed(t *testing.T) {
	s := &Server{Status: StatusRunning, cmd: exec.Command("/bin/sh", "-c", "sleep 30"), stopChan: make(chan struct{})}
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		s.monitorProcess()
		close(done)
	}()

	if err := s.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done

	if s.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", s.Status)
	}
}

func TestScriptRestartOverrides(t *testing.T) {
	script := filepath.Join(t.TempDir(), "startserver.sh")
	body := "if [ \"${ATM9E_RESTART:-true}\" = \"false\" ]; then\n  exit 0\nfi\n\"${ATM9E_JAVA:-java}\" nogui\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}

	got := scriptRestartOverrides(script)
	if len(got) != 1 || got[0] != "ATM9E_RESTART=false" {
		t.Errorf("overrides = %v, want [ATM9E_RESTART=false]", got)
	}
}

// All the Mods 9 Expert logs a 120KB line during boot. Reading has to carry on past it,
// or the pipe fills and the game freezes.
func TestReadLinesSurvivesVeryLongLine(t *testing.T) {
	long := strings.Repeat("x", 200*1024)
	input := "before\n" + long + "\nafter\nno newline"

	var got []string
	readLines(strings.NewReader(input), func(line string) { got = append(got, line) })

	if len(got) != 4 {
		t.Fatalf("got %d lines, want 4", len(got))
	}
	if got[0] != "before" || got[2] != "after" || got[3] != "no newline" {
		t.Errorf("lines around the long one changed: %q, %q, %q", got[0], got[2], got[3])
	}
	if len(got[1]) != maxConsoleLine {
		t.Errorf("long line kept %d bytes, want %d", len(got[1]), maxConsoleLine)
	}
}

// The real failure end to end: a child that logs a huge line and then keeps logging
// must be able to finish, which it can't if nothing is draining its output.
func TestServerOutputKeepsDrainingAfterLongLine(t *testing.T) {
	dir := t.TempDir()
	done := filepath.Join(dir, "finished")
	script := "head -c 120000 /dev/zero | tr '\\0' x; echo; " +
		"i=0; while [ $i -lt 3000 ]; do echo 'filler line that fills the pipe buffer quickly'; i=$((i+1)); done; touch " + done

	cmd := exec.Command("/bin/sh", "-c", script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	go s.readOutput(stdout)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(done); err == nil {
			cmd.Wait()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cmd.Process.Kill()
	t.Fatal("child blocked writing output: the reader stopped draining the pipe")
}
