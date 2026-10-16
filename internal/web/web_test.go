package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/app"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/fakecam"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/i18n"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/store"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/web"
)

type env struct {
	ctl *app.Controller
	cam *fakecam.Camera
	dir string
	t   *testing.T
}

func newEnv(t *testing.T, pairs int, logs *web.LogBuffer) *env {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.DirEnv, filepath.Join(dir, "cfg"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DestDir = filepath.Join(dir, "photos")
	cfg.AcceptPMHPairing = false
	cfg.AutoDownload = false
	cam := fakecam.New("ILCA-77M2", pairs)
	cam.AllowedGUID[cfg.OwnGUID()] = true
	if err := cam.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cam.Stop)
	db, _ := store.Open(filepath.Join(dir, "db.json"))
	c := app.New(cfg, db)
	if logs != nil {
		c.OnLog = logs.Append // before Start: the controller reads it from its goroutines
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	t.Cleanup(func() { cancel(); <-c.Stopped(); i18n.Set("en") })
	i18n.Set("en")
	return &env{ctl: c, cam: cam, dir: dir, t: t}
}

func (e *env) req(h http.Handler, method, path string, body any, mod func(*http.Request)) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Host = "127.0.0.1:8080"
	if method == http.MethodPost {
		r.Header.Set("X-SPMAG", "1")
	}
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func (e *env) state(h http.Handler) map[string]any {
	w := e.req(h, "GET", "/api/state", nil, nil)
	if w.Code != 200 {
		e.t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func waitState(t *testing.T, e *env, h http.Handler, what string, cond func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if m := e.state(h); cond(m) {
			return m
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s: %v", what, e.state(h)["title"])
	return nil
}

func TestGuards(t *testing.T) {
	e := newEnv(t, 1, nil)
	open := web.Handler(web.Options{Ctl: e.ctl, LoopbackOnly: true})
	if w := e.req(open, "GET", "/api/state", nil, nil); w.Code != 200 {
		t.Fatalf("loopback host: %d", w.Code)
	}
	if w := e.req(open, "GET", "/api/state", nil, func(r *http.Request) { r.Host = "evil.example:8080" }); w.Code != 403 {
		t.Fatalf("rebinding host must be refused, got %d", w.Code)
	}
	if w := e.req(open, "POST", "/api/action", map[string]string{"action": "stop"}, func(r *http.Request) { r.Header.Del("X-SPMAG") }); w.Code != 403 {
		t.Fatalf("missing header: %d", w.Code)
	}
	if w := e.req(open, "POST", "/api/action", map[string]string{"action": "stop"}, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); w.Code != 403 {
		t.Fatalf("cross-origin: %d", w.Code)
	}
	if w := e.req(open, "POST", "/api/action", map[string]string{"action": "stop"}, func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8080") }); w.Code != 200 {
		t.Fatalf("same-origin: %d", w.Code)
	}
	if w := e.req(open, "POST", "/api/action", map[string]string{"action": "nope"}, nil); w.Code != 400 {
		t.Fatalf("unknown action: %d", w.Code)
	}

	pw := web.Handler(web.Options{Ctl: e.ctl, Password: "s3cret"})
	if w := e.req(pw, "GET", "/api/state", nil, nil); w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no credentials: %d", w.Code)
	}
	if w := e.req(pw, "GET", "/api/state", nil, func(r *http.Request) { r.SetBasicAuth("x", "wrong") }); w.Code != 401 {
		t.Fatalf("wrong password: %d", w.Code)
	}
	if w := e.req(pw, "GET", "/api/state", nil, func(r *http.Request) { r.SetBasicAuth("anyone", "s3cret") }); w.Code != 200 {
		t.Fatalf("right password: %d", w.Code)
	}
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options"} {
		if open2 := e.req(open, "GET", "/", nil, nil); open2.Header().Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
}

func TestStaticAndStrings(t *testing.T) {
	e := newEnv(t, 1, nil)
	h := web.Handler(web.Options{Ctl: e.ctl, LoopbackOnly: true})
	for _, p := range []string{"/", "/app.js", "/style.css"} {
		if w := e.req(h, "GET", p, nil, nil); w.Code != 200 || w.Body.Len() < 100 {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	if w := e.req(h, "GET", "/", nil, nil); !strings.Contains(w.Body.String(), "SPMAG Wifi Photo Receiver") {
		t.Fatal("index title missing")
	}
	i18n.Set("ru")
	w := e.req(h, "GET", "/api/strings", nil, nil)
	var d struct {
		Lang    string
		Strings map[string]string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Lang != "ru" || d.Strings["tab.transfer"] != "Передача" {
		t.Fatalf("strings: %q %q", d.Lang, d.Strings["tab.transfer"])
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	e := newEnv(t, 1, nil)
	h := web.Handler(web.Options{Ctl: e.ctl, LoopbackOnly: true})
	dest := filepath.Join(e.dir, "new photos")
	good := map[string]any{"destDir": dest, "folderPattern": "{yyyy}/{MM}", "jpeg": true, "raw": false, "video": false, "other": true,
		"auto": true, "finish": false, "match": true, "pmh": false, "hostName": "NAS", "language": "de"}
	if w := e.req(h, "POST", "/api/settings", good, nil); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if i18n.Current() != "de" {
		t.Fatalf("language not applied: %s", i18n.Current())
	}
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		t.Fatal("destination folder not created")
	}
	cfg, _ := config.Load()
	if cfg.DestDir != dest || cfg.IncludeRAW || !cfg.IncludeOther || cfg.HostName != "NAS" || cfg.Language != "de" || cfg.FinishAfterAuto {
		t.Fatalf("saved settings: %+v", cfg)
	}
	var back struct {
		DestDir, Language string
		Languages         []struct{ Code, Name string }
	}
	w := e.req(h, "GET", "/api/settings", nil, nil)
	_ = json.Unmarshal(w.Body.Bytes(), &back)
	if len(back.Languages) != 10 || back.Languages[0].Code != "en" || back.Languages[6].Name != "Русский" {
		t.Fatalf("languages: %s", w.Body)
	}
	if back.DestDir != dest || back.Language != "de" {
		t.Fatalf("GET settings: %s", w.Body)
	}
	for name, mod := range map[string]func(map[string]any){
		"relative dest":  func(m map[string]any) { m["destDir"] = "photos" },
		"empty dest":     func(m map[string]any) { m["destDir"] = "" },
		"dotdot pattern": func(m map[string]any) { m["folderPattern"] = "../../etc" },
		"abs pattern":    func(m map[string]any) { m["folderPattern"] = "/etc" },
		"unknown lang":   func(m map[string]any) { m["language"] = "xx" },
	} {
		bad := map[string]any{}
		for k, v := range good {
			bad[k] = v
		}
		mod(bad)
		if w := e.req(h, "POST", "/api/settings", bad, nil); w.Code != 400 {
			t.Errorf("%s: want 400, got %d", name, w.Code)
		}
	}
}

func TestTransferThroughAPI(t *testing.T) {
	logs := &web.LogBuffer{}
	e := newEnv(t, 3, logs)
	h := web.Handler(web.Options{Ctl: e.ctl, Logs: logs, Version: "test", LoopbackOnly: true})
	e.ctl.ConnectIP(e.cam.Addr())
	st := waitState(t, e, h, "ready", func(m map[string]any) bool { return m["state"].(float64) == float64(app.StateReady) })
	items := st["items"].([]any)
	if len(items) != 6 || st["version"] != "test" || !strings.Contains(st["counts"].(string), "On camera: 6") {
		t.Fatalf("state: %d items, counts %q", len(items), st["counts"])
	}
	// download two chosen files, then everything else
	var keys []string
	for _, it := range items[:2] {
		keys = append(keys, it.(map[string]any)["key"].(string))
	}
	if w := e.req(h, "POST", "/api/action", map[string]any{"action": "downloadSelected", "keys": keys}, nil); w.Code != 200 {
		t.Fatalf("downloadSelected: %d %s", w.Code, w.Body)
	}
	waitState(t, e, h, "two received", func(m map[string]any) bool {
		return m["state"].(float64) == float64(app.StateReady) && strings.Contains(m["counts"].(string), "received: 2")
	})
	if w := e.req(h, "POST", "/api/action", map[string]any{"action": "downloadSelected"}, nil); w.Code != 400 {
		t.Fatalf("empty selection must fail: %d", w.Code)
	}
	if w := e.req(h, "POST", "/api/action", map[string]any{"action": "downloadMissing"}, nil); w.Code != 200 {
		t.Fatal("downloadMissing")
	}
	st = waitState(t, e, h, "all received", func(m map[string]any) bool {
		return m["state"].(float64) == float64(app.StateReady) && strings.Contains(m["counts"].(string), "received: 6")
	})
	for _, it := range st["items"].([]any) {
		m := it.(map[string]any)
		if m["statusText"] != "Received" || m["path"] == "" {
			t.Fatalf("item %v", m)
		}
	}
	// forget one -> missing
	if w := e.req(h, "POST", "/api/action", map[string]any{"action": "forget", "keys": keys[:1]}, nil); w.Code != 200 {
		t.Fatal("forget")
	}
	waitState(t, e, h, "one missing", func(m map[string]any) bool { return strings.Contains(m["counts"].(string), "received: 5") })
	// log
	w := e.req(h, "GET", "/api/log?since=0", nil, nil)
	var lg struct {
		Lines []string
		Next  int
	}
	_ = json.Unmarshal(w.Body.Bytes(), &lg)
	if len(lg.Lines) == 0 || lg.Next != len(lg.Lines) {
		t.Fatalf("log: %+v", lg)
	}
	w = e.req(h, "GET", "/api/log?since="+itoa(lg.Next), nil, nil)
	_ = json.Unmarshal(w.Body.Bytes(), &lg)
	if len(lg.Lines) != 0 {
		t.Fatalf("log since end: %+v", lg)
	}
	if w := e.req(h, "POST", "/api/action", map[string]any{"action": "finish"}, nil); w.Code != 200 {
		t.Fatal("finish")
	}
	waitState(t, e, h, "waiting", func(m map[string]any) bool { return m["state"].(float64) == float64(app.StateWaiting) })
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestLogBufferAndLoopback(t *testing.T) {
	var b web.LogBuffer
	for i := 0; i < 1500; i++ {
		b.Append("x")
	}
	lines, next := b.Since(0)
	if len(lines) != 1000 || next != 1500 {
		t.Fatalf("ring: %d %d", len(lines), next)
	}
	if l, n := b.Since(next); len(l) != 0 || n != next {
		t.Fatal("since end")
	}
	for addr, want := range map[string]bool{"127.0.0.1:8080": true, "localhost:80": true, "[::1]:9": true, ":8080": false, "0.0.0.0:80": false, "192.168.1.5:80": false} {
		if web.IsLoopbackAddr(addr) != want {
			t.Errorf("IsLoopbackAddr(%q) != %v", addr, want)
		}
	}
}
