package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeServerDirName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "special chars (! and |) and spaces become a clean slug",
			in:   "Mastering Magic | A Magic World with Quests!",
			want: "mastering-magic-a-magic-world-with-quests",
		},
		{
			name: "version dots become dashes",
			in:   "Craftoria 1.30.0",
			want: "craftoria-1-30-0",
		},
		{
			name: "apostrophes dropped, no double dashes",
			in:   "Len's Server",
			want: "len-s-server",
		},
		{
			name: "leading/trailing junk trimmed",
			in:   "  !!Hello World!!  ",
			want: "hello-world",
		},
		{
			name: "empty / all-special falls back to 'server'",
			in:   "!!!",
			want: "server",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeServerDirName(tc.in); got != tc.want {
				t.Errorf("sanitizeServerDirName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestUniqueServerDir(t *testing.T) {
	root := t.TempDir()
	first := uniqueServerDir(root, "bedrock ;)", "uuid-a")
	if filepath.Base(first) != "bedrock" {
		t.Fatalf("first dir = %q, want bedrock", first)
	}
	os.MkdirAll(first, 0755)
	os.WriteFile(filepath.Join(first, ".uuid"), []byte("uuid-a\n"), 0644)

	// Same name, different server: must not share the folder.
	if got := filepath.Base(uniqueServerDir(root, "bedrock ;)", "uuid-b")); got != "bedrock-2" {
		t.Errorf("second server dir = %q, want bedrock-2", got)
	}
	// Retried create for the same server reuses its own folder.
	if got := uniqueServerDir(root, "bedrock ;)", "uuid-a"); got != first {
		t.Errorf("retry dir = %q, want %q", got, first)
	}
	// A leftover folder with no .uuid is not claimed either.
	os.MkdirAll(filepath.Join(root, "bedrock-2"), 0755)
	if got := filepath.Base(uniqueServerDir(root, "bedrock ;)", "uuid-c")); got != "bedrock-3" {
		t.Errorf("third server dir = %q, want bedrock-3", got)
	}
}
