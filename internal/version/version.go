// Package version holds the pure version parsing, formatting, and comparison
// helpers used to read agent `--version` output, registry "latest" lookups, and
// to decide whether an installed version is outdated. Everything here is pure
// (no process/IO) and independently testable.
package version

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var semverTokenRe = regexp.MustCompile(`(?i)\bv?\d+\.\d+(?:\.\d+)?(?:-[0-9a-z.-]+)?(?:\+[0-9a-z.-]+)?\b`)

// ExtractToken returns the first semver-like token in s.
func ExtractToken(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if match := semverTokenRe.FindString(s); match != "" {
		return match, true
	}
	return "", false
}

// FormatWithToken substitutes newVersion for the version token found in before,
// preserving surrounding text (e.g. "codex-cli 0.90.0" -> "codex-cli 0.98.0").
func FormatWithToken(before, newVersion string) string {
	newVersion = strings.TrimSpace(newVersion)
	if newVersion == "" {
		return ""
	}
	before = strings.TrimSpace(before)
	if before == "" || before == "unknown" {
		return newVersion
	}
	token, ok := ExtractToken(before)
	if !ok {
		return newVersion
	}
	if strings.HasPrefix(token, "v") && !strings.HasPrefix(newVersion, "v") {
		newVersion = "v" + newVersion
	}
	return strings.Replace(before, token, newVersion, 1)
}

// Same reports whether two parsed `--version` outputs denote the same version.
// Exact string equality counts; otherwise both must contain a version token and
// the tokens must be identical. Tools decorate the version with state that
// toggles between invocations (e.g. grok appends " [stable]" only while its
// update-check cache is fresh), so raw output equality would misreport an
// unchanged agent as updated.
func Same(a, b string) bool {
	if a == b {
		return true
	}
	at, aok := ExtractToken(a)
	bt, bok := ExtractToken(b)
	return aok && bok && at == bt
}

// ParseOutput parses a `--version` command's combined output into a clean
// version string, preferring a line that is just a version. When several lines
// qualify the last one wins: tools that emit noise around the version print
// banners or log lines first and the version last.
func ParseOutput(out string) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return "unknown"
	}
	lines := strings.Split(trimmed, "\n")
	first := ""
	versionOnly := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		if isVersionOnlyLine(line) {
			versionOnly = line
		}
	}
	if versionOnly != "" {
		return versionOnly
	}
	if first != "" {
		return first
	}
	return "unknown"
}

// isVersionOnlyLine reports whether line is nothing but a version. The primary
// predicate is the same one ParseLatest uses: ExtractToken consumes the whole
// line, which covers prerelease and build-metadata forms like "1.2.3-rc.1".
// The dotted-numeric fallback keeps four-plus-component versions such as
// "1.2.3.4", which the semver token regex would truncate to "1.2.3".
func isVersionOnlyLine(line string) bool {
	if token, ok := ExtractToken(line); ok && token == line {
		return true
	}
	return isDottedNumeric(line)
}

// isDottedNumeric reports whether line is dot-separated decimal components
// (at least two), optionally prefixed with "v".
func isDottedNumeric(line string) bool {
	line = strings.TrimPrefix(line, "v")
	parts := strings.Split(line, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// ParseLatest pulls the version out of a registry query's stdout. It prefers a
// line whose entire content is a single version token - the normal single-field
// output of `npm view dist-tags.latest` and friends - so advisory banner lines
// are ignored whether the tool prints them before or after the version. It only
// falls back to an embedded token when no standalone line exists.
func ParseLatest(out string) string {
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		s := strings.Trim(strings.TrimSpace(line), "\"'")
		if s == "" {
			continue
		}
		if token, ok := ExtractToken(s); ok && token == s {
			return token
		}
	}
	for _, line := range lines {
		s := strings.Trim(strings.TrimSpace(line), "\"'")
		if token, ok := ExtractToken(s); ok {
			return token
		}
	}
	return ""
}

// ParseBunJSON extracts the top-level version from `bun info --json` output,
// which may be a JSON scalar ("6.0.3") or the full manifest object.
func ParseBunJSON(out string) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return ""
	}
	var scalar string
	if err := json.Unmarshal([]byte(trimmed), &scalar); err == nil {
		if token, ok := ExtractToken(scalar); ok {
			return token
		}
	}
	var obj struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil && obj.Version != "" {
		if token, ok := ExtractToken(obj.Version); ok {
			return token
		}
	}
	return ""
}

// ParseBrewLatest extracts the stable version from `brew info --json=v2` output.
func ParseBrewLatest(out string) string {
	var payload struct {
		Formulae []struct {
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
		} `json:"formulae"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return ""
	}
	if len(payload.Formulae) == 0 {
		return ""
	}
	if token, ok := ExtractToken(payload.Formulae[0].Versions.Stable); ok {
		return token
	}
	return ""
}

// Compare orders two version tokens: <0 if a is older, 0 if equal, >0 if a is
// newer. It compares numeric base components (missing trailing components are
// zero, so "1.2" == "1.2.0"); on an equal base, a release outranks a prerelease
// and prerelease tails follow SemVer precedence (spec item 11). Build metadata
// is ignored. Intentionally lightweight (no semver dependency).
func Compare(a, b string) int {
	abase, apre := splitVersion(a)
	bbase, bpre := splitVersion(b)
	ac, bc := numericComponents(abase), numericComponents(bbase)
	for i := 0; i < len(ac) || i < len(bc); i++ {
		var x, y int
		if i < len(ac) {
			x = ac[i]
		}
		if i < len(bc) {
			y = bc[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == "" && bpre == "":
		return 0
	case apre == "": // a is a release, b a prerelease of the same base -> a newer
		return 1
	case bpre == "": // a is a prerelease, b the released version -> a older
		return -1
	default:
		return comparePrerelease(apre, bpre)
	}
}

// splitVersion strips a leading "v" and build metadata (which semver says must
// be ignored for precedence), then separates the numeric base from any
// prerelease tail.
func splitVersion(v string) (base, pre string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

// comparePrerelease orders two non-empty prerelease tails per SemVer spec item
// 11: dot-separated identifiers compare left to right, numeric identifiers
// compare numerically (so "rc.9" < "rc.10"), a numeric identifier always sorts
// below an alphanumeric one, alphanumeric identifiers compare ASCII-lexically,
// and when every shared identifier is equal the shorter tail sorts first.
func comparePrerelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, y := as[i], bs[i]
		if x == y {
			continue
		}
		xn, xok := numericIdentifier(x)
		yn, yok := numericIdentifier(y)
		switch {
		case xok && yok:
			if xn < yn {
				return -1
			}
			return 1
		case xok:
			return -1
		case yok:
			return 1
		case x < y:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	default:
		return 0
	}
}

// numericIdentifier parses a prerelease identifier as a decimal number,
// reporting false for alphanumeric identifiers (and empty ones).
func numericIdentifier(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func numericComponents(base string) []int {
	parts := strings.Split(base, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			// Deliberate: a malformed component ("1.x.3") maps to 0 so Compare
			// stays total over arbitrary tool output instead of failing.
			n = 0
		}
		out = append(out, n)
	}
	return out
}
