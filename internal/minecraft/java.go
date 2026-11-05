package minecraft

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultJavaMajor is the runtime on PATH in the agent image.
const defaultJavaMajor = 21

// JavaRuntimeDir holds Java runtimes installed on demand, one per major version. It
// must sit on the data volume so they survive agent updates; the agent sets it at boot.
var JavaRuntimeDir = "/data/java"

var (
	adoptiumAPI     = "https://api.adoptium.net/v3"
	javaInstallMu   sync.Mutex
	javaDownloadTTL = 15 * time.Minute
)

// scriptJavaCheck matches the minimum-version test in a pack's launch script, such as
// `if [ ! "$JAVA_VERSION" -ge 25 ]`.
var scriptJavaCheck = regexp.MustCompile(`JAVA_VERSION\}?"?\s+-(?:ge|lt)\s+"?(\d+)`)

// mcJavaMajor returns the Java major a Minecraft version is built for, or 0 if the
// version can't be parsed. Releases from 26.1 on use year-based version numbers.
func mcJavaMajor(mcVersion string) int {
	parts := strings.Split(strings.TrimSpace(mcVersion), ".")
	if len(parts) < 2 {
		return 0
	}
	first, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	if first >= 26 {
		return 25
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	patch := 0
	if len(parts) >= 3 {
		patch, _ = strconv.Atoi(parts[2])
	}
	switch {
	case minor < 17:
		return 8
	case minor < 20 || (minor == 20 && patch <= 4):
		return 17
	default:
		return 21
	}
}

// jarJavaMajor picks the runtime for a server the agent launches with `java -jar`.
// Proxies don't run the game, so their Minecraft version says nothing about Java.
func jarJavaMajor(serverType ServerType, mcVersion string) int {
	if serverType == TypeBungeeCord || serverType == TypeVelocity {
		return 0
	}
	return mcJavaMajor(mcVersion)
}

// scriptJavaMajor returns the Java a launch script needs beyond the image default, or
// 0 if the default will do. It only ever raises the version: scripts run whatever
// `java` is on PATH, and working packs such as Forge 1.20.1 already run on the default
// even though their Minecraft version would suggest 17.
func scriptJavaMajor(scriptPath, mcVersion string) int {
	required := mcJavaMajor(mcVersion)
	if data, err := os.ReadFile(scriptPath); err == nil {
		for _, m := range scriptJavaCheck.FindAllSubmatch(data, -1) {
			if declared, err := strconv.Atoi(string(m[1])); err == nil && declared > required {
				required = declared
			}
		}
	}
	if required <= defaultJavaMajor {
		return 0
	}
	return required
}

// javaBinForMajor returns a Java binary of the given major version, installing it first
// if the image doesn't carry one. A major of 0 means the image default.
func javaBinForMajor(major int, progress func(string)) (string, error) {
	switch major {
	case 0, defaultJavaMajor:
		return "java", nil
	case 17:
		if _, err := os.Stat(java17Bin); err == nil {
			return java17Bin, nil
		}
	}
	return ensureJava(major, progress)
}

// javaEnv returns the environment for a launch script, with javaBin first on PATH so
// the script's plain `java` calls resolve to it.
func javaEnv(javaBin string) []string {
	binDir := filepath.Dir(javaBin)
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "JAVA_HOME=") {
			env = append(env, kv)
		}
	}
	return append(env,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"JAVA_HOME="+filepath.Dir(binDir),
	)
}

// ensureJava returns the binary for an installed runtime, downloading it if needed.
// Temurin's Alpine builds are the ones that run in the agent's musl-based image.
func ensureJava(major int, progress func(string)) (string, error) {
	javaInstallMu.Lock()
	defer javaInstallMu.Unlock()

	home := filepath.Join(JavaRuntimeDir, strconv.Itoa(major))
	bin := filepath.Join(home, "bin", "java")
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}

	if progress != nil {
		progress(fmt.Sprintf("Installing Java %d, this only happens once...", major))
	}
	if err := installJava(major, home); err != nil {
		return "", fmt.Errorf("could not install Java %d: %w", major, err)
	}
	if progress != nil {
		progress(fmt.Sprintf("Java %d installed", major))
	}
	return bin, nil
}

// installJava downloads, verifies and unpacks a runtime in a staging directory, then
// moves it into place only once it runs, so a failed install leaves nothing behind.
func installJava(major int, home string) error {
	link, checksum, err := findJavaPackage(major)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(JavaRuntimeDir, 0755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(JavaRuntimeDir, ".install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	archive := filepath.Join(staging, "java.tar.gz")
	if err := downloadVerified(link, checksum, archive); err != nil {
		return err
	}

	unpacked := filepath.Join(staging, "jre")
	if err := os.Mkdir(unpacked, 0755); err != nil {
		return err
	}
	if out, err := exec.Command("tar", "-xzf", archive, "-C", unpacked, "--strip-components=1").CombinedOutput(); err != nil {
		return fmt.Errorf("unpack failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(filepath.Join(unpacked, "bin", "java"), "-version").CombinedOutput(); err != nil {
		return fmt.Errorf("downloaded runtime doesn't run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return os.Rename(unpacked, home)
}

// findJavaPackage asks Adoptium for the latest JRE of a major version for this machine.
func findJavaPackage(major int) (link, checksum string, err error) {
	arch := map[string]string{"amd64": "x64", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" {
		return "", "", fmt.Errorf("no Java builds for architecture %s", runtime.GOARCH)
	}

	query := url.Values{
		"architecture": {arch},
		"image_type":   {"jre"},
		"os":           {"alpine-linux"},
		"vendor":       {"eclipse"},
	}
	endpoint := fmt.Sprintf("%s/assets/latest/%d/hotspot?%s", adoptiumAPI, major, query.Encode())

	client := http.Client{Timeout: time.Minute}
	resp, err := client.Get(endpoint)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("Adoptium returned %s", resp.Status)
	}

	var assets []struct {
		Binary struct {
			Package struct {
				Link     string `json:"link"`
				Checksum string `json:"checksum"`
			} `json:"package"`
		} `json:"binary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
		return "", "", fmt.Errorf("unreadable Adoptium response: %w", err)
	}
	if len(assets) == 0 || assets[0].Binary.Package.Link == "" || assets[0].Binary.Package.Checksum == "" {
		return "", "", fmt.Errorf("no Java %d build for alpine-linux/%s", major, arch)
	}
	return assets[0].Binary.Package.Link, assets[0].Binary.Package.Checksum, nil
}

// downloadVerified saves a URL to dest and fails unless it matches the SHA-256 checksum.
func downloadVerified(link, checksum, dest string) error {
	client := http.Client{Timeout: javaDownloadTTL}
	resp, err := client.Get(link)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, hash), resp.Body); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, checksum) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, checksum)
	}
	return nil
}
