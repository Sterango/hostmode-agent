package moddata

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loaderLibraryDirs is where each loader's installer puts its versioned library folder.
var loaderLibraryDirs = map[string]string{
	"neoforge": "libraries/net/neoforged/neoforge",
	"forge":    "libraries/net/minecraftforge/forge",
	"fabric":   "libraries/net/fabricmc/fabric-loader",
	"quilt":    "libraries/org/quiltmc/quilt-loader",
}

// loaderDepIDs are the dependency ids that mean "this loader" in mod metadata.
var loaderDepIDs = map[string]map[string]bool{
	"neoforge": {"neoforge": true},
	"forge":    {"forge": true},
	"fabric":   {"fabricloader": true, "fabric-loader": true},
	"quilt":    {"quilt_loader": true},
}

// installedLoaderVersion returns the loader version in a server's libraries folder, or
// "" if it can't be told. Upgrades leave old versions behind, so the highest wins: that
// can miss a problem but can't invent one, which matters more for a warning people read.
func installedLoaderVersion(serverDir, loader string) string {
	rel, ok := loaderLibraryDirs[loader]
	if !ok {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(serverDir, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		version := e.Name()
		if loader == "forge" {
			// Forge folders are named <minecraft>-<forge>, such as 1.20.1-47.4.10.
			if i := strings.LastIndex(version, "-"); i >= 0 {
				version = version[i+1:]
			}
		}
		if best == "" || compareVersions(version, best) > 0 {
			best = version
		}
	}
	return best
}

// analyzeLoader reports mods that need a different version of the loader than the one
// installed. The loader refuses to boot when this happens.
func analyzeLoader(set []JarInfo, serverLoader, loaderVersion string) []Issue {
	depIDs := loaderDepIDs[serverLoader]
	if loaderVersion == "" || depIDs == nil {
		return nil
	}
	maven := serverLoader == "forge" || serverLoader == "neoforge"

	var issues []Issue
	for i := range set {
		j := &set[i]
		for _, m := range j.Mods {
			if m.Nested || m.Loader != serverLoader {
				continue
			}
			for _, d := range m.Depends {
				if d.Kind != "required" || d.Side == "CLIENT" || !depIDs[d.ID] {
					continue
				}
				if versionSatisfies(loaderVersion, d.Range, maven) {
					continue
				}
				name := m.Name
				if name == "" {
					name = m.ID
				}
				issues = append(issues, Issue{
					Severity: "error", Code: "loader_version", Files: []string{j.Filename}, ModID: m.ID, New: j.Candidate,
					Message: fmt.Sprintf("%s needs %s %s, but this server has %s.",
						name, loaderLabel(serverLoader), describeRange(d.Range), loaderVersion),
				})
			}
		}
	}
	return issues
}

// describeRange turns the common open-ended Maven range into words.
func describeRange(ranges []string) string {
	if len(ranges) == 1 {
		r := strings.TrimSpace(ranges[0])
		if strings.HasPrefix(r, "[") && strings.HasSuffix(r, ",)") {
			return strings.TrimSuffix(strings.TrimPrefix(r, "["), ",)") + " or newer"
		}
	}
	return strings.Join(ranges, " or ")
}
