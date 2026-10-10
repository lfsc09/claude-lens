// Package upstream resolves where the proxy forwards requests: the active
// proxy integration if one is configured, otherwise the Anthropic API.
package upstream

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/lfsc09/claude-lens/internal/database"
)

// AnthropicBaseURL is where requests go when no proxy integration is active.
const AnthropicBaseURL = "https://api.anthropic.com"

// Target is the resolved forwarding destination. AuthToken and Headers
// override the client's own when set; a direct-to-Anthropic Target leaves
// them empty so the client's credentials pass through.
type Target struct {
	// IntegrationID and Type identify the active proxy integration; zero
	// and "" when forwarding directly to Anthropic.
	IntegrationID int64
	Type          string
	BaseURL       *url.URL
	AuthToken     string
	Headers       map[string]string
}

// Registry holds the current Target, readable lock-free from the request
// path and refreshed whenever an integration changes.
type Registry struct {
	db      *database.DB
	current atomic.Pointer[Target]
	changed chan struct{}
}

// NewRegistry builds a Registry and loads the initial Target from db.
func NewRegistry(ctx context.Context, db *database.DB) (*Registry, error) {
	r := &Registry{db: db, changed: make(chan struct{}, 1)}
	if err := r.Refresh(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// Current returns the Target to forward to; never nil.
func (r *Registry) Current() *Target {
	return r.current.Load()
}

// Changed returns a channel that receives a value after each Refresh, so
// background work (price sync) can react to integration changes promptly.
func (r *Registry) Changed() <-chan struct{} {
	return r.changed
}

// Refresh reloads the active proxy integration from the database and swaps
// it in as the current Target.
func (r *Registry) Refresh(ctx context.Context) error {
	integration, err := r.db.GetActiveProxyIntegration(ctx)
	if err != nil {
		return fmt.Errorf("load active proxy integration: %w", err)
	}

	target, err := targetFor(integration)
	if err != nil {
		return err
	}
	r.current.Store(target)

	select {
	case r.changed <- struct{}{}:
	default:
	}
	return nil
}

func targetFor(integration *database.ProxyIntegration) (*Target, error) {
	if integration == nil {
		base, err := url.Parse(AnthropicBaseURL)
		if err != nil {
			return nil, err
		}
		return &Target{BaseURL: base}, nil
	}
	base, err := url.Parse(integration.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse %s base URL: %w", integration.Type, err)
	}
	return &Target{
		IntegrationID: integration.ID,
		Type:          integration.Type,
		BaseURL:       base,
		AuthToken:     integration.AuthToken,
		Headers:       integration.CustomHeaders,
	}, nil
}

// NormalizeBaseURL validates raw as an http(s) URL with a host and returns
// it without a trailing slash.
func NormalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("base_url must be an http:// or https:// URL")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

var headerName = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

// ValidateHeaders checks every custom header has a valid name and a
// non-empty single-line value.
func ValidateHeaders(headers map[string]string) error {
	for name, value := range headers {
		if !headerName.MatchString(name) {
			return fmt.Errorf("invalid header name %q", name)
		}
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q needs a non-empty single-line value", name)
		}
	}
	return nil
}
