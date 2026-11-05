package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sterango/redstonecore-agent/internal/api"
	"github.com/sterango/redstonecore-agent/internal/minecraft"
)

const (
	// modTrashDir sits beside mods/, not in it, so the loader never sees trashed jars.
	modTrashDir = ".mod-trash"
	modTrashTTL = 7 * 24 * time.Hour
)

// trashStamp reads the stamp the panel chose for a removal. The panel names the trashed
// file from it, so both sides agree on the name without a round trip. 0 means delete.
func trashStamp(cmd api.Command) int64 {
	stamp, _ := cmd.Payload["trash_stamp"].(float64)
	return int64(stamp)
}

// removeMod takes a jar out of mods/, keeping it for undo when given a stamp. A jar
// that's already gone isn't an error.
func removeMod(serverDir, filename string, stamp int64) error {
	path := filepath.Join(serverDir, "mods", filename)
	if stamp == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	trash := filepath.Join(serverDir, modTrashDir)
	if err := os.MkdirAll(trash, 0775); err != nil {
		return err
	}
	dest := filepath.Join(trash, strconv.FormatInt(stamp, 10)+"-"+filename)
	if err := os.Rename(path, dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// restoreMod moves a trashed jar back into mods/. It won't overwrite a jar of the same
// name, which would throw away whatever replaced it.
func (a *Agent) restoreMod(cmd api.Command, server *minecraft.Server) error {
	if cmd.Payload == nil {
		return fmt.Errorf("restore_mod requires payload")
	}
	filename, err := modFilename(fmt.Sprint(cmd.Payload["filename"]))
	if err != nil {
		return err
	}
	stamp := trashStamp(cmd)
	if stamp == 0 {
		return fmt.Errorf("restore_mod requires trash_stamp")
	}

	src := filepath.Join(server.DataDir, modTrashDir, strconv.FormatInt(stamp, 10)+"-"+filename)
	dest := filepath.Join(server.DataDir, "mods", filename)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("%s is no longer available to restore", filename)
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists in mods", filename)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0775); err != nil {
		return err
	}
	if err := os.Rename(src, dest); err != nil {
		return fmt.Errorf("failed to restore mod: %w", err)
	}
	log.Printf("Mod %s restored on server %s", filename, server.Name)
	return nil
}

// purgeModTrash deletes jars past their undo window, going by the stamp in the name
// because a rename keeps the jar's original modification time.
func purgeModTrash(serverDir string, now time.Time) {
	trash := filepath.Join(serverDir, modTrashDir)
	entries, err := os.ReadDir(trash)
	if err != nil {
		return
	}
	cutoff := now.Add(-modTrashTTL).Unix()
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "-")
		if !ok {
			continue
		}
		stamp, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || stamp >= cutoff {
			continue
		}
		if err := os.Remove(filepath.Join(trash, e.Name())); err != nil {
			log.Printf("Warning: failed to purge %s: %v", e.Name(), err)
		}
	}
}

func (a *Agent) purgeModTrashLoop() {
	for {
		a.serversMu.RLock()
		dirs := make([]string, 0, len(a.servers))
		for _, s := range a.servers {
			dirs = append(dirs, s.DataDir)
		}
		a.serversMu.RUnlock()

		now := time.Now()
		for _, dir := range dirs {
			purgeModTrash(dir, now)
		}
		time.Sleep(6 * time.Hour)
	}
}
