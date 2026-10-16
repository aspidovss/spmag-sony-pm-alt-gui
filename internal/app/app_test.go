package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/app"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/fakecam"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ssdp"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/store"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/transfer"
)

func waitFor(t *testing.T, c *app.Controller, what string, cond func(app.Snapshot) bool) app.Snapshot {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s := c.Snapshot()
		if cond(s) {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := c.Snapshot()
	t.Fatalf("timeout waiting for %s; state=%v msg=%q", what, s.State, s.Message)
	return s
}

func setup(t *testing.T, pairs int) (*fakecam.Camera, config.Settings, *store.DB, string) {
	cam := fakecam.New("ILCE-77M2", pairs)
	cfg := config.Defaults()
	cam.AllowedGUID[cfg.OwnGUID()] = true
	if err := cam.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cam.Stop)
	dir := t.TempDir()
	cfg.DestDir = filepath.Join(dir, "photos")
	cfg.AcceptPMHPairing = false
	db, _ := store.Open(filepath.Join(dir, "db.json"))
	return cam, cfg, db, dir
}

func startCtl(t *testing.T, cfg config.Settings, db *store.DB) *app.Controller {
	c := app.New(cfg, db)
	var mu sync.Mutex
	c.OnLog = func(l string) { mu.Lock(); t.Log(l); mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-c.Stopped() })
	c.Start(ctx)
	return c
}

func TestManualFlowAndReconcile(t *testing.T) {
	cam, cfg, db, _ := setup(t, 4)
	cfg.IncludeRAW = false // RAW excluded by filter
	c := startCtl(t, cfg, db)

	c.ConnectIP(cam.Addr())
	s := waitFor(t, c, "ready", func(s app.Snapshot) bool { return s.State == app.StateReady })
	if s.Counts.Total != 8 || s.Counts.Missing != 4 || s.Counts.Excluded != 4 {
		t.Fatalf("counts %+v", s.Counts)
	}
	c.DownloadMissing()
	s = waitFor(t, c, "jpegs received", func(s app.Snapshot) bool { return s.State == app.StateReady && s.Counts.Received == 4 })
	for _, it := range s.Items {
		if it.Kind == transfer.KindJPEG {
			b, err := os.ReadFile(it.LocalPath)
			if err != nil {
				t.Fatal(err)
			}
			var src []byte
			for _, f := range cam.Files {
				if f.Name == it.Name {
					src = f.Data
				}
			}
			if string(b) != string(src) {
				t.Fatalf("content mismatch for %s", it.Name)
			}
			if !strings.Contains(it.LocalPath, filepath.Join("2026", "2026-10-14")) {
				t.Fatalf("unexpected path %s", it.LocalPath)
			}
			st, _ := os.Stat(it.LocalPath)
			if !st.ModTime().Equal(it.Captured) {
				t.Errorf("mtime %v != %v", st.ModTime(), it.Captured)
			}
		}
	}
	// pick one RAW explicitly despite the filter
	var rawKey, jpgPath, jpgKey string
	for _, it := range s.Items {
		if it.Kind == transfer.KindRAW && rawKey == "" {
			rawKey = it.Key
		}
		if it.Kind == transfer.KindJPEG && jpgPath == "" {
			jpgPath, jpgKey = it.LocalPath, it.Key
		}
	}
	c.DownloadKeys([]string{rawKey})
	waitFor(t, c, "raw received", func(s app.Snapshot) bool { return s.State == app.StateReady && s.Counts.Received == 5 })

	// delete a received file locally -> missing again after rescan
	os.Remove(jpgPath)
	c.Rescan()
	s = waitFor(t, c, "rescan", func(s app.Snapshot) bool { return s.State == app.StateReady && s.Counts.Received == 4 })
	for _, it := range s.Items {
		if it.Key == jpgKey && (it.Status != transfer.StatusMissing || it.Note == "") {
			t.Fatalf("deleted file status %v note %q", it.Status, it.Note)
		}
	}
	c.Disconnect(true)
	waitFor(t, c, "waiting", func(s app.Snapshot) bool { return s.State == app.StateWaiting })
	if n := len(cam.SonyStates); n < 2 || cam.SonyStates[n-1] != 5 {
		t.Fatalf("camera not told to finish: %v", cam.SonyStates)
	}
}

func TestMatchExistingFiles(t *testing.T) {
	cam, cfg, db, _ := setup(t, 2)
	// pretend PlayMemories already imported one file into some other folder
	old := filepath.Join(cfg.DestDir, "PMH", "2026-10-14")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, cam.Files[0].Name), cam.Files[0].Data, 0o644)
	// same name, different size => not the same file
	os.WriteFile(filepath.Join(old, cam.Files[1].Name), []byte("other"), 0o644)
	c := startCtl(t, cfg, db)
	c.ConnectIP(cam.Addr())
	s := waitFor(t, c, "ready", func(s app.Snapshot) bool { return s.State == app.StateReady })
	if s.Counts.Received != 1 || s.Counts.Missing != 3 {
		t.Fatalf("counts %+v", s.Counts)
	}
}

func TestNotPaired(t *testing.T) {
	cam, cfg, db, _ := setup(t, 1)
	cam.AllowedGUID = map[[16]byte]bool{{1}: true}
	c := startCtl(t, cfg, db)
	c.ConnectIP(cam.Addr())
	s := waitFor(t, c, "failure", func(s app.Snapshot) bool { return strings.Contains(s.Message, "not paired") })
	if s.State != app.StateWaiting {
		t.Fatal(s.State)
	}
}

// Full automatic path: SSDP NOTIFY -> description -> connect -> download -> finish.
func TestAutoImportViaSSDP(t *testing.T) {
	cam := fakecam.New("ILCE-77M2", 3)
	cfg := config.Defaults()
	cam.AllowedGUID[cfg.OwnGUID()] = true
	if err := cam.Start("127.0.0.1:15740"); err != nil {
		t.Skip("port 15740 busy:", err)
	}
	defer cam.Stop()
	loc, err := cam.StartHTTP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.DestDir = filepath.Join(dir, "photos")
	cfg.AcceptPMHPairing = false
	db, _ := store.Open(filepath.Join(dir, "db.json"))
	c := startCtl(t, cfg, db)

	// feed the NOTIFY the camera would multicast
	l := &ssdp.Listener{OnCamera: func(sc ssdp.Camera) {
		if sc.FriendlyName != "ILCE-77M2" || sc.IP != "127.0.0.1" {
			t.Errorf("camera %+v", sc)
		}
	}}
	l.HandleDatagram(context.Background(), cam.NotifyMessage(loc), "127.0.0.1")
	// and drive the controller the same way its own listener would
	got := make(chan ssdp.Camera, 1)
	l2 := &ssdp.Listener{OnCamera: func(sc ssdp.Camera) { got <- sc }}
	l2.HandleDatagram(context.Background(), cam.NotifyMessage(loc), "127.0.0.1")
	sc := <-got
	app.InjectDiscovery(c, sc)

	waitFor(t, c, "auto finished", func(s app.Snapshot) bool {
		return s.State == app.StateWaiting && strings.Contains(s.Message, "6 new file(s)")
	})
	if n := len(cam.SonyStates); n == 0 || cam.SonyStates[n-1] != 5 {
		t.Fatalf("finish not sent: %v", cam.SonyStates)
	}
	if db.Len() != 6 {
		t.Fatalf("db has %d records", db.Len())
	}
	// camera keeps announcing for a while: must not reconnect during cooldown
	conns := cam.Connections
	app.InjectDiscovery(c, sc)
	time.Sleep(300 * time.Millisecond)
	if cam.Connections != conns {
		t.Fatal("reconnected during cooldown")
	}
}
