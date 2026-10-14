package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func guardedRequest(t *testing.T, allowAnyHost bool, method, host string, headers map[string]string, body string) int {
	t.Helper()
	handler := guardMiddleware(allowAnyHost)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, "http://"+host+"/api/limiters", strings.NewReader(body))
	req.Host = host
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

func TestGuard_HostAllowlist(t *testing.T) {
	cases := map[string]int{
		"localhost:7802": http.StatusOK,
		"127.0.0.1:7802": http.StatusOK,
		"[::1]:7802":     http.StatusOK,
		"localhost":      http.StatusOK,
		"evil.test:7802": http.StatusForbidden,
		"192.168.1.9":    http.StatusForbidden,
	}
	for host, want := range cases {
		if got := guardedRequest(t, false, http.MethodGet, host, nil, ""); got != want {
			t.Errorf("Host %q: status = %d, want %d", host, got, want)
		}
	}
}

func TestGuard_AllowAnyHostSkipsHostCheck(t *testing.T) {
	if got := guardedRequest(t, true, http.MethodGet, "claude-lens.lan:7802", nil, ""); got != http.StatusOK {
		t.Errorf("status = %d, want 200", got)
	}
}

func TestGuard_RejectsCrossOriginStateChange(t *testing.T) {
	json := map[string]string{"Content-Type": "application/json"}
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"same origin", map[string]string{"Origin": "http://localhost:7802", "Content-Type": "application/json"}, http.StatusOK},
		{"no origin", json, http.StatusOK},
		{"foreign origin", map[string]string{"Origin": "http://evil.test", "Content-Type": "application/json"}, http.StatusForbidden},
		{"other port origin", map[string]string{"Origin": "http://localhost:3000", "Content-Type": "application/json"}, http.StatusForbidden},
		{"null origin", map[string]string{"Origin": "null", "Content-Type": "application/json"}, http.StatusForbidden},
		{"cross-site fetch metadata", map[string]string{"Sec-Fetch-Site": "cross-site", "Content-Type": "application/json"}, http.StatusForbidden},
		{"same-site fetch metadata", map[string]string{"Sec-Fetch-Site": "same-site", "Content-Type": "application/json"}, http.StatusForbidden},
		{"same-origin fetch metadata", map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}, http.StatusOK},
	}
	for _, tc := range cases {
		if got := guardedRequest(t, false, http.MethodPost, "localhost:7802", tc.headers, `{}`); got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestGuard_RequiresJSONBodies(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"json", "application/json", `{}`, http.StatusOK},
		{"json with charset", "application/json; charset=utf-8", `{}`, http.StatusOK},
		{"text/plain", "text/plain", `{}`, http.StatusUnsupportedMediaType},
		{"form", "application/x-www-form-urlencoded", `a=b`, http.StatusUnsupportedMediaType},
		{"missing", "", `{}`, http.StatusUnsupportedMediaType},
		{"bodiless post", "", ``, http.StatusOK},
	}
	for _, tc := range cases {
		headers := map[string]string{}
		if tc.contentType != "" {
			headers["Content-Type"] = tc.contentType
		}
		if got := guardedRequest(t, false, http.MethodPost, "localhost:7802", headers, tc.body); got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestGuard_IgnoresCrossOriginReads(t *testing.T) {
	headers := map[string]string{"Origin": "http://evil.test", "Sec-Fetch-Site": "cross-site"}
	if got := guardedRequest(t, false, http.MethodGet, "localhost:7802", headers, ""); got != http.StatusOK {
		t.Errorf("status = %d, want 200", got)
	}
}
