package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/app"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/fakecam"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/i18n"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ptpip"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ssdp"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/store"
)

// A device that pretends to be a camera must not be able to write outside the
// destination folder through the file names it reports.
func TestHostileFileNamesStayInsideDestination(t *testing.T) {
	cam := fakecam.New("ILCE-77M2", 0)
	cfg := config.Defaults()
	cam.AllowedGUID[cfg.OwnGUID()] = true
	dir := t.TempDir()
	cfg.DestDir = filepath.Join(dir, "photos")
	cfg.AcceptPMHPairing = false
	cfg.FolderPattern = ""
	cfg.IncludeOther = true
	db, _ := store.Open(filepath.Join(dir, "db.json"))
	taken := time.Date(2026, 10, 14, 9, 30, 0, 0, time.Local)
	names := []string{
		"../escaped1.txt",
		`..\escaped2.txt`,
		"../../escaped3.txt",
		"sub/../../escaped4.txt",
		`C:\Windows\escaped5.txt`,
		"/etc/escaped6.txt",
		"..",
		".",
		"",
		"CON.txt",
		"trailing dot.",
		"nul\x00byte.txt",
	}
	for i, n := range names {
		cam.Files = append(cam.Files, &fakecam.File{Folder: "100MSDCF", Name: n, Data: []byte("pwned"), Taken: taken.Add(time.Duration(i) * time.Minute)})
	}
	// files must be added before the camera starts (it indexes them then)
	if err := cam.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cam.Stop)
	c := startCtl(t, cfg, db)
	c.ConnectIP(cam.Addr())
	waitFor(t, c, "ready", func(s app.Snapshot) bool { return s.State == app.StateReady })
	c.DownloadMissing()
	waitFor(t, c, "download finished", func(s app.Snapshot) bool {
		return s.State == app.StateReady && s.Counts.Missing == 0
	})

	// nothing may appear outside <dir>/photos
	dest := filepath.Join(dir, "photos")
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rel, err := filepath.Rel(dest, p); err != nil || strings.HasPrefix(rel, "..") {
			if filepath.Base(p) != "db.json" && !strings.HasSuffix(p, ".part") {
				t.Errorf("file written outside the destination folder: %s", p)
			}
		}
		return nil
	})
	if _, err := os.Stat("/etc/escaped6.txt"); err == nil {
		t.Error("wrote to /etc")
	}
	// every item that was saved lives inside the destination
	for _, it := range c.Snapshot().Items {
		if it.LocalPath == "" {
			continue
		}
		if rel, err := filepath.Rel(dest, it.LocalPath); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("%q saved to %s", it.Name, it.LocalPath)
		}
	}
}

// ---- access list for cameras ------------------------------------------------

func waitWaiting(t *testing.T, c *app.Controller, contains string) app.Snapshot {
	t.Helper()
	return waitFor(t, c, "refusal", func(s app.Snapshot) bool {
		return s.State == app.StateWaiting && strings.Contains(s.Message, contains)
	})
}

func hasOp(cam *fakecam.Camera, op uint16) bool {
	for _, o := range cam.Ops {
		if o == op {
			return true
		}
	}
	return false
}

func TestACLAddressRuleBlocksBeforeConnecting(t *testing.T) {
	i18n.Set("en")
	cam, cfg, db, _ := setup(t, 2)
	cfg.AllowedCameras = []string{"192.168.77.0/24"} // the camera is on 127.0.0.1
	c := startCtl(t, cfg, db)
	c.ConnectIP(cam.Addr())
	waitWaiting(t, c, "not on the allowed cameras list")
	time.Sleep(200 * time.Millisecond)
	if cam.Connections != 0 {
		t.Fatalf("the camera got %d connection(s): nothing may be sent to a device that is not allowed", cam.Connections)
	}
}

func TestACLAddressRuleAllows(t *testing.T) {
	i18n.Set("en")
	cam, cfg, db, _ := setup(t, 2)
	cfg.AllowedCameras = []string{"10.0.0.0/8", "127.0.0.1"}
	c := startCtl(t, cfg, db)
	c.ConnectIP(cam.Addr())
	s := waitFor(t, c, "ready", func(s app.Snapshot) bool { return s.State == app.StateReady })
	if s.Counts.Total != 4 {
		t.Fatalf("counts %+v", s.Counts)
	}
}

func TestACLSerialAndModelAreCheckedBeforeTheFileList(t *testing.T) {
	i18n.Set("en")
	cam, cfg, db, _ := setup(t, 2) // model ILCE-77M2, serial 0000000012345678
	cfg.AllowedCameras = []string{"serial:99999"}
	c := startCtl(t, cfg, db)
	c.ConnectIP(cam.Addr())
	waitWaiting(t, c, "serial number")
	time.Sleep(200 * time.Millisecond)
	if hasOp(cam, ptpip.OpGetObjectHandles) || hasOp(cam, ptpip.OpGetObjectInfo) || hasOp(cam, ptpip.OpGetObject) {
		t.Fatalf("files were listed or read from a camera that is not allowed: %v", cam.Ops)
	}

	// wrong model
	cam2, cfg2, db2, _ := setup(t, 1)
	cfg2.AllowedCameras = []string{"model:ILCE-7M4"}
	c2 := startCtl(t, cfg2, db2)
	c2.ConnectIP(cam2.Addr())
	waitWaiting(t, c2, "model")

	// right serial (leading zeros do not matter) and model
	cam3, cfg3, db3, _ := setup(t, 1)
	cfg3.AllowedCameras = []string{"serial:12345678", "model:ilce-77m2", "127.0.0.0/8"}
	c3 := startCtl(t, cfg3, db3)
	c3.ConnectIP(cam3.Addr())
	waitFor(t, c3, "ready", func(s app.Snapshot) bool { return s.State == app.StateReady })
}

func TestACLEmptyListRefusesPublicAddresses(t *testing.T) {
	i18n.Set("en")
	_, cfg, db, _ := setup(t, 1)
	c := startCtl(t, cfg, db)
	start := time.Now()
	c.ConnectIP("8.8.8.8")
	waitWaiting(t, c, "not a local network address")
	if time.Since(start) > 3*time.Second {
		t.Fatal("must be refused without trying to connect")
	}
	if err := c.CheckAddr("1.1.1.1"); err == nil {
		t.Fatal("CheckAddr must refuse a public address")
	}
	if err := c.CheckAddr("192.168.1.20"); err != nil {
		t.Fatal(err)
	}
}

func TestACLBrokenListRefusesEverything(t *testing.T) {
	i18n.Set("en")
	cam, cfg, db, _ := setup(t, 1)
	cfg.AllowedCameras = []string{"127.0.0.1", "typo-here"}
	c := startCtl(t, cfg, db)
	c.ConnectIP(cam.Addr())
	waitWaiting(t, c, "typo-here")
	time.Sleep(200 * time.Millisecond)
	if cam.Connections != 0 {
		t.Fatal("a list with an invalid entry must not allow anything")
	}
}

func TestACLFiltersAutomaticDiscovery(t *testing.T) {
	i18n.Set("en")
	cam, cfg, db, _ := setup(t, 1)
	cfg.AllowedCameras = []string{"192.168.77.0/24"}
	c := startCtl(t, cfg, db)
	app.InjectDiscovery(c, ssdp.Camera{IP: "127.0.0.1", FriendlyName: "Imposter", Manufacturer: "Sony", Model: "ILCE-77M2"})
	time.Sleep(400 * time.Millisecond)
	if cam.Connections != 0 {
		t.Fatal("a discovered camera that is not on the list must be ignored")
	}
	if st := c.Snapshot(); st.State != app.StateWaiting {
		t.Fatalf("state %v", st.State)
	}
}
