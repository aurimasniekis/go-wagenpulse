package vwdataact

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Which redirects count as an expired session: only those into the sign-in flow (the identity provider, or the portal
// outside its API). A download handed off to another host (for example a storage host) is data.
func TestRedirectClassification(t *testing.T) {
	zipBytes := []byte("PK\x03\x04synthetic")
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	}))
	defer storage.Close()
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><form>sign in</form></html>"))
	}))
	defer identity.Close()
	var target string
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/signin":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("sign in"))
		default:
			http.Redirect(w, r, target, http.StatusFound)
		}
	}))
	defer portal.Close()

	cl, err := New(Config{BaseURL: portal.URL, IdentityBase: identity.URL, MinInterval: time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	target = storage.URL + "/blob/1"
	got, err := cl.Download(ctx, "WVWZZZTEST0000001", "req-1", RequestContinuous, "a.zip")
	if err != nil || !bytes.Equal(got, zipBytes) {
		t.Fatalf("download redirected to a storage host: %v", err)
	}

	for name, to := range map[string]string{
		"identity provider":      identity.URL + "/oidc/v1/authorize",
		"portal outside its API": portal.URL + "/signin",
	} {
		target = to
		if _, err := cl.Download(ctx, "WVWZZZTEST0000001", "req-1", RequestContinuous, "a.zip"); ClassOf(err) != ClassAuth {
			t.Errorf("redirect to the %s: got %v, want an expired session", name, err)
		}
	}
}
