package moddata

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// ModMeta is the loader-declared identity and relationships of one mod, read
// from fabric.mod.json, quilt.mod.json or (neoforge.)mods.toml inside a jar.
type ModMeta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Loader      string   `json:"loader"`                // fabric | quilt | forge | neoforge
	Environment string   `json:"environment,omitempty"` // client | server | "" (both)
	Provides    []string `json:"provides,omitempty"`
	Depends     []ModRel `json:"depends,omitempty"`
	Breaks      []ModRel `json:"breaks,omitempty"`
	Nested      bool     `json:"nested,omitempty"` // shipped inside another jar (jar-in-jar)
}

// ModRel is one declared relationship to another mod id. Range is the raw
// version predicate as written (Maven range for Forge, semver predicates for
// Fabric/Quilt); Kind is "required", "optional", "breaks" or "conflicts".
type ModRel struct {
	ID    string   `json:"id"`
	Range []string `json:"range,omitempty"` // OR-ed alternatives
	Kind  string   `json:"kind"`
	Side  string   `json:"side,omitempty"` // CLIENT | SERVER | BOTH (Forge)
}

const maxNestedDepth = 3

// readJarMeta returns every mod declared by a jar, including jar-in-jar
// children (Fabric API ships ~40 modules that way). Multi-loader jars declare
// the same mod once per loader (e.g. fabric.mod.json + neoforge.mods.toml);
// all are returned and the analysis picks the ones for the server's loader.
// Never returns an error: unreadable jars simply yield no mods.
func readJarMeta(zr *zip.Reader, depth int) []ModMeta {
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}

	var mods []ModMeta
	var nestedPaths []string

	if f := files["fabric.mod.json"]; f != nil {
		if m, nested, ok := parseFabricMeta(readZipEntry(f)); ok {
			mods = append(mods, m)
			nestedPaths = append(nestedPaths, nested...)
		}
	}
	if f := files["quilt.mod.json"]; f != nil {
		if m, nested, ok := parseQuiltMeta(readZipEntry(f)); ok {
			mods = append(mods, m)
			nestedPaths = append(nestedPaths, nested...)
		}
	}
	manifestVersion := ""
	if mf := files["META-INF/MANIFEST.MF"]; mf != nil {
		manifestVersion = manifestValue(readZipEntry(mf), "Implementation-Version")
	}
	for _, tomlName := range []string{"META-INF/neoforge.mods.toml", "META-INF/mods.toml"} {
		f := files[tomlName]
		if f == nil {
			continue
		}
		loader := "forge"
		if strings.HasPrefix(tomlName, "META-INF/neoforge") {
			loader = "neoforge"
		}
		mods = append(mods, parseForgeToml(readZipEntry(f), loader, manifestVersion)...)
	}
	// Forge/NeoForge jar-in-jar is listed in META-INF/jarjar/metadata.json.
	if f := files["META-INF/jarjar/metadata.json"]; f != nil {
		var jj struct {
			Jars []struct {
				Path string `json:"path"`
			} `json:"jars"`
		}
		if json.Unmarshal(readZipEntry(f), &jj) == nil {
			for _, j := range jj.Jars {
				nestedPaths = append(nestedPaths, j.Path)
			}
		}
	}

	if depth < maxNestedDepth {
		for _, p := range nestedPaths {
			f := files[strings.TrimPrefix(p, "/")]
			if f == nil {
				continue
			}
			data := readZipEntry(f)
			nzr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				continue
			}
			for _, m := range readJarMeta(nzr, depth+1) {
				m.Nested = true
				mods = append(mods, m)
			}
		}
	}

	return mods
}

// isLibraryJar reports whether a jar without mod metadata is a loader-level
// library or language provider (FMLModType: LIBRARY/LANGPROVIDER/GAMELIBRARY).
func isLibraryJar(zr *zip.Reader) bool {
	for _, f := range zr.File {
		if f.Name == "META-INF/MANIFEST.MF" {
			return manifestValue(readZipEntry(f), "FMLModType") != ""
		}
	}
	return false
}

func readZipEntry(f *zip.File) []byte {
	// Metadata files are tiny; nested jars can be a few MB. Cap at 64MB.
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, _ := io.ReadAll(io.LimitReader(rc, 64<<20))
	return data
}

func manifestValue(manifest []byte, key string) string {
	sc := bufio.NewScanner(bytes.NewReader(manifest))
	prefix := key + ":"
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Fabric / Quilt (JSON)

// jsonRange accepts a fabric dependency value: a string or an array of strings.
func jsonRange(v interface{}) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []interface{}:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func fabricRels(m map[string]interface{}, kind string) []ModRel {
	var rels []ModRel
	for id, v := range m {
		rels = append(rels, ModRel{ID: id, Range: jsonRange(v), Kind: kind})
	}
	return rels
}

// fabric.mod.json permits (non-standard) comments and control chars in strings;
// strip line comments so common hand-written files still parse.
var jsonLineComment = regexp.MustCompile(`(?m)^\s*//.*$`)

func parseFabricMeta(data []byte) (ModMeta, []string, bool) {
	var raw struct {
		ID          string                 `json:"id"`
		Name        string                 `json:"name"`
		Version     string                 `json:"version"`
		Environment string                 `json:"environment"`
		Provides    []string               `json:"provides"`
		Depends     map[string]interface{} `json:"depends"`
		Recommends  map[string]interface{} `json:"recommends"`
		Breaks      map[string]interface{} `json:"breaks"`
		Conflicts   map[string]interface{} `json:"conflicts"`
		Jars        []struct {
			File string `json:"file"`
		} `json:"jars"`
	}
	data = jsonLineComment.ReplaceAll(data, nil)
	if err := json.Unmarshal(data, &raw); err != nil || raw.ID == "" {
		return ModMeta{}, nil, false
	}
	env := raw.Environment
	if env == "*" {
		env = ""
	}
	m := ModMeta{
		ID: raw.ID, Name: raw.Name, Version: raw.Version, Loader: "fabric",
		Environment: env, Provides: raw.Provides,
		Depends: append(fabricRels(raw.Depends, "required"), fabricRels(raw.Recommends, "optional")...),
		Breaks:  append(fabricRels(raw.Breaks, "breaks"), fabricRels(raw.Conflicts, "conflicts")...),
	}
	var nested []string
	for _, j := range raw.Jars {
		nested = append(nested, j.File)
	}
	return m, nested, true
}

func parseQuiltMeta(data []byte) (ModMeta, []string, bool) {
	var raw struct {
		QuiltLoader struct {
			ID       string `json:"id"`
			Version  string `json:"version"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Provides []interface{} `json:"provides"`
			Depends  []interface{} `json:"depends"`
			Breaks   []interface{} `json:"breaks"`
			Jars     []string      `json:"jars"`
		} `json:"quilt_loader"`
		Minecraft struct {
			Environment string `json:"environment"`
		} `json:"minecraft"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.QuiltLoader.ID == "" {
		return ModMeta{}, nil, false
	}
	ql := raw.QuiltLoader
	rels := func(list []interface{}, kind string) []ModRel {
		var out []ModRel
		for _, d := range list {
			switch t := d.(type) {
			case string:
				out = append(out, ModRel{ID: t, Kind: kind})
			case map[string]interface{}:
				id, _ := t["id"].(string)
				if id == "" {
					continue
				}
				k := kind
				if opt, _ := t["optional"].(bool); opt && kind == "required" {
					k = "optional"
				}
				out = append(out, ModRel{ID: id, Range: jsonRange(t["versions"]), Kind: k})
			}
		}
		return out
	}
	var provides []string
	for _, p := range ql.Provides {
		switch t := p.(type) {
		case string:
			provides = append(provides, t)
		case map[string]interface{}:
			if id, ok := t["id"].(string); ok {
				provides = append(provides, id)
			}
		}
	}
	env := raw.Minecraft.Environment
	if env == "*" {
		env = ""
	}
	m := ModMeta{
		ID: ql.ID, Name: ql.Metadata.Name, Version: ql.Version, Loader: "quilt",
		Environment: env, Provides: provides,
		Depends: rels(ql.Depends, "required"),
		Breaks:  rels(ql.Breaks, "breaks"),
	}
	return m, ql.Jars, true
}

// ---------------------------------------------------------------------------
// Forge / NeoForge (TOML)
//
// mods.toml uses a small, regular subset of TOML: [[mods]] and
// [[dependencies.<modid>]] array tables with `key = value` pairs whose values
// are strings ('', "", ''' ''' or """ """), booleans or numbers. A purpose-built
// reader keeps the agent dependency-free.

type tomlTable struct {
	name string
	kv   map[string]string
}

func parseTomlTables(data []byte) []tomlTable {
	var tables []tomlTable
	cur := tomlTable{name: "", kv: map[string]string{}}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			tables = append(tables, cur)
			name := strings.Trim(stripTomlComment(line), "[] \t")
			cur = tomlTable{name: name, kv: map[string]string{}}
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(line[:eq]), `"'`)
		val := strings.TrimSpace(line[eq+1:])

		// Multi-line strings: ''' or """ possibly spanning lines.
		for _, q := range []string{`'''`, `"""`} {
			if strings.HasPrefix(val, q) {
				body := val[3:]
				for !strings.Contains(body, q) && i+1 < len(lines) {
					i++
					body += "\n" + lines[i]
				}
				if end := strings.Index(body, q); end >= 0 {
					body = body[:end]
				}
				cur.kv[key] = body
				val = ""
				break
			}
		}
		if val == "" {
			continue
		}
		// Arrays of inline tables: `mods = [ { modId = "x", ... }, ]`, possibly
		// spanning lines. Each inline table becomes its own table named by key.
		if strings.HasPrefix(val, "[") && !strings.HasPrefix(val, "[[") {
			body := val
			for tomlDepth(body) > 0 && i+1 < len(lines) {
				i++
				body += "\n" + lines[i]
			}
			name := key
			if cur.name != "" {
				name = cur.name + "." + key
			}
			for _, inline := range tomlInlineTables(body) {
				tables = append(tables, tomlTable{name: name, kv: inline})
			}
			continue
		}
		cur.kv[key] = parseTomlScalar(val)
	}
	tables = append(tables, cur)
	return tables
}

// tomlDepth returns the unclosed [ / { nesting depth of s, ignoring quoted text.
func tomlDepth(s string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		}
	}
	return depth
}

// tomlInlineTables extracts `{ k = v, ... }` tables from an array literal.
func tomlInlineTables(s string) []map[string]string {
	var out []map[string]string
	var quote byte
	start, depth := -1, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '{':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case c == '}':
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, tomlInlinePairs(s[start:i]))
				start = -1
			}
		}
	}
	return out
}

func tomlInlinePairs(s string) map[string]string {
	kv := map[string]string{}
	var quote byte
	last := 0
	flush := func(part string) {
		if eq := strings.Index(part, "="); eq > 0 {
			k := strings.Trim(strings.TrimSpace(part[:eq]), `"'`)
			kv[k] = parseTomlScalar(strings.TrimSpace(part[eq+1:]))
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ',':
			flush(s[last:i])
			last = i + 1
		}
	}
	flush(s[last:])
	return kv
}

func stripTomlComment(s string) string {
	inS, inD := false, false
	for i, c := range s {
		switch c {
		case '\'':
			if !inD {
				inS = !inS
			}
		case '"':
			if !inS {
				inD = !inD
			}
		case '#':
			if !inS && !inD {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return s
}

func parseTomlScalar(val string) string {
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
		q := val[0]
		if end := strings.IndexByte(val[1:], q); end >= 0 {
			s := val[1 : end+1]
			if q == '"' {
				s = strings.ReplaceAll(s, `\"`, `"`)
			}
			return s
		}
		return strings.Trim(val, `"'`)
	}
	return strings.TrimSpace(stripTomlComment(val))
}

func parseForgeToml(data []byte, loader, manifestVersion string) []ModMeta {
	tables := parseTomlTables(data)
	var mods []ModMeta
	deps := map[string][]ModRel{}

	for _, t := range tables {
		switch {
		case t.name == "mods":
			id := t.kv["modId"]
			if id == "" {
				continue
			}
			ver := t.kv["version"]
			if strings.Contains(ver, "${") {
				ver = manifestVersion
			}
			mods = append(mods, ModMeta{ID: id, Name: t.kv["displayName"], Version: ver, Loader: loader})
		case strings.HasPrefix(t.name, "dependencies."):
			owner := strings.TrimPrefix(t.name, "dependencies.")
			depID := t.kv["modId"]
			if depID == "" {
				continue
			}
			// Forge: mandatory=true/false. NeoForge: type="required|optional|incompatible|discouraged".
			kind := "optional"
			switch strings.ToLower(t.kv["type"]) {
			case "required":
				kind = "required"
			case "incompatible":
				kind = "breaks"
			case "discouraged":
				kind = "conflicts"
			case "":
				if t.kv["mandatory"] == "true" {
					kind = "required"
				}
			}
			rel := ModRel{ID: depID, Kind: kind, Side: strings.ToUpper(t.kv["side"])}
			if r := t.kv["versionRange"]; r != "" {
				rel.Range = []string{r}
			}
			deps[owner] = append(deps[owner], rel)
		}
	}

	for i := range mods {
		for _, rel := range deps[mods[i].ID] {
			if rel.Kind == "breaks" || rel.Kind == "conflicts" {
				mods[i].Breaks = append(mods[i].Breaks, rel)
			} else {
				mods[i].Depends = append(mods[i].Depends, rel)
			}
		}
	}
	return mods
}
