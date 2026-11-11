// Package hoststats samples the machine's CPU and memory use for the dashboard's live
// instance stats. Linux only: it reads /proc, which inside the agent's container still
// describes the whole host. Anywhere /proc isn't readable, Sample returns nil and the
// heartbeat simply leaves the stats out.
package hoststats

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Stats is what the heartbeat reports.
type Stats struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsedMB  int64   `json:"ram_used_mb"`
	MemTotalMB int64   `json:"ram_total_mb"`
}

// Sampler turns /proc/stat's cumulative counters into a usage figure between calls.
type Sampler struct {
	mu        sync.Mutex
	lastIdle  uint64
	lastTotal uint64
}

// Sample returns current usage, or nil when it can't be read. CPU is the share of time not
// idle since the previous call; the first call has no previous one and reports 0.
func (s *Sampler) Sample() *Stats {
	used, total, ok := memory()
	if !ok {
		return nil
	}
	stats := &Stats{MemUsedMB: used / 1024, MemTotalMB: total / 1024}

	idle, all, ok := cpuTimes()
	if ok {
		s.mu.Lock()
		if s.lastTotal > 0 && all > s.lastTotal {
			dTotal := float64(all - s.lastTotal)
			dIdle := float64(idle - s.lastIdle)
			pct := (1 - dIdle/dTotal) * 100
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			stats.CPUPercent = float64(int(pct*10+0.5)) / 10
		}
		s.lastIdle, s.lastTotal = idle, all
		s.mu.Unlock()
	}
	return stats
}

// cpuTimes reads the aggregate "cpu" line: idle (idle + iowait) and the total of all fields.
func cpuTimes() (idle, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		for i, v := range fields[1:] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			// guest and guest_nice (fields 9 and 10) are already counted in user and nice.
			if i >= 8 {
				break
			}
			total += n
			if i == 3 || i == 4 {
				idle += n
			}
		}
		return idle, total, true
	}
	return 0, 0, false
}

// memory returns used and total in KiB, where used is MemTotal - MemAvailable (what the
// kernel can't hand out without swapping; page cache doesn't count as used).
func memory() (used, total int64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	var available int64 = -1
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = n
		case "MemAvailable:":
			available = n
		}
	}
	if total <= 0 || available < 0 {
		return 0, 0, false
	}
	return total - available, total, true
}
