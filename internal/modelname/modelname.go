// Package modelname reduces the model identifiers used by gateways and cloud
// providers to the plain Claude model name that model_prices is keyed by.
package modelname

import "strings"

const (
	anthropicVendorMarker = "anthropic."
	bedrockVersionSuffix  = "-v1:0"
)

// Normalize drops any leading "<provider>/" path, any "<region>.anthropic."
// vendor prefix and Bedrock's "-v1:0" version suffix from id:
// "bedrock/us.anthropic.claude-haiku-4-5-20251001-v1:0" and
// "claude-haiku-4-5-20251001" both yield "claude-haiku-4-5-20251001". An id
// without any of these is returned unchanged.
func Normalize(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.LastIndex(id, anthropicVendorMarker); i >= 0 {
		id = id[i+len(anthropicVendorMarker):]
	}
	return strings.TrimSuffix(id, bedrockVersionSuffix)
}
