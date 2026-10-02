package moddata

import (
	"strconv"
	"strings"
)

// Version predicates are matched leniently: anything we can't parse counts as
// satisfied, so a quirky range never produces a false "incompatible" warning.

// compareVersions orders two version strings following Maven's
// ComparableVersion rules (used by Forge/NeoForge, and a superset of what
// Fabric versions need):
//   - "." and "_" separate items; "-" opens a sub-list, so "1.21-2.0.11" is
//     1.21 followed by the list (2.0.11);
//   - a number outranks a sub-list or qualifier at the same position, so
//     "1.21.1-x" > "1.21-y";
//   - known qualifiers order alpha < beta < milestone < rc < snapshot <
//     release < sp < unknown words (alphabetical);
//   - "+build" metadata is ignored.
func compareVersions(a, b string) int {
	return compareTokens(versionTokens(a), versionTokens(b))
}

type vtok struct {
	kind byte // 'n' number, 'w' word, '-' sub-list marker
	num  int
	word string
}

func versionTokens(v string) []vtok {
	v = strings.ToLower(strings.TrimSpace(v))
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	var toks []vtok
	i := 0
	for i < len(v) {
		c := v[i]
		switch {
		case c >= '0' && c <= '9':
			j := i
			for j < len(v) && v[j] >= '0' && v[j] <= '9' {
				j++
			}
			n, _ := strconv.Atoi(v[i:j])
			toks = append(toks, vtok{kind: 'n', num: n})
			i = j
		case isLetter(c):
			j := i
			for j < len(v) && isLetter(v[j]) {
				j++
			}
			toks = append(toks, vtok{kind: 'w', word: v[i:j]})
			i = j
		case c == '-':
			toks = append(toks, vtok{kind: '-'})
			i++
		default: // '.', '_', anything else separates
			i++
		}
	}
	// Trailing sub-list markers / zero padding carry no meaning.
	for len(toks) > 0 && (toks[len(toks)-1].kind == '-' || (toks[len(toks)-1].kind == 'n' && toks[len(toks)-1].num == 0)) {
		toks = toks[:len(toks)-1]
	}
	return toks
}

func qualifierRank(w string) (int, bool) {
	switch w {
	case "a", "alpha":
		return 0, true
	case "b", "beta":
		return 1, true
	case "m", "milestone":
		return 2, true
	case "rc", "cr", "pre":
		return 3, true
	case "snapshot":
		return 4, true
	case "", "ga", "final", "release":
		return 5, true
	case "sp":
		return 6, true
	}
	return 0, false
}

// compareWithNothing orders the rest of a token list against "end of version".
func compareWithNothing(rest []vtok) int {
	for _, t := range rest {
		switch t.kind {
		case 'n':
			if t.num != 0 {
				return 1
			}
		case 'w':
			if r, known := qualifierRank(t.word); known {
				if r < 5 {
					return -1
				}
				if r > 5 {
					return 1
				}
				continue
			}
			return 1 // Maven: unknown qualifiers sort after release ("1.21z" > "1.21.x")
		}
	}
	return 0
}

func compareTokens(a, b []vtok) int {
	i := 0
	for ; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		switch {
		case x.kind == 'n' && y.kind == 'n':
			if x.num != y.num {
				if x.num < y.num {
					return -1
				}
				return 1
			}
		case x.kind == y.kind && x.kind == '-':
			// both open a sub-list: keep going
		case x.kind == 'n':
			return 1 // a number outranks a sub-list or qualifier
		case y.kind == 'n':
			return -1
		case x.kind == '-':
			return 1 // a sub-list outranks a qualifier
		case y.kind == '-':
			return -1
		default: // both words
			xr, xk := qualifierRank(x.word)
			yr, yk := qualifierRank(y.word)
			switch {
			case xk && yk && xr != yr:
				if xr < yr {
					return -1
				}
				return 1
			case xk != yk:
				if xk {
					return -1 // known qualifiers sort before unknown words
				}
				return 1
			case x.word != y.word:
				return strings.Compare(x.word, y.word)
			}
		}
	}
	if i < len(a) {
		return compareWithNothing(a[i:])
	}
	if i < len(b) {
		return -compareWithNothing(b[i:])
	}
	return 0
}

// splitPre separates a pre-release qualifier (a hyphen followed by a letter).
// Only used for the "~" / "^" / wildcard predicates, which look at numeric
// segments.
func splitPre(v string) (string, string) {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	for i := 0; i < len(v)-1; i++ {
		if v[i] == '-' && isLetter(v[i+1]) {
			return v[:i], v[i+1:]
		}
	}
	return strings.TrimRight(v, "-"), ""
}

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func splitSegs(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '_' || r == '-' })
}

func compareSeg(x, y string) int {
	xi, xErr := strconv.Atoi(x)
	yi, yErr := strconv.Atoi(y)
	if xErr == nil && yErr == nil {
		switch {
		case xi < yi:
			return -1
		case xi > yi:
			return 1
		}
		return 0
	}
	return strings.Compare(x, y)
}

// versionSatisfies reports whether version matches any of the OR-ed ranges.
// An empty range list, an empty/unknown version, or an unparseable range all
// count as satisfied. maven selects Forge semantics, where a bare version
// ("2.0.0-beta.17") is a soft requirement that any version satisfies.
func versionSatisfies(version string, ranges []string, maven bool) bool {
	if len(ranges) == 0 || version == "" || strings.Contains(version, "${") {
		return true
	}
	for _, r := range ranges {
		r = strings.TrimSpace(r)
		if maven && !strings.HasPrefix(r, "[") && !strings.HasPrefix(r, "(") {
			return true
		}
		if ok, parsed := matchRange(version, r); ok || !parsed {
			return true
		}
	}
	return false
}

// versionMatchesStrict is the opposite bias, for "breaks" ranges: only a
// definite, parsed match counts, so we never invent an incompatibility.
func versionMatchesStrict(version string, ranges []string, maven bool) bool {
	if version == "" || strings.Contains(version, "${") {
		return len(ranges) == 0
	}
	if len(ranges) == 0 {
		return true
	}
	for _, r := range ranges {
		r = strings.TrimSpace(r)
		if maven && !strings.HasPrefix(r, "[") && !strings.HasPrefix(r, "(") {
			continue
		}
		if ok, parsed := matchRange(version, r); ok && parsed {
			return true
		}
	}
	return false
}

// matchRange returns (matches, understood).
func matchRange(v, r string) (bool, bool) {
	if r == "" || r == "*" {
		return true, true
	}
	if strings.HasPrefix(r, "[") || strings.HasPrefix(r, "(") {
		return matchMaven(v, r)
	}
	// Fabric/Quilt: space-separated predicates are AND-ed.
	for _, p := range strings.Fields(r) {
		ok, parsed := matchPredicate(v, p)
		if !parsed {
			return false, false
		}
		if !ok {
			return false, true
		}
	}
	return true, true
}

// matchMaven handles "[1.0,2.0)", "[1.20.1]", "[47,)", and comma-joined unions
// like "[1.0,1.1),[1.2,)".
func matchMaven(v, r string) (bool, bool) {
	var parts []string
	depth, start := 0, 0
	for i, c := range r {
		switch c {
		case '[', '(':
			depth++
		case ']', ')':
			depth--
			if depth == 0 {
				parts = append(parts, r[start:i+1])
				start = i + 1
			}
		case ',':
			if depth == 0 {
				start = i + 1
			}
		}
	}
	if len(parts) == 0 {
		return false, false
	}
	for _, p := range parts {
		if len(p) < 2 {
			return false, false
		}
		loInc := p[0] == '['
		hiInc := p[len(p)-1] == ']'
		inner := p[1 : len(p)-1]
		if !strings.Contains(inner, ",") {
			if compareVersions(v, inner) == 0 {
				return true, true
			}
			continue
		}
		bounds := strings.SplitN(inner, ",", 2)
		lo, hi := strings.TrimSpace(bounds[0]), strings.TrimSpace(bounds[1])
		ok := true
		if lo != "" {
			c := compareVersions(v, lo)
			ok = ok && (c > 0 || (loInc && c == 0))
		}
		if hi != "" {
			c := compareVersions(v, hi)
			ok = ok && (c < 0 || (hiInc && c == 0))
		}
		if ok {
			return true, true
		}
	}
	return false, true
}

func matchPredicate(v, p string) (bool, bool) {
	ops := []string{">=", "<=", ">", "<", "=", "~", "^"}
	op := ""
	for _, o := range ops {
		if strings.HasPrefix(p, o) {
			op = o
			p = strings.TrimSpace(p[len(o):])
			break
		}
	}
	if p == "" || p == "*" {
		return true, true
	}
	// Wildcards: "1.20.x" / "1.20.*" match any version with that prefix.
	if strings.HasSuffix(p, ".x") || strings.HasSuffix(p, ".X") || strings.HasSuffix(p, ".*") {
		prefix := p[:len(p)-2]
		vs, ps := splitSegs(stripPre(v)), splitSegs(prefix)
		if len(vs) < len(ps) {
			return false, true
		}
		for i := range ps {
			if compareSeg(vs[i], ps[i]) != 0 {
				return false, true
			}
		}
		return true, true
	}
	if !startsWithDigit(p) {
		return false, false
	}
	c := compareVersions(v, p)
	switch op {
	case ">=":
		return c >= 0, true
	case "<=":
		return c <= 0, true
	case ">":
		return c > 0, true
	case "<":
		return c < 0, true
	case "", "=":
		return c == 0, true
	case "~": // same major.minor, >= p
		segs := splitSegs(stripPre(p))
		vs := splitSegs(stripPre(v))
		if c < 0 || len(segs) < 1 || len(vs) < 1 || compareSeg(vs[0], segs[0]) != 0 {
			return false, true
		}
		if len(segs) >= 2 && (len(vs) < 2 || compareSeg(vs[1], segs[1]) != 0) {
			return false, true
		}
		return true, true
	case "^": // same major, >= p
		segs := splitSegs(stripPre(p))
		vs := splitSegs(stripPre(v))
		return c >= 0 && len(segs) > 0 && len(vs) > 0 && compareSeg(vs[0], segs[0]) == 0, true
	}
	return false, false
}

func stripPre(v string) string {
	s, _ := splitPre(v)
	return s
}

func startsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}
