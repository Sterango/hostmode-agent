package gameserver

import "testing"

func TestHytaleAuthFromLog(t *testing.T) {
	cases := []struct {
		line string
		want HytaleAuthEvent
		ok   bool
	}{
		{"Visit https://oauth.accounts.hytale.com/oauth2/device/verify?user_code=ABCD-EFGH to log in.",
			HytaleAuthEvent{URL: "https://oauth.accounts.hytale.com/oauth2/device/verify?user_code=ABCD-EFGH", Code: "ABCD-EFGH"}, true},
		{"Enter code: WXYZ-1234", HytaleAuthEvent{Code: "WXYZ-1234"}, true},
		{"[Auth] Authentication successful, credentials saved", HytaleAuthEvent{Done: true}, true},
		{"Server listening on 0.0.0.0:5520", HytaleAuthEvent{}, false},
		{"See https://hytale.com/news for patch notes", HytaleAuthEvent{}, false},
	}
	for _, c := range cases {
		got, ok := HytaleAuthFromLog(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("%q: got %+v ok=%v, want %+v ok=%v", c.line, got, ok, c.want, c.ok)
		}
	}
}

// Every spec's ports must fit inside the block the panel reserves for it.
func TestSpecPortsFitBlock(t *testing.T) {
	for key, s := range Specs {
		for _, p := range s.Ports {
			if p.Offset < 0 || p.Offset >= s.PortCount {
				t.Errorf("%s: port offset %d outside block of %d", key, p.Offset, s.PortCount)
			}
		}
		if s.QueryOffset >= s.PortCount {
			t.Errorf("%s: query offset %d outside block of %d", key, s.QueryOffset, s.PortCount)
		}
		if s.Key != key {
			t.Errorf("%s: Key field is %q", key, s.Key)
		}
	}
}

func TestArgSafe(t *testing.T) {
	if got := argSafe(`My "Cool" Server!`); got != "My_Cool_Server" {
		t.Errorf("argSafe = %q", got)
	}
	if got := argSafe("!!!"); got != "HostMode" {
		t.Errorf("argSafe empty = %q", got)
	}
}
