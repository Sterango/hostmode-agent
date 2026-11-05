package moddata

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// JarInfo describes one jar in mods/ (or a candidate about to be installed).
type JarInfo struct {
	Key         string    `json:"key,omitempty"` // caller-supplied id for candidates
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	SHA1        string    `json:"sha1"`
	Fingerprint uint32    `json:"fingerprint"` // CurseForge murmur2
	Loader      string    `json:"loader"`      // loader whose metadata applies on this server, "" if not a mod
	Loaders     []string  `json:"loaders"`     // every loader the jar declares metadata for
	Library     bool      `json:"library,omitempty"`
	Mods        []ModMeta `json:"mods"`
	ConfigFiles []string  `json:"config_files,omitempty"`
	Candidate   bool      `json:"candidate,omitempty"`
}

// Issue is one compatibility problem found in the (prospective) mod set.
type Issue struct {
	Severity  string   `json:"severity"` // error | warning
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Files     []string `json:"files"`                // top-level jars involved
	ModID     string   `json:"mod_id,omitempty"`     // the mod raising the issue
	MissingID string   `json:"missing_id,omitempty"` // for missing_dependency
	New       bool     `json:"new"`                  // involves a candidate
}

var (
	scanMu    sync.Mutex
	scanLocks = map[string]*sync.Mutex{}
)

func serverLock(uuid string) *sync.Mutex {
	scanMu.Lock()
	defer scanMu.Unlock()
	if scanLocks[uuid] == nil {
		scanLocks[uuid] = &sync.Mutex{}
	}
	return scanLocks[uuid]
}

func scanCachePath(dataDir, serverUUID string) string {
	return filepath.Join(dataDir, "cache", "mod-scan", safeKey(serverUUID)+".json")
}

func downloadCacheDir(dataDir string) string {
	return filepath.Join(dataDir, "cache", "mod-downloads")
}

// HandleModScan inventories mods/ and, when candidates are given, downloads
// them and checks the resulting mod set for problems.
//
// params:
//
//	mc_version  string   server Minecraft version (for minecraft dep ranges)
//	loader      string   server loader: forge | neoforge | fabric | quilt
//	candidates  []{key, url, filename, sha1}
//	replace     []string filenames in mods/ that the candidates replace
func HandleModScan(serverDir, serverUUID, dataDir string, params map[string]interface{}) map[string]interface{} {
	lock := serverLock(serverUUID)
	lock.Lock()
	defer lock.Unlock()

	mcVersion, _ := params["mc_version"].(string)
	loader, _ := params["loader"].(string)

	installed, err := scanInstalled(serverDir, serverUUID, dataDir)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	replace := map[string]bool{}
	if list, ok := params["replace"].([]interface{}); ok {
		for _, r := range list {
			if s, ok := r.(string); ok {
				replace[s] = true
			}
		}
	}

	var candidates []JarInfo
	var candErrors []Issue
	if list, ok := params["candidates"].([]interface{}); ok {
		for _, c := range list {
			cm, _ := c.(map[string]interface{})
			key, _ := cm["key"].(string)
			u, _ := cm["url"].(string)
			fn, _ := cm["filename"].(string)
			want, _ := cm["sha1"].(string)
			ji, err := inspectCandidate(dataDir, u, fn, want)
			if err != nil {
				candErrors = append(candErrors, Issue{
					Severity: "error", Code: "download_failed", New: true, Files: []string{fn},
					Message: fmt.Sprintf("Couldn't download %s to check it: %v", fn, err),
				})
				continue
			}
			ji.Key = key
			ji.Candidate = true
			candidates = append(candidates, ji)
		}
	}

	for i := range installed {
		installed[i] = selectForLoader(installed[i], loader, mcVersion)
	}
	for i := range candidates {
		candidates[i] = selectForLoader(candidates[i], loader, mcVersion)
	}

	set := make([]JarInfo, 0, len(installed)+len(candidates))
	for _, j := range installed {
		if !replace[j.Filename] {
			set = append(set, j)
		}
	}
	set = append(set, candidates...)

	loaderVersion := installedLoaderVersion(serverDir, loader)
	issues := append(candErrors, analyze(set, mcVersion, loader)...)
	issues = append(issues, analyzeLoader(set, loader, loaderVersion)...)
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].New != issues[j].New {
			return issues[i].New
		}
		return issues[i].Severity == "error" && issues[j].Severity != "error"
	})

	return map[string]interface{}{
		"installed":      installed,
		"candidates":     candidates,
		"issues":         issues,
		"loader_version": loaderVersion,
	}
}

// ---------------------------------------------------------------------------
// Inventory

type scanCacheEntry struct {
	Stamp string  `json:"stamp"`
	Info  JarInfo `json:"info"`
}

func scanInstalled(serverDir, serverUUID, dataDir string) ([]JarInfo, error) {
	modsDir := filepath.Join(serverDir, "mods")
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []JarInfo{}, nil
		}
		return nil, err
	}

	cachePath := scanCachePath(dataDir, serverUUID)
	cache := map[string]scanCacheEntry{}
	if data, err := os.ReadFile(cachePath); err == nil {
		_ = json.Unmarshal(data, &cache)
	}

	configFiles := listConfigFiles(serverDir)

	out := []JarInfo{}
	fresh := map[string]scanCacheEntry{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		stamp := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
		var ji JarInfo
		if c, ok := cache[e.Name()]; ok && c.Stamp == stamp {
			ji = c.Info
		} else {
			ji, err = inspectJar(filepath.Join(modsDir, e.Name()))
			if err != nil {
				continue
			}
		}
		ji.Filename = e.Name()
		fresh[e.Name()] = scanCacheEntry{Stamp: stamp, Info: ji}
		ji.ConfigFiles = matchConfigFiles(ji, configFiles)
		out = append(out, ji)
	}

	if data, err := json.Marshal(fresh); err == nil {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0755)
		_ = os.WriteFile(cachePath, data, 0644)
	}
	return out, nil
}

// inspectJar hashes and parses one jar file.
func inspectJar(path string) (JarInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return JarInfo{}, err
	}
	sum := sha1.Sum(data)
	ji := JarInfo{
		Filename:    filepath.Base(path),
		Size:        int64(len(data)),
		SHA1:        hex.EncodeToString(sum[:]),
		Fingerprint: curseForgeFingerprint(data),
		Mods:        []ModMeta{},
	}
	if zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
		ji.Mods = readJarMeta(zr, 0)
		if len(ji.Mods) == 0 {
			ji.Library = isLibraryJar(zr)
		}
	}
	seen := map[string]bool{}
	for _, m := range ji.Mods {
		if !m.Nested && !seen[m.Loader] {
			seen[m.Loader] = true
			ji.Loaders = append(ji.Loaders, m.Loader)
		}
	}
	return ji, nil
}

// ---------------------------------------------------------------------------
// Candidates

var allowedDownloadHosts = []string{"cdn.modrinth.com", "forgecdn.net", "curseforge.com"}

func inspectCandidate(dataDir, rawURL, filename, wantSHA1 string) (JarInfo, error) {
	if filename == "" || filename != filepath.Base(filename) || !strings.HasSuffix(strings.ToLower(filename), ".jar") {
		return JarInfo{}, fmt.Errorf("invalid filename")
	}
	wantSHA1 = strings.ToLower(wantSHA1)
	dir := downloadCacheDir(dataDir)
	if wantSHA1 != "" {
		cached := filepath.Join(dir, wantSHA1+".jar")
		if _, err := os.Stat(cached); err == nil {
			ji, err := inspectJar(cached)
			if err == nil && ji.SHA1 == wantSHA1 {
				ji.Filename = filename
				return ji, nil
			}
		}
	}

	tmp, err := DownloadModJar(rawURL, dir)
	if err != nil {
		return JarInfo{}, err
	}
	ji, err := inspectJar(tmp)
	if err != nil {
		os.Remove(tmp)
		return JarInfo{}, err
	}
	if wantSHA1 != "" && ji.SHA1 != wantSHA1 {
		os.Remove(tmp)
		return JarInfo{}, fmt.Errorf("checksum mismatch")
	}
	_ = os.Rename(tmp, filepath.Join(dir, ji.SHA1+".jar"))
	pruneDownloadCache(dir)
	ji.Filename = filename
	return ji, nil
}

// CachedModJar returns the path of a previously downloaded jar with this
// sha1, if present — lets install_mod skip a second download after a check.
func CachedModJar(dataDir, sha1hex string) string {
	if sha1hex == "" {
		return ""
	}
	p := filepath.Join(downloadCacheDir(dataDir), strings.ToLower(sha1hex)+".jar")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// DownloadModJar fetches a mod jar from an allow-listed CDN into dir and
// returns the temp file path. The caller verifies and renames it.
func DownloadModJar(rawURL, dir string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("invalid download url")
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, h := range allowedDownloadHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("download host %s is not allowed", host)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	req, _ := http.NewRequest("GET", rawURL, nil)
	req.Header.Set("User-Agent", "RedstoneCore-Agent/1.0 (https://redstonecore.net)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.CreateTemp(dir, "dl-*.part")
	if err != nil {
		return "", err
	}
	const maxSize = 512 << 20
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxSize+1))
	f.Close()
	if err != nil || n > maxSize {
		os.Remove(f.Name())
		if err == nil {
			err = fmt.Errorf("file too large")
		}
		return "", err
	}
	return f.Name(), nil
}

// pruneDownloadCache keeps the download cache bounded (oldest first, >1GB or >7d).
func pruneDownloadCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fi struct {
		path string
		size int64
		mod  time.Time
	}
	var files []fi
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		files = append(files, fi{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= 1<<30 && time.Since(f.mod) < 7*24*time.Hour {
			break
		}
		os.Remove(f.path)
		total -= f.size
	}
}

// ---------------------------------------------------------------------------
// Analysis

// Ids supplied by the loader/game itself rather than a mod jar.
var builtinIDs = map[string]bool{
	"minecraft": true, "java": true, "fabricloader": true, "fabric-loader": true,
	"quilt_loader": true, "forge": true, "neoforge": true, "javafml": true, "lowcodefml": true,
	"mixinextras": true, // bundled by Fabric Loader 0.15+ and Forge/NeoForge 1.20+
}

type provider struct {
	jar     *JarInfo
	version string
	nested  bool
}

func analyze(set []JarInfo, mcVersion, serverLoader string) []Issue {
	var issues []Issue
	providers := map[string][]provider{}

	for i := range set {
		j := &set[i]
		for _, m := range j.Mods {
			providers[m.ID] = append(providers[m.ID], provider{j, m.Version, m.Nested})
			for _, p := range m.Provides {
				providers[p] = append(providers[p], provider{j, m.Version, m.Nested})
			}
		}
	}

	label := func(j *JarInfo) string {
		for _, m := range j.Mods {
			if !m.Nested && m.Name != "" {
				return m.Name
			}
		}
		return j.Filename
	}

	for i := range set {
		j := &set[i]

		if len(j.Mods) == 0 {
			if j.Library || !j.Candidate {
				continue // loader libraries / unrecognised jars that already load fine
			}
			issues = append(issues, Issue{
				Severity: "warning", Code: "not_a_mod", Files: []string{j.Filename}, New: j.Candidate,
				Message: fmt.Sprintf("%s has no mod metadata — it may be a library, a client-only file, or for a different loader.", j.Filename),
			})
			continue
		}

		if msg, sev := loaderMismatch(j.Loader, serverLoader, mcVersion); msg != "" {
			issues = append(issues, Issue{
				Severity: sev, Code: "wrong_loader", Files: []string{j.Filename}, New: j.Candidate,
				Message: fmt.Sprintf("%s is a %s mod, but this is a %s server. %s", label(j), loaderLabel(j.Loader), loaderLabel(serverLoader), msg),
			})
			continue // its dependency list is meaningless on this loader
		}

		for _, m := range j.Mods {
			if m.Nested {
				continue // bundled libraries' deps are the parent's responsibility
			}
			name := m.Name
			if name == "" {
				name = m.ID
			}

			if m.Environment == "client" {
				issues = append(issues, Issue{
					Severity: "warning", Code: "client_only", Files: []string{j.Filename}, ModID: m.ID, New: j.Candidate,
					Message: fmt.Sprintf("%s is client-only — the server will skip it. Players install it on their own game instead.", name),
				})
			}

			for _, d := range m.Depends {
				if d.Kind != "required" || d.Side == "CLIENT" {
					continue
				}
				maven := m.Loader == "forge" || m.Loader == "neoforge"
				if d.ID == "minecraft" {
					if mcVersion != "" && !minecraftCompatible(mcVersion, d.Range, maven) {
						issues = append(issues, Issue{
							Severity: "error", Code: "wrong_minecraft", Files: []string{j.Filename}, ModID: m.ID, New: j.Candidate,
							Message: fmt.Sprintf("%s needs Minecraft %s, but the server runs %s.", name, strings.Join(d.Range, " or "), mcVersion),
						})
					}
					continue
				}
				if builtinIDs[d.ID] {
					continue
				}
				provs := providers[d.ID]
				if len(provs) == 0 {
					issues = append(issues, Issue{
						Severity: "error", Code: "missing_dependency", Files: []string{j.Filename}, ModID: m.ID, MissingID: d.ID, New: j.Candidate,
						Message: fmt.Sprintf("%s requires %s, which isn't installed.", name, d.ID),
					})
					continue
				}
				ok := false
				for _, p := range provs {
					if versionSatisfies(p.version, d.Range, maven) {
						ok = true
						break
					}
				}
				if !ok {
					p := provs[0]
					issues = append(issues, Issue{
						Severity: "error", Code: "dependency_version", Files: []string{j.Filename, p.jar.Filename}, ModID: m.ID, MissingID: d.ID,
						New:     j.Candidate || p.jar.Candidate,
						Message: fmt.Sprintf("%s needs %s %s, but %s is installed.", name, d.ID, strings.Join(d.Range, " or "), p.version),
					})
				}
			}

			for _, b := range m.Breaks {
				maven := m.Loader == "forge" || m.Loader == "neoforge"
				for _, p := range providers[b.ID] {
					if p.jar == j || !versionMatchesStrict(p.version, b.Range, maven) {
						continue
					}
					sev, verb := "error", "is incompatible with"
					if b.Kind == "conflicts" {
						sev, verb = "warning", "may conflict with"
					}
					issues = append(issues, Issue{
						Severity: sev, Code: "incompatible", Files: []string{j.Filename, p.jar.Filename}, ModID: m.ID,
						New:     j.Candidate || p.jar.Candidate,
						Message: fmt.Sprintf("%s %s %s (%s).", name, verb, label(p.jar), b.ID),
					})
					break
				}
			}
		}
	}

	// The same mod id in two top-level jars = two versions installed side by side.
	for id, provs := range providers {
		var top []provider
		for _, p := range provs {
			if !p.nested {
				top = append(top, p)
			}
		}
		if len(top) < 2 || builtinIDs[id] {
			continue
		}
		seen := map[string]bool{}
		var files []string
		isNew := false
		for _, p := range top {
			if !seen[p.jar.Filename] {
				seen[p.jar.Filename] = true
				files = append(files, p.jar.Filename)
				isNew = isNew || p.jar.Candidate
			}
		}
		if len(files) < 2 {
			continue
		}
		issues = append(issues, Issue{
			Severity: "error", Code: "duplicate", Files: files, ModID: id, New: isNew,
			Message: fmt.Sprintf("%s is installed more than once (%s). Remove all but one.", id, strings.Join(files, ", ")),
		})
	}

	return issues
}

// selectForLoader narrows a multi-loader jar to the metadata the server's
// loader will actually read. Jars with no usable metadata are left as-is so the
// analysis can report them as the wrong loader.
func selectForLoader(j JarInfo, serverLoader, mcVersion string) JarInfo {
	if serverLoader == "" || len(j.Loaders) == 0 {
		if len(j.Loaders) > 0 {
			j.Loader = j.Loaders[0]
		}
		return j
	}
	runs := func(l string) bool {
		msg, sev := loaderMismatch(l, serverLoader, mcVersion)
		return msg == "" || sev == "warning"
	}

	chosen := ""
	for _, l := range j.Loaders {
		if l == serverLoader {
			chosen = l
		}
	}
	if chosen == "" {
		for _, l := range j.Loaders {
			if runs(l) {
				chosen = l
				break
			}
		}
	}
	if chosen == "" {
		j.Loader = j.Loaders[0]
		return j
	}

	j.Loader = chosen
	var mods []ModMeta
	for _, m := range j.Mods {
		if (!m.Nested && m.Loader == chosen) || (m.Nested && runs(m.Loader)) {
			mods = append(mods, m)
		}
	}
	j.Mods = mods
	return j
}

// minecraftCompatible checks a mod's minecraft dependency. Mod templates
// commonly declare ranges like [1.21,1.21.1) that loaders accept on 1.21.1 in
// practice, so a mismatch only counts when the range misses the server's whole
// release line (e.g. a 1.20.x mod on a 1.21.1 server).
func minecraftCompatible(mc string, ranges []string, maven bool) bool {
	if versionSatisfies(mc, ranges, maven) {
		return true
	}
	segs := splitSegs(stripPre(mc))
	if len(segs) < 2 {
		return false
	}
	line := segs[0] + "." + segs[1]
	if versionSatisfies(line, ranges, maven) {
		return true
	}
	for patch := 0; patch <= 12; patch++ {
		if versionSatisfies(fmt.Sprintf("%s.%d", line, patch), ranges, maven) {
			return true
		}
	}
	return false
}

func loaderLabel(l string) string {
	switch l {
	case "neoforge":
		return "NeoForge"
	case "":
		return "unknown"
	}
	return strings.ToUpper(l[:1]) + l[1:]
}

// loaderMismatch returns ("", "") when a mod for modLoader runs on serverLoader.
func loaderMismatch(modLoader, serverLoader, mcVersion string) (string, string) {
	if modLoader == "" || serverLoader == "" || modLoader == serverLoader {
		return "", ""
	}
	switch {
	case serverLoader == "quilt" && modLoader == "fabric":
		return "", "" // Quilt loads Fabric mods
	case serverLoader == "neoforge" && modLoader == "forge":
		if strings.HasPrefix(mcVersion, "1.20.1") {
			return "NeoForge 1.20.1 can usually load Forge mods, but it isn't guaranteed.", "warning"
		}
		return "Forge mods don't load on NeoForge for this Minecraft version.", "error"
	}
	return "It won't load.", "error"
}

// ---------------------------------------------------------------------------
// Config file matching (for the post-install config step)

func listConfigFiles(serverDir string) []string {
	var out []string
	for _, base := range []string{"config", "defaultconfigs"} {
		root := filepath.Join(serverDir, base)
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(serverDir, p)
			if info.IsDir() {
				if strings.Count(rel, string(filepath.Separator)) > 2 {
					return filepath.SkipDir
				}
				return nil
			}
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
	}
	return out
}

func matchConfigFiles(j JarInfo, files []string) []string {
	var ids []string
	for _, m := range j.Mods {
		if !m.Nested && len(m.ID) >= 3 {
			ids = append(ids, strings.ToLower(m.ID))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var out []string
	for _, f := range files {
		lf := strings.ToLower(f)
		parts := strings.Split(lf, "/")
		base := parts[len(parts)-1]
		for _, id := range ids {
			if strings.HasPrefix(base, id+".") || strings.HasPrefix(base, id+"-") || strings.HasPrefix(base, id+"_") ||
				(len(parts) > 2 && parts[1] == id) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}
