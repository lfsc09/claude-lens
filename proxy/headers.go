package proxy

import (
	"regexp"
	"strings"
)

// schemePrefix matches a leading auth scheme such as "Bearer ".
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

// sessionIDDisallowed matches every character not allowed in a session id.
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
