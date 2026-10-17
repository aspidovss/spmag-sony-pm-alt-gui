package web_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/web"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// get fetches path from the local server, returning the status code (0 when the
// connection is refused).
func get(t *testing.T, port int, path, user, pass string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestLoginAndPassword(t *testing.T) {
	e := newEnv(t, 1, nil)
	h := web.Handler(web.Options{Ctl: e.ctl, User: "admin", Password: "s3cret"})
	for _, c := range []struct {
		user, pass string
		want       int
	}{
		{"admin", "s3cret", 200},
		{"admin", "wrong", 401},
		{"root", "s3cret", 401}, // right password, wrong login
		{"", "s3cret", 401},
	} {
		w := e.req(h, "GET", "/api/state", nil, func(r *http.Request) { r.SetBasicAuth(c.user, c.pass) })
		if w.Code != c.want {
			t.Errorf("login %q password %q: got %d, want %d", c.user, c.pass, w.Code, c.want)
		}
	}
}

func TestManagerLifecycle(t *testing.T) {
	e := newEnv(t, 1, nil)
	m := web.NewManager(web.Options{Ctl: e.ctl, Version: "test"})
	defer m.Stop()

	p1 := freePort(t)
	if err := m.Apply(web.Access{Enabled: true, Port: p1, User: "bob", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); !st.Running || !strings.HasSuffix(st.Addr, fmt.Sprint(p1)) {
		t.Fatalf("status: %+v", st)
	}
	if got := get(t, p1, "/api/state", "", ""); got != 401 {
		t.Fatalf("no credentials: %d", got)
	}
	if got := get(t, p1, "/api/state", "bob", "pw"); got != 200 {
		t.Fatalf("right credentials: %d", got)
	}
	if got := get(t, p1, "/api/state", "alice", "pw"); got != 401 {
		t.Fatalf("wrong login: %d", got)
	}

	// another port and password
	p2 := freePort(t)
	if err := m.Apply(web.Access{Enabled: true, Port: p2, User: "bob", Password: "new"}); err != nil {
		t.Fatal(err)
	}
	if got := get(t, p1, "/api/state", "bob", "pw"); got != 0 {
		t.Fatalf("old port still open: %d", got)
	}
	if got := get(t, p2, "/api/state", "bob", "pw"); got != 401 {
		t.Fatalf("old password accepted: %d", got)
	}
	if got := get(t, p2, "/api/state", "bob", "new"); got != 200 {
		t.Fatalf("new password refused: %d", got)
	}

	// port already taken: error reported, nothing left running
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	err := m.Apply(web.Access{Enabled: true, Port: busy.Addr().(*net.TCPAddr).Port})
	if err == nil {
		t.Fatal("expected an error for a port in use")
	}
	if st := m.Status(); st.Running || st.Err == "" {
		t.Fatalf("status after failure: %+v", st)
	}

	// disabling stops the server
	_ = m.Apply(web.Access{Enabled: true, Port: p1, Password: "x"})
	if err := m.Apply(web.Access{Enabled: false, Port: p1}); err != nil {
		t.Fatal(err)
	}
	if got := get(t, p1, "/api/state", "", ""); got != 0 {
		t.Fatalf("still serving after disable: %d", got)
	}
	if m.Status().Running {
		t.Fatal("status says running")
	}
}

func TestAccessValidation(t *testing.T) {
	for _, c := range []struct {
		name string
		a    web.Access
		ok   bool
	}{
		{"disabled is always fine", web.Access{Enabled: false, Port: 0, Remote: true}, true},
		{"local without password", web.Access{Enabled: true, Port: 8080}, true},
		{"remote with password", web.Access{Enabled: true, Port: 8080, Remote: true, Password: "x"}, true},
		{"remote without password", web.Access{Enabled: true, Port: 8080, Remote: true}, false},
		{"port 0", web.Access{Enabled: true, Port: 0}, false},
		{"port too big", web.Access{Enabled: true, Port: 70000}, false},
	} {
		if err := c.a.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.name, err)
		}
	}
	a := web.Access{Enabled: true}.Normalize()
	if a.Port != config.DefaultWebPort || a.User != config.DefaultWebUser {
		t.Errorf("defaults: %+v", a)
	}
	if l := (web.Access{Enabled: true, Port: 9000}).Listen(); l.Addr != "127.0.0.1:9000" {
		t.Errorf("local listen address: %q", l.Addr)
	}
	if l := (web.Access{Enabled: true, Port: 9000, Remote: true, Password: "x"}).Listen(); l.Addr != ":9000" {
		t.Errorf("network listen address: %q", l.Addr)
	}
}

// post sends a settings update through the real HTTP server.
func post(t *testing.T, port int, user, pass string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/settings", port), bytes.NewReader(b))
	req.Header.Set("X-SPMAG", "1")
	req.SetBasicAuth(user, pass)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestWebSettingsFromThePage(t *testing.T) {
	e := newEnv(t, 1, nil)
	m := web.NewManager(web.Options{Ctl: e.ctl, Version: "test"})
	defer m.Stop()
	var saved config.Settings
	m.OnSettings = func(s config.Settings) { saved = s }
	p1 := freePort(t)
	start := web.Access{Enabled: true, Port: p1, User: "admin", Password: "pw1"}
	cfg := e.ctl.Settings()
	start.Apply(&cfg) // the running server always matches the saved settings
	e.ctl.SetSettings(cfg)
	if err := m.Apply(start); err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"destDir": filepath.Join(e.dir, "photos"), "folderPattern": "", "jpeg": true,
		"webEnabled": true, "webPort": p1, "webUser": "admin"}
	with := func(kv ...any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1]
		}
		return out
	}
	// invalid combinations are refused and change nothing
	if code, _ := post(t, p1, "admin", "pw1", with("webRemote", true, "webClearPassword", true)); code != 400 {
		t.Errorf("remote without password: %d", code)
	}
	if code, _ := post(t, p1, "admin", "pw1", with("webPort", 70000)); code != 400 {
		t.Errorf("bad port: %d", code)
	}
	if got := get(t, p1, "/api/state", "admin", "pw1"); got != 200 {
		t.Fatalf("server must be unchanged: %d", got)
	}

	// an empty password keeps the old one; the password is never sent back
	code, out := post(t, p1, "admin", "pw1", with("webUser", "boss"))
	if code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	if out["webPasswordSet"] != true || out["webRestart"] != true {
		t.Errorf("reply: %v", out)
	}
	if _, leaked := out["webPassword"]; leaked {
		t.Error("the reply contains the password")
	}
	if saved.WebUser != "boss" || saved.WebPassword != "pw1" {
		t.Errorf("OnSettings got %+v", saved)
	}
	deadline := time.Now().Add(5 * time.Second)
	for get(t, p1, "/api/state", "boss", "pw1") != 200 {
		if time.Now().After(deadline) {
			t.Fatal("server did not restart with the new login")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := get(t, p1, "/api/state", "admin", "pw1"); got != 401 {
		t.Errorf("old login still accepted: %d", got)
	}

	// move to another port with a new password
	p2 := freePort(t)
	if code, out = post(t, p1, "boss", "pw1", with("webUser", "boss", "webPort", p2, "webPassword", "pw2")); code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	for get(t, p2, "/api/state", "boss", "pw2") != 200 {
		if time.Now().After(deadline.Add(5 * time.Second)) {
			t.Fatal("server did not move to the new port")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := get(t, p1, "/api/state", "", ""); got != 0 {
		t.Errorf("old port still open: %d", got)
	}
	disk, _ := config.Load()
	if disk.WebPort != p2 || disk.WebPassword != "pw2" || disk.WebUser != "boss" || !disk.WebEnabled {
		t.Errorf("settings on disk: %+v", disk)
	}

	// GET never contains the password
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/api/settings", p2), nil)
	req.SetBasicAuth("boss", "pw2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte("pw2")) {
		t.Errorf("GET /api/settings leaks the password: %s", body)
	}
}

func TestPinnedWebOptionsAreReadOnly(t *testing.T) {
	e := newEnv(t, 1, nil)
	m := web.NewManager(web.Options{Ctl: e.ctl, Version: "test"})
	defer m.Stop()
	m.Pin()
	p := freePort(t)
	if err := m.Start(web.Listen{Addr: fmt.Sprintf("127.0.0.1:%d", p), Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"destDir": filepath.Join(e.dir, "photos"), "jpeg": true,
		"webEnabled": false, "webPort": 1234, "webUser": "x", "webPassword": "hacked"}
	code, out := post(t, p, "any", "pw", body)
	if code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	if out["webLocked"] != true {
		t.Errorf("webLocked missing: %v", out)
	}
	time.Sleep(700 * time.Millisecond) // a (wrong) restart would have happened by now
	if got := get(t, p, "/api/state", "any", "pw"); got != 200 {
		t.Fatalf("pinned server changed: %d", got)
	}
	if cfg := e.ctl.Settings(); cfg.WebPassword == "hacked" || cfg.WebPort == 1234 {
		t.Errorf("pinned options were saved: %+v", cfg)
	}
}

func TestOpenAddressNeedsPassword(t *testing.T) {
	e := newEnv(t, 1, nil)
	m := web.NewManager(web.Options{Ctl: e.ctl})
	defer m.Stop()
	if err := m.Start(web.Listen{Addr: ":0"}, false); err == nil {
		t.Fatal("a network address without a password must be refused")
	}
	if m.Status().Running {
		t.Fatal("must not be running")
	}
}

func TestQuitEndpoint(t *testing.T) {
	e := newEnv(t, 1, nil)

	// without a Quit callback there is no Exit button and no endpoint
	plain := web.Handler(web.Options{Ctl: e.ctl, LoopbackOnly: true})
	if st := e.state(plain); st["canQuit"] != false {
		t.Errorf("canQuit without callback: %v", st["canQuit"])
	}
	if w := e.req(plain, "POST", "/api/quit", map[string]string{}, nil); w.Code != 404 {
		t.Errorf("quit without callback: %d", w.Code)
	}

	quit := make(chan struct{}, 1)
	h := web.Handler(web.Options{Ctl: e.ctl, LoopbackOnly: true, Quit: func() { quit <- struct{}{} }})
	if st := e.state(h); st["canQuit"] != true {
		t.Errorf("canQuit with callback: %v", st["canQuit"])
	}
	if w := e.req(h, "GET", "/api/quit", nil, nil); w.Code != 405 {
		t.Errorf("GET quit: %d", w.Code)
	}
	// still protected against cross-site requests
	if w := e.req(h, "POST", "/api/quit", map[string]string{}, func(r *http.Request) { r.Header.Del("X-SPMAG") }); w.Code != 403 {
		t.Errorf("quit without header: %d", w.Code)
	}
	select {
	case <-quit:
		t.Fatal("quit ran for a refused request")
	case <-time.After(500 * time.Millisecond):
	}
	if w := e.req(h, "POST", "/api/quit", map[string]string{}, nil); w.Code != 200 {
		t.Fatalf("quit: %d", w.Code)
	}
	select {
	case <-quit:
	case <-time.After(3 * time.Second):
		t.Fatal("quit callback not called")
	}
}

func TestBruteForceIsStopped(t *testing.T) {
	e := newEnv(t, 1, nil)
	h := web.Handler(web.Options{Ctl: e.ctl, User: "admin", Password: "s3cret"})
	tryWith := func(user, pass string) int {
		return e.req(h, "GET", "/api/state", nil, func(r *http.Request) {
			r.RemoteAddr = "203.0.113.9:5555"
			if user != "" {
				r.SetBasicAuth(user, pass)
			}
		}).Code
	}
	// the browser's first request has no credentials: it does not count
	for i := 0; i < 8; i++ {
		if c := tryWith("", ""); c != 401 {
			t.Fatalf("anonymous request: %d", c)
		}
	}
	for i := 0; i < 5; i++ {
		if c := tryWith("admin", "wrong"); c != 401 {
			t.Fatalf("guess %d: %d", i, c)
		}
	}
	// now even the right password is refused for a while
	if c := tryWith("admin", "s3cret"); c != 429 {
		t.Fatalf("after 5 failures: %d, want 429", c)
	}
	// another address is not affected
	w := e.req(h, "GET", "/api/state", nil, func(r *http.Request) {
		r.RemoteAddr = "198.51.100.7:1234"
		r.SetBasicAuth("admin", "s3cret")
	})
	if w.Code != 200 {
		t.Fatalf("other address: %d", w.Code)
	}
}

func TestSecurityHeadersAndLimits(t *testing.T) {
	e := newEnv(t, 1, nil)
	h := web.Handler(web.Options{Ctl: e.ctl, LoopbackOnly: true})
	w := e.req(h, "GET", "/", nil, nil)
	for k, want := range map[string]string{
		"X-Frame-Options":              "DENY",
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "no-store",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe-") {
		t.Errorf("CSP: %q", csp)
	}
	// embedded files cannot be escaped from
	for _, p := range []string{"/../go.mod", "/..%2f..%2fgo.mod", "/static/../../go.mod", "/%2e%2e/%2e%2e/etc/passwd"} {
		if w := e.req(h, "GET", p, nil, nil); w.Code == 200 && strings.Contains(w.Body.String(), "module ") {
			t.Errorf("%s leaked a file", p)
		}
	}
	// oversized request bodies are cut off
	big := strings.Repeat("a", 2<<20)
	if w := e.req(h, "POST", "/api/settings", map[string]string{"destDir": big}, nil); w.Code != 400 {
		t.Errorf("2 MB body: %d, want 400", w.Code)
	}
	// log lines are capped (they can contain text from the network)
	var lb web.LogBuffer
	lb.Append(strings.Repeat("x", 100000))
	lines, _ := lb.Since(0)
	if len(lines) != 1 || len(lines[0]) > 2100 {
		t.Errorf("log line length %d", len(lines[0]))
	}
}

func TestCameraAccessListThroughThePage(t *testing.T) {
	e := newEnv(t, 1, nil)
	h := web.Handler(web.Options{Ctl: e.ctl, LoopbackOnly: true})
	body := func(list ...string) map[string]any {
		return map[string]any{"destDir": filepath.Join(e.dir, "photos"), "jpeg": true, "allowedCameras": list}
	}
	// typos are refused and nothing is saved
	if w := e.req(h, "POST", "/api/settings", body("192.168.1.5", "oops"), nil); w.Code != 400 {
		t.Fatalf("invalid entry: %d %s", w.Code, w.Body)
	}
	if got := e.ctl.Settings().AllowedCameras; len(got) != 0 {
		t.Fatalf("saved despite the error: %v", got)
	}
	// a valid list is cleaned (comments, duplicates, separators) and saved
	w := e.req(h, "POST", "/api/settings", body("192.168.1.0/24, serial:123\n# note\n192.168.1.0/24", "model:ILCA-77M2"), nil)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	cfg, _ := config.Load()
	if strings.Join(cfg.AllowedCameras, "|") != "192.168.1.0/24|serial:123|model:ILCA-77M2" {
		t.Fatalf("saved list: %v", cfg.AllowedCameras)
	}
	var back struct{ AllowedCameras []string }
	_ = json.Unmarshal(e.req(h, "GET", "/api/settings", nil, nil).Body.Bytes(), &back)
	if len(back.AllowedCameras) != 3 {
		t.Fatalf("GET: %v", back.AllowedCameras)
	}
	// "Connect to IP" respects the list
	if w := e.req(h, "POST", "/api/action", map[string]string{"action": "connectIP", "ip": "10.9.9.9"}, nil); w.Code != 400 {
		t.Fatalf("address outside the list: %d", w.Code)
	}
	if w := e.req(h, "POST", "/api/action", map[string]string{"action": "connectIP", "ip": "192.168.1.77"}, nil); w.Code != 200 {
		t.Fatalf("address inside the list: %d %s", w.Code, w.Body)
	}
	// without a list public addresses are still refused
	body0 := body()
	e.req(h, "POST", "/api/settings", body0, nil)
	if w := e.req(h, "POST", "/api/action", map[string]string{"action": "connectIP", "ip": "8.8.8.8"}, nil); w.Code != 400 {
		t.Fatalf("public address: %d", w.Code)
	}
}
