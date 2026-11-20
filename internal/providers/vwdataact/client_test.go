package vwdataact

import (
	"bytes"
	"context"
	"errors"
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

// A permanent redirect from the portal host to another host is a moved portal, not "no active request" (the old host
// rewrites API paths to pages that answer 404). A redirect into the identity provider stays an expired session.
func TestPermanentRedirectOffThePortalIsAMove(t *testing.T) {
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><form>sign in</form></html>"))
	}))
	defer identity.Close()
	var target string
	var code int
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/en"+r.URL.Path, code)
	}))
	defer portal.Close()
	cl, err := New(Config{BaseURL: portal.URL, IdentityBase: identity.URL, MinInterval: time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range []int{http.StatusMovedPermanently, http.StatusPermanentRedirect} {
		target, code = other.URL, c
		_, err := cl.Identifier(ctx, "WVWZZZTEST0000001", RequestContinuous)
		if ClassOf(err) != ClassProtocol || EndpointOf(err) != EndpointIdentifier {
			t.Errorf("%d to another host: got %v, want a moved portal", c, err)
		}
		if _, err := cl.Vehicles(ctx); ClassOf(err) != ClassProtocol {
			t.Errorf("%d to another host (vehicles): got %v", c, err)
		}
	}
	target, code = identity.URL, http.StatusMovedPermanently
	if _, err := cl.Identifier(ctx, "WVWZZZTEST0000001", RequestContinuous); ClassOf(err) != ClassAuth {
		t.Errorf("301 into the identity provider: got %v, want an expired session", err)
	}
}

// A permanent redirect from the portal host to a foreign host is not followed (the request carries the VIN in its
// path); redirects within the portal, into the identity provider, and downloads handed to a storage host still work.
func TestCrossHostPermanentRedirectNotFollowed(t *testing.T) {
	var hits int
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer foreign.Close()
	for _, code := range []int{http.StatusMovedPermanently, http.StatusPermanentRedirect} {
		hits = 0
		portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, foreign.URL+"/en"+r.URL.Path, code)
		}))
		cl, err := New(Config{BaseURL: portal.URL, IdentityBase: "http://identity.invalid", MinInterval: time.Millisecond}, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _, gerr := cl.get(context.Background(), EndpointVehicles, portal.URL+"/proxy_api/x/WVWZZZTEST0000001", nil, 1<<20)
		var pe *Error
		if !errors.As(gerr, &pe) || pe.Class != ClassProtocol || pe.Status != code || pe.Endpoint != EndpointVehicles {
			t.Fatalf("%d: want a protocol error for a moved portal, got %v", code, gerr)
		}
		if hits != 0 {
			t.Fatalf("%d: the redirect target received %d requests", code, hits)
		}
		// A 302 to the same foreign host is not a move: it is followed as before.
		portal.Close()
	}
}
