package proxy

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// ParseCustomHeaders parses CLENS_PROXY_CUSTOM_HEADERS: newline-separated
// "Header-Name: value" lines. Blank lines are skipped. A malformed line (no
// colon, or an empty name/value) is an error — this only ever runs once at
// startup against operator-supplied config, so failing loudly beats silently
// dropping a header the operator expected to be forwarded.
func ParseCustomHeaders(raw string) (map[string]string, error) {
	headers := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("CLENS_PROXY_CUSTOM_HEADERS must use 'Header-Name: value' format (bad line: %q)", line)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" || value == "" {
			return nil, fmt.Errorf("CLENS_PROXY_CUSTOM_HEADERS contains an empty header name or value (bad line: %q)", line)
		}
		headers[name] = value
	}
	return headers, nil
}

// SetHeader sets key to value, replacing any existing value under that
// (case-insensitive) header name. http.Header already stores keys under
// their canonical MIME form, so this is exactly http.Header.Set — named
// here so the proxy's header-mutation call sites read the same as the
// Python version's set_header calls did.
func SetHeader(headers http.Header, key, value string) {
	headers.Set(key, value)
}

var schemePrefix = regexp.MustCompile(`^[A-Za-z]+\s+`)

// FormatAuthHeader returns token unchanged if it already carries an auth
// scheme (e.g. "Bearer sk-..."), otherwise prefixes it with "Bearer ".
func FormatAuthHeader(token string) string {
	trimmed := strings.TrimSpace(token)
	if schemePrefix.MatchString(trimmed) {
		return trimmed
	}
	return "Bearer " + trimmed
}

var sessionIDDisallowed = regexp.MustCompile(`[^a-zA-Z0-9_\-]`)

// SanitizeSessionID replaces any character outside [a-zA-Z0-9_-] with "_",
// trims leading/trailing underscores, and falls back to "default_session"
// if that leaves nothing.
func SanitizeSessionID(name string) string {
	clean := sessionIDDisallowed.ReplaceAllString(name, "_")
	clean = strings.Trim(clean, "_")
	if clean == "" {
		return "default_session"
	}
	return clean
}
