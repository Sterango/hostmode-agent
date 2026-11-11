package gameserver

import (
	"regexp"
	"strings"
)

// Hytale's downloader (install) and server (first start) each print a one-time
// OAuth device-login prompt: a verification URL and a short user code that the
// owner of a Hytale account must approve in a browser. The agent watches the
// container log for these and surfaces them in the panel.

var (
	hytaleURLRe  = regexp.MustCompile(`https://[^\s"'<>]*hytale\.com[^\s"'<>]*`)
	hytaleCodeRe = regexp.MustCompile(`(?i)(?:user[_ ]?code|code)\s*[:=]\s*([A-Z0-9]{4,}(?:-[A-Z0-9]{3,})*)`)
	hytaleDoneRe = regexp.MustCompile(`(?i)(authenticat\w*|logged in|login)\s+(success\w*|complete\w*)|successfully (authenticated|logged in)`)
)

// HytaleAuthEvent is what a single log line tells us about the login flow.
type HytaleAuthEvent struct {
	URL  string // verification URL, if the line has one
	Code string // user code, if the line has one
	Done bool   // the line reports a completed login
}

// HytaleAuthFromLog extracts device-login details from one log line. ok is false
// when the line is unrelated.
func HytaleAuthFromLog(line string) (ev HytaleAuthEvent, ok bool) {
	if hytaleDoneRe.MatchString(line) {
		return HytaleAuthEvent{Done: true}, true
	}
	if m := hytaleURLRe.FindString(line); m != "" && isDeviceURL(m) {
		ev.URL = strings.TrimRight(m, ".,)")
		ok = true
	}
	if m := hytaleCodeRe.FindStringSubmatch(line); m != nil {
		ev.Code = m[1]
		ok = true
	}
	return ev, ok
}

func isDeviceURL(u string) bool {
	l := strings.ToLower(u)
	return strings.Contains(l, "device") || strings.Contains(l, "user_code") ||
		strings.Contains(l, "oauth") || strings.Contains(l, "activate") || strings.Contains(l, "verify")
}
