package moddata

import "testing"

func TestVersionSatisfies(t *testing.T) {
	cases := []struct {
		v    string
		r    []string
		want bool
	}{
		{"1.20.1", []string{"[1.20,1.21)"}, true},
		{"1.21", []string{"[1.20,1.21)"}, false},
		{"1.20.1", []string{"[1.20.1]"}, true},
		{"47.2.0", []string{"[47,)"}, true},
		{"46.0.1", []string{"[47,)"}, false},
		{"0.92.0+1.20.1", []string{">=0.90.0"}, true},
		{"0.80.0", []string{">=0.90.0"}, false},
		{"1.20.1", []string{"1.20.x"}, true},
		{"1.21", []string{"1.20.x"}, false},
		{"1.20.4", []string{"~1.20"}, true},
		{"1.21.1", []string{">=1.21 <1.22"}, true},
		{"1.22", []string{">=1.21 <1.22"}, false},
		{"1.19.2", []string{"1.18.2", "1.19.2"}, true},
		{"1.20.1", []string{"*"}, true},
		{"2.0.0-beta.1", []string{">=2.0.0"}, false},
		{"5.0", []string{"weird range"}, true},
	}
	for _, c := range cases {
		if got := versionSatisfies(c.v, c.r, false); got != c.want {
			t.Errorf("versionSatisfies(%q, %v) = %v, want %v", c.v, c.r, got, c.want)
		}
	}
}

func TestMavenSemantics(t *testing.T) {
	cases := []struct {
		v    string
		r    string
		want bool
	}{
		{"2.0.0", "2.0.0-beta.17", true},            // bare = soft requirement
		{"1.11.0-1.21.1", "[1.11.0-, 1.12-)", true}, // MC-suffixed version
		{"1.21-2.0.11", "(,1.21-2.0.5]", false},     // hyphenated numeric segments
		{"1.21-2.0.4", "(,1.21-2.0.5]", true},
		{"1.21.1-3.14.8", "(,1.21-3.4.4]", false}, // number outranks sub-list
		{"1.21.1-92-NEOFORGE", "[1.21-87-NEOFORGE,)", true},
	}
	for _, c := range cases {
		if got := versionSatisfies(c.v, []string{c.r}, true); got != c.want {
			t.Errorf("maven versionSatisfies(%q, %q) = %v, want %v", c.v, c.r, got, c.want)
		}
	}
	if !minecraftCompatible("1.21.1", []string{"[1.21,1.21z)"}, true) || !minecraftCompatible("1.21.1", []string{"[1.21,1.21.1)"}, true) {
		t.Error("1.21.1 should be accepted for 1.21-line ranges")
	}
	if minecraftCompatible("1.21.1", []string{"[1.20,1.20.2)"}, true) {
		t.Error("1.21.1 should be rejected for a 1.20-line range")
	}
	if versionMatchesStrict("1.21-2.0.11", []string{"(,1.21-2.0.5]"}, true) {
		t.Error("strict match should be false")
	}
}

func TestParseForgeToml(t *testing.T) {
	toml := `
modLoader="javafml" #mandatory
loaderVersion="[47,)"
license='MIT'

[[mods]]
modId="create"
version="${file.jarVersion}"
displayName="Create"
description='''
Multi
line'''

[[dependencies.create]]
    modId="forge"
    mandatory=true
    versionRange="[47.1.3,)"
    side="BOTH"
[[dependencies.create]]
    modId="flywheel"
    mandatory=true
    versionRange="[0.6.10,0.6.11)"
[[dependencies.create]]
    modId="optifine"
    type="incompatible"
`
	mods := parseForgeToml([]byte(toml), "forge", "0.5.1.f")
	if len(mods) != 1 || mods[0].ID != "create" || mods[0].Version != "0.5.1.f" || mods[0].Name != "Create" {
		t.Fatalf("unexpected mods: %+v", mods)
	}
	if len(mods[0].Depends) != 2 || mods[0].Depends[1].ID != "flywheel" || mods[0].Depends[1].Kind != "required" {
		t.Fatalf("unexpected depends: %+v", mods[0].Depends)
	}
	if len(mods[0].Breaks) != 1 || mods[0].Breaks[0].ID != "optifine" {
		t.Fatalf("unexpected breaks: %+v", mods[0].Breaks)
	}
}

func TestParseInlineToml(t *testing.T) {
	toml := "modLoader = 'javafml'\nmods = [\n\t{ modId = 'mes', version = '2.0.0', displayName = \"Moogs, End\" },\n]\n[[dependencies.mes]]\nmodId = \"moogs_structures\"\nmandatory = true\nversionRange = \"[1.1.0,)\"\n"
	mods := parseForgeToml([]byte(toml), "neoforge", "")
	if len(mods) != 1 || mods[0].ID != "mes" || mods[0].Name != "Moogs, End" || len(mods[0].Depends) != 1 {
		t.Fatalf("unexpected: %+v", mods)
	}
}

func TestParseFabric(t *testing.T) {
	js := `{"schemaVersion":1,"id":"sodium","version":"0.5.8","name":"Sodium","environment":"client",
	"depends":{"fabricloader":">=0.12.0","minecraft":["1.20.4","1.20.5"]},"breaks":{"optifabric":"*"},
	"jars":[{"file":"META-INF/jars/x.jar"}]}`
	m, nested, ok := parseFabricMeta([]byte(js))
	if !ok || m.ID != "sodium" || m.Environment != "client" || len(nested) != 1 || len(m.Breaks) != 1 {
		t.Fatalf("unexpected: %+v %v %v", m, nested, ok)
	}
}

func TestAnalyze(t *testing.T) {
	set := []JarInfo{
		{Filename: "create.jar", Loader: "forge", Mods: []ModMeta{{ID: "create", Name: "Create", Version: "0.5.1", Loader: "forge",
			Depends: []ModRel{{ID: "flywheel", Kind: "required", Range: []string{"[0.6.10,0.6.11)"}}, {ID: "minecraft", Kind: "required", Range: []string{"[1.20.1,1.20.2)"}}}}}},
		{Filename: "flywheel.jar", Loader: "forge", Candidate: true, Mods: []ModMeta{{ID: "flywheel", Version: "0.6.9", Loader: "forge"}}},
		{Filename: "sodium.jar", Loader: "fabric", Mods: []ModMeta{{ID: "sodium", Loader: "fabric"}}},
	}
	issues := analyze(set, "1.20.1", "forge")
	codes := map[string]bool{}
	for _, i := range issues {
		codes[i.Code] = true
	}
	if !codes["dependency_version"] || !codes["wrong_loader"] || codes["wrong_minecraft"] {
		t.Fatalf("unexpected issues: %+v", issues)
	}
}

func TestCurseForgeFingerprint(t *testing.T) {
	// Whitespace is ignored, so these hash identically.
	if curseForgeFingerprint([]byte("a b\nc")) != curseForgeFingerprint([]byte("abc")) {
		t.Fatal("whitespace should be ignored")
	}
}
