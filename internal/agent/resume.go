package agent

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sterango/hostmode-agent/internal/minecraft"
)

// resumeStagger spaces out restarts so several modpacks don't all load at once.
const resumeStagger = 5 * time.Second

func (a *Agent) resumeListPath() string {
	return filepath.Join(a.config.DataDir, "resume-servers.json")
}

// saveResumeList records the servers that are up. Minecraft servers are children of the
// agent, so they stop whenever it does and only it can bring them back.
func (a *Agent) saveResumeList() {
	a.serversMu.RLock()
	uuids := make([]string, 0, len(a.servers))
	for uuid, s := range a.servers {
		if s.IsActive() {
			uuids = append(uuids, uuid)
		}
	}
	a.serversMu.RUnlock()
	sort.Strings(uuids)

	data, err := json.Marshal(uuids)
	if err != nil {
		log.Printf("[Resume] Could not encode server list: %v", err)
		return
	}
	if err := os.WriteFile(a.resumeListPath(), data, 0644); err != nil {
		log.Printf("[Resume] Could not save server list: %v", err)
	}
}

// resumeServers starts everything on the saved list. The list is deleted first so it
// applies once: a server that takes the agent down can't restart itself forever.
func (a *Agent) resumeServers() {
	path := a.resumeListPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	os.Remove(path)

	var uuids []string
	if err := json.Unmarshal(data, &uuids); err != nil {
		log.Printf("[Resume] Ignoring unreadable server list: %v", err)
		return
	}

	a.serversMu.RLock()
	servers := make([]*minecraft.Server, 0, len(uuids))
	for _, uuid := range uuids {
		if s, ok := a.servers[uuid]; ok {
			servers = append(servers, s)
		}
	}
	a.serversMu.RUnlock()

	go func() {
		for i, s := range servers {
			if i > 0 {
				time.Sleep(resumeStagger)
			}
			log.Printf("[Resume] Starting %s", s.Name)
			if err := s.Start(); err != nil {
				log.Printf("[Resume] Failed to start %s: %v", s.Name, err)
			}
		}
	}()
}
