package priceprovider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetJSON_SendsBearerTokenAndDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q, want Bearer secret", got)
		}
		w.Write([]byte(`{"name":"x"}`))
	}))
	defer srv.Close()

	var out struct{ Name string }
	if err := GetJSON(context.Background(), NewHTTPClient(), srv.URL, "secret", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if out.Name != "x" {
		t.Errorf("Name = %q, want x", out.Name)
	}
}

func TestGetJSON_NonOKStatusIsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := GetJSON(context.Background(), NewHTTPClient(), srv.URL, "", &struct{}{})
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusNotFound {
		t.Fatalf("got %v, want a *StatusError with code 404", err)
	}
}

func TestGetJSON_RejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"pad":"` + strings.Repeat("a", maxResponseBytes) + `"}`))
	}))
	defer srv.Close()

	err := GetJSON(context.Background(), NewHTTPClient(), srv.URL, "", &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("got %v, want an oversized-response error", err)
	}
}

func TestModelPrice_Priced(t *testing.T) {
	for _, tc := range []struct {
		price ModelPrice
		want  bool
	}{
		{ModelPrice{InputPerM: 1, OutputPerM: 2}, true},
		{ModelPrice{InputPerM: 1}, false},
		{ModelPrice{OutputPerM: 2}, false},
		{ModelPrice{}, false},
	} {
		if got := tc.price.Priced(); got != tc.want {
			t.Errorf("%+v.Priced() = %v, want %v", tc.price, got, tc.want)
		}
	}
}
