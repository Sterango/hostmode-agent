package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeJar(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("jar"), 0644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRemoveModKeepsJarWhenStamped(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "mods", "a.jar")
	writeJar(t, jar)

	if err := removeMod(dir, "a.jar", 1700000000); err != nil {
		t.Fatal(err)
	}
	if exists(jar) {
		t.Error("jar still in mods/")
	}
	if !exists(filepath.Join(dir, modTrashDir, "1700000000-a.jar")) {
		t.Error("jar not in trash")
	}
	if err := removeMod(dir, "gone.jar", 1700000000); err != nil {
		t.Errorf("missing jar should not error: %v", err)
	}
}

func TestRemoveModDeletesWithoutStamp(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "mods", "a.jar")
	writeJar(t, jar)

	if err := removeMod(dir, "a.jar", 0); err != nil {
		t.Fatal(err)
	}
	if exists(jar) || exists(filepath.Join(dir, modTrashDir)) {
		t.Error("jar should be deleted outright")
	}
}

func TestPurgeModTrashOnlyDropsExpired(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1700000000, 0)
	old := now.Add(-modTrashTTL - time.Hour).Unix()
	fresh := now.Add(-time.Hour).Unix()

	trash := filepath.Join(dir, modTrashDir)
	oldJar := filepath.Join(trash, itoa(old)+"-old.jar")
	freshJar := filepath.Join(trash, itoa(fresh)+"-fresh.jar")
	writeJar(t, oldJar)
	writeJar(t, freshJar)

	purgeModTrash(dir, now)

	if exists(oldJar) {
		t.Error("expired jar not purged")
	}
	if !exists(freshJar) {
		t.Error("fresh jar was purged")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
