package minecraft

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMcJavaMajor(t *testing.T) {
	cases := map[string]int{
		"1.12.2": 8, "1.16.5": 8, "1.17": 17, "1.18.2": 17, "1.20": 17, "1.20.1": 17, "1.20.4": 17,
		"1.20.5": 21, "1.20.6": 21, "1.21": 21, "1.21.1": 21, "26.1": 25, "26.1.2": 25, "27.2": 25,
		"": 0, "latest": 0, "26w14a": 0,
	}
	for version, want := range cases {
		if got := mcJavaMajor(version); got != want {
			t.Errorf("mcJavaMajor(%q) = %d, want %d", version, got, want)
		}
	}
}

func TestJarJavaMajorIgnoresVersionForProxies(t *testing.T) {
	if got := jarJavaMajor(TypeBungeeCord, "26.1.2"); got != 0 {
		t.Errorf("bungeecord = %d, want 0", got)
	}
	if got := jarJavaMajor(TypeVelocity, "26.1.2"); got != 0 {
		t.Errorf("velocity = %d, want 0", got)
	}
	if got := jarJavaMajor(TypeVanilla, "26.1.2"); got != 25 {
		t.Errorf("vanilla = %d, want 25", got)
	}
}

func TestScriptJavaMajor(t *testing.T) {
	cases := []struct {
		name, script, mc string
		want             int
	}{
		{"pack demands newer than its Minecraft version", `if [ ! "$JAVA_VERSION" -ge 25 ]; then`, "1.20.1", 25},
		{"check satisfied by the default", `if [ ! "$JAVA_VERSION" -ge 21 ]; then`, "1.21.1", 0},
		{"lt form", `if [ "${JAVA_VERSION}" -lt 24 ]; then`, "1.21.1", 24},
		{"no check never downgrades from the default", "java @args.txt nogui", "1.20.1", 0},
		{"no check on a game that needs newer", "java @args.txt nogui", "26.1.2", 25},
		{"unrelated numbers are ignored", `if [ "$RETRIES" -ge 30 ]; then`, "1.21.1", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "startserver.sh")
			if err := os.WriteFile(path, []byte(c.script), 0755); err != nil {
				t.Fatal(err)
			}
			if got := scriptJavaMajor(path, c.mc); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestJavaEnvPutsRuntimeFirstOnPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("JAVA_HOME", "/opt/java/openjdk")

	var paths, homes []string
	for _, kv := range javaEnv("/data/java/25/bin/java") {
		if strings.HasPrefix(kv, "PATH=") {
			paths = append(paths, kv)
		}
		if strings.HasPrefix(kv, "JAVA_HOME=") {
			homes = append(homes, kv)
		}
	}
	if len(paths) != 1 || paths[0] != "PATH=/data/java/25/bin:/usr/bin" {
		t.Errorf("PATH entries = %v", paths)
	}
	if len(homes) != 1 || homes[0] != "JAVA_HOME=/data/java/25" {
		t.Errorf("JAVA_HOME entries = %v", homes)
	}
}

// fakeJavaArchive builds a tarball shaped like a Temurin release, with a top-level
// directory and an executable bin/java.
func fakeJavaArchive(t *testing.T, javaScript string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "jdk-25-jre/bin/java", Mode: 0755, Size: int64(len(javaScript))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(javaScript)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// serveJava stands in for Adoptium and counts archive downloads.
func serveJava(t *testing.T, archive []byte, checksum string) *int {
	t.Helper()
	downloads := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/archive" {
			downloads++
			w.Write(archive)
			return
		}
		fmt.Fprintf(w, `[{"binary":{"package":{"link":%q,"checksum":%q}}}]`, srv.URL+"/archive", checksum)
	}))
	t.Cleanup(srv.Close)

	oldAPI, oldDir := adoptiumAPI, JavaRuntimeDir
	adoptiumAPI, JavaRuntimeDir = srv.URL, t.TempDir()
	t.Cleanup(func() { adoptiumAPI, JavaRuntimeDir = oldAPI, oldDir })
	return &downloads
}

func TestEnsureJavaInstallsOnceAndReuses(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	archive := fakeJavaArchive(t, "#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(archive)
	downloads := serveJava(t, archive, hex.EncodeToString(sum[:]))

	var messages []string
	bin, err := ensureJava(25, func(m string) { messages = append(messages, m) })
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(JavaRuntimeDir, "25", "bin", "java"); bin != want {
		t.Errorf("bin = %s, want %s", bin, want)
	}
	if len(messages) != 2 {
		t.Errorf("progress messages = %v", messages)
	}
	if _, err := ensureJava(25, nil); err != nil {
		t.Fatal(err)
	}
	if *downloads != 1 {
		t.Errorf("downloads = %d, want 1", *downloads)
	}
	assertNoStaging(t)
}

func TestEnsureJavaRejectsBadChecksum(t *testing.T) {
	archive := fakeJavaArchive(t, "#!/bin/sh\nexit 0\n")
	serveJava(t, archive, strings.Repeat("0", 64))

	_, err := ensureJava(25, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	assertNotInstalled(t)
}

func TestEnsureJavaRejectsRuntimeThatWontRun(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	archive := fakeJavaArchive(t, "#!/bin/sh\nexit 1\n")
	sum := sha256.Sum256(archive)
	serveJava(t, archive, hex.EncodeToString(sum[:]))

	_, err := ensureJava(25, nil)
	if err == nil || !strings.Contains(err.Error(), "doesn't run") {
		t.Fatalf("err = %v", err)
	}
	assertNotInstalled(t)
}

func assertNotInstalled(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(JavaRuntimeDir, "25")); !os.IsNotExist(err) {
		t.Errorf("failed install left a runtime behind: %v", err)
	}
	assertNoStaging(t)
}

func assertNoStaging(t *testing.T) {
	t.Helper()
	leftovers, _ := filepath.Glob(filepath.Join(JavaRuntimeDir, ".install-*"))
	if len(leftovers) != 0 {
		t.Errorf("staging left behind: %v", leftovers)
	}
}

// TestEnsureJavaLive downloads a real runtime. Run it inside Alpine, as the Temurin
// builds it fetches are musl-only: RSC_JAVA_LIVE=25 go test -run EnsureJavaLive
func TestEnsureJavaLive(t *testing.T) {
	major := os.Getenv("RSC_JAVA_LIVE")
	if major == "" {
		t.Skip("set RSC_JAVA_LIVE to a Java major version")
	}
	var n int
	fmt.Sscan(major, &n)

	oldDir := JavaRuntimeDir
	JavaRuntimeDir = t.TempDir()
	t.Cleanup(func() { JavaRuntimeDir = oldDir })

	bin, err := ensureJava(n, func(m string) { t.Log(m) })
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-version").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", out)
	if !strings.Contains(string(out), fmt.Sprintf(`version "%s`, major)) {
		t.Errorf("unexpected version output: %s", out)
	}
}
