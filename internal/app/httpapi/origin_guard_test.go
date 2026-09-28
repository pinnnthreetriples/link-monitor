package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOriginGuardRequiresRequestOrigin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		host   string
		origin string
		want   int
	}{
		{"same origin", "127.0.0.1:8731", "http://127.0.0.1:8731", http.StatusOK},
		{"different port", "127.0.0.1:8731", "http://127.0.0.1:8732", http.StatusForbidden},
		{"different loopback name", "127.0.0.1:8731", "http://localhost:8731", http.StatusForbidden},
		{"different scheme", "127.0.0.1:8731", "https://127.0.0.1:8731", http.StatusForbidden},
		{"origin with path", "127.0.0.1:8731", "http://127.0.0.1:8731/path", http.StatusForbidden},
		{"same foreign origin", "evil.example:8731", "http://evil.example:8731", http.StatusForbidden},
	}
	h := New(Deps{Status: newFakeStatus()})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/api/status", nil)
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("Host %q, Origin %q = %d, want %d", tc.host, tc.origin, rec.Code, tc.want)
			}
		})
	}
}

func TestOriginGuardRejectsNonlocalHostWithoutOrigin(t *testing.T) {
	t.Parallel()

	h := New(Deps{Status: newFakeStatus()})
	for _, tc := range []struct {
		name string
		host string
		want int
	}{
		{"local CLI", "localhost:8731", http.StatusOK},
		{"SSH tunnel", "127.0.0.1:48217", http.StatusOK},
		{"foreign DNS name", "evil.example:8731", http.StatusForbidden},
		{"foreign IP", "192.0.2.10:8731", http.StatusForbidden},
		{"missing port", "127.0.0.1", http.StatusForbidden},
		{"invalid port", "127.0.0.1:bad", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8731/api/status", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("Host %q = %d, want %d", tc.host, rec.Code, tc.want)
			}
		})
	}
}

func TestOriginGuardAllowsPeerclipThroughSSHTunnel(t *testing.T) {
	t.Parallel()

	h, clip := clipHandler(clipOn())
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:48217/api/clip/receive",
		strings.NewReader(`{"text":"from peer"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST through SSH tunnel = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := clip.arrived(); len(got) != 1 || got[0] != "from peer" {
		t.Errorf("received = %q, want the peer's item", got)
	}
}
