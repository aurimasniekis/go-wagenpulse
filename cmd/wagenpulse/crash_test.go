package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aurimasniekis/go-wagenpulse/internal/store"
)

// TestCrashDuringImportRecovers runs the real binary, SIGKILLs it in the middle of a multi-file import and restarts
// it. The import must resume (orphaned lease released at startup) and finish with every observation stored exactly
// once. All data is synthetic.
func TestCrashDuringImportRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the server binary")
	}
	const files, keys = 80, 300 // 80 deliveries × 300 signals × 3 capture times = 72,000 observations
	dir := t.TempDir()
	bin := filepath.Join(dir, "wagenpulse")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil { //nolint:gosec // test build
		t.Fatalf("build: %v\n%s", err, out)
	}
	port, mport := freePort(t), freePort(t)
	base := fmt.Sprintf("http://localhost:%d", port)
	env := append(cleanEnv(), "WAGENPULSE_MODE=development", "WAGENPULSE_DATA_DIR="+filepath.Join(dir, "data"),
		"WAGENPULSE_PUBLIC_URL="+base, fmt.Sprintf("WAGENPULSE_HTTP_LISTEN=127.0.0.1:%d", port),
		fmt.Sprintf("WAGENPULSE_MANAGEMENT_LISTEN=127.0.0.1:%d", mport), "WAGENPULSE_LOG_LEVEL=warn")
	logf, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logf.Close() }()
	start := func() *exec.Cmd {
		cmd := exec.Command(bin, "serve") //nolint:gosec // test binary
		cmd.Env, cmd.Stdout, cmd.Stderr = env, logf, logf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waitReady(t, mport)
		return cmd
	}
	srv := start()
	defer func() { _ = srv.Process.Kill(); _, _ = srv.Process.Wait() }()

	tok := exec.Command(bin, "admin", "setup-token") //nolint:gosec // test binary
	tok.Env = env
	out, err := tok.Output()
	if err != nil {
		t.Fatalf("setup-token: %v", err)
	}
	setup := regexp.MustCompile(`wgp_setup_\S+`).FindString(string(out))
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: base, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	me := c.json("POST", "/api/v1/setup", map[string]any{"token": setup, "username": "root", "password": "crash-test-password-1234"})
	c.csrf, _ = me["csrf_token"].(string)
	veh := c.json("POST", "/api/v1/vehicles", map[string]any{"display_name": "Crash test", "vin": "XXXZZZ00ZXX000009"})

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("vehicle_id", veh["id"].(string))
	_ = mw.WriteField("event_policy", "none")
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for f := range files {
		w, _ := mw.CreateFormFile("files", fmt.Sprintf("synthetic-%03d.json", f))
		_, _ = w.Write(syntheticDelivery("XXXZZZ00ZXX000009", keys, t0.Add(time.Duration(f)*15*time.Minute)))
	}
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/uploads/deliveries", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	im := c.do(req)
	importID := im["id"].(string)

	// Kill -9 once a few files are done and the import is still running.
	deadline := time.Now().Add(60 * time.Second)
	for {
		st := c.json("GET", "/api/v1/imports/"+importID, nil)["import"].(map[string]any)
		done := int(st["files_done"].(float64))
		if st["state"] == "running" && done >= 5 {
			t.Logf("killing the server at %d/%d files", done, files)
			break
		}
		if st["state"] != "running" && st["state"] != "queued" {
			t.Fatalf("import finished before the crash could be injected (%v); increase the workload", st["state"])
		}
		if time.Now().After(deadline) {
			t.Fatal("import did not progress")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := srv.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_, _ = srv.Process.Wait()

	srv = start()
	restarted := time.Now()
	var final map[string]any
	for {
		final = c.json("GET", "/api/v1/imports/"+importID, nil)["import"].(map[string]any)
		if final["state"] != "running" && final["state"] != "queued" {
			break
		}
		if time.Since(restarted) > 60*time.Second {
			t.Fatalf("import did not finish after restart: %v", final)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("resumed and finished %s after restart: accepted=%v duplicate=%v files=%v/%v", time.Since(restarted).Round(time.Millisecond),
		final["observations_accepted"], final["observations_duplicate"], final["files_done"], final["files_total"])
	if final["state"] != "succeeded" || int(final["files_done"].(float64)) != files {
		t.Fatalf("import after restart: %v", final)
	}
	if time.Since(restarted) > 30*time.Second {
		t.Fatalf("recovery waited for lease expiry (%s); orphaned leases should be released at startup", time.Since(restarted))
	}
	_ = srv.Process.Signal(syscall.SIGTERM)
	_, _ = srv.Process.Wait()

	d, err := store.OpenFileReadOnly(filepath.Join(dir, "data", store.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var n, rd int
	if err := d.QueryRow(`SELECT count(*) FROM observations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM raw_deliveries`).Scan(&rd); err != nil {
		t.Fatal(err)
	}
	if n != files*keys*3 || rd != files {
		t.Fatalf("observations=%d (want %d), raw deliveries=%d (want %d)", n, files*keys*3, rd, files)
	}
}

func syntheticDelivery(vin string, keys int, at time.Time) []byte {
	type rec struct {
		Key   string `json:"key"`
		Name  string `json:"dataFieldName"`
		Value string `json:"value"`
		TS    string `json:"timestampUtc"`
	}
	recs := make([]rec, 0, keys*3)
	for k := range 3 {
		ts := at.Add(time.Duration(k) * 5 * time.Minute).Format("2006-01-02T15:04:05.000Z")
		for i := range keys {
			recs = append(recs, rec{Key: fmt.Sprintf("00000000-0000-3000-8000-%012d", i), Name: fmt.Sprintf("synthetic_%d", i),
				Value: fmt.Sprint((i*31 + k + at.Minute()) % 1000), TS: ts})
		}
	}
	b, _ := json.Marshal(map[string]any{"vin": vin, "user_id": "synthetic", "Data": recs})
	return b
}

type client struct {
	t    *testing.T
	base string
	csrf string
	http *http.Client
}

func (c *client) json(method, path string, body any) map[string]any {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r) //nolint:noctx // test
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req)
}

func (c *client) do(req *http.Request) map[string]any {
	c.t.Helper()
	req.Header.Set("Origin", c.base)
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		c.t.Fatalf("%s %s: %d %s", req.Method, req.URL.Path, res.StatusCode, raw)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func waitReady(t *testing.T, mport int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/readyz", mport), nil)
		if res, err := http.DefaultClient.Do(req); err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}

// cleanEnv drops WAGENPULSE_* variables from the test's environment (the server rejects unknown overrides).
func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "WAGENPULSE_") {
			out = append(out, kv)
		}
	}
	return out
}
