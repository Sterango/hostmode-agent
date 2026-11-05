package moddata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledLoaderVersion(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{"1.20.1-47.4.3", "1.20.1-47.4.16", "1.20.1-47.4.10"} {
		if err := os.MkdirAll(filepath.Join(dir, "libraries/net/minecraftforge/forge", v), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if got := installedLoaderVersion(dir, "forge"); got != "47.4.16" {
		t.Errorf("forge version = %q, want the highest, 47.4.16", got)
	}
	if got := installedLoaderVersion(dir, "neoforge"); got != "" {
		t.Errorf("missing loader = %q, want empty", got)
	}
}

func TestAnalyzeLoader(t *testing.T) {
	jar := func(rng string) []JarInfo {
		return []JarInfo{{Filename: "sable.jar", Mods: []ModMeta{{
			ID: "sable", Name: "Sable", Loader: "neoforge",
			Depends: []ModRel{{ID: "neoforge", Range: []string{rng}, Kind: "required"}},
		}}}}
	}

	issues := analyzeLoader(jar("[21.1.228,)"), "neoforge", "21.1.219")
	if len(issues) != 1 || issues[0].Code != "loader_version" {
		t.Fatalf("issues = %+v, want one loader_version", issues)
	}
	want := "Sable needs NeoForge 21.1.228 or newer, but this server has 21.1.219."
	if issues[0].Message != want {
		t.Errorf("message = %q, want %q", issues[0].Message, want)
	}

	if got := analyzeLoader(jar("[21.1.200,)"), "neoforge", "21.1.219"); len(got) != 0 {
		t.Errorf("satisfied range reported: %+v", got)
	}
	if got := analyzeLoader(jar("[21.1.228,)"), "neoforge", ""); len(got) != 0 {
		t.Errorf("unknown loader version reported: %+v", got)
	}
}
