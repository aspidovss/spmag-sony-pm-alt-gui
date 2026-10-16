package capture_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/capture"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/fakecam"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ptpip"
)

func TestCaptureMasksIdentity(t *testing.T) {
	cam := fakecam.New("ILCA-77M2", 2)
	cam.Serial = "00000000987654321"
	cfg := config.Defaults()
	cfg.AcceptPMHPairing = false
	cam.AllowedGUID[cfg.OwnGUID()] = true
	if err := cam.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer cam.Stop()
	loc, err := cam.StartHTTP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := t.TempDir()
	res, err := capture.Run(ctx, cam.Addr(), cam.NotifyMessage(loc), cfg, out, false, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dir != filepath.Join(out, "ILCA-77M2") || len(res.Files) != 8 {
		t.Fatalf("%+v", res)
	}
	read := func(n string) []byte {
		b, err := os.ReadFile(filepath.Join(res.Dir, n))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	d, err := ptpip.ParseDeviceInfo(read(capture.FileDeviceInfo))
	if err != nil || d.SerialNumber != "00000000000000000" {
		t.Fatalf("serial not masked: %q %v", d.SerialNumber, err)
	}
	ack := read(capture.FileInitAck)
	if !bytes.Equal(ack[14:20], make([]byte, 6)) {
		t.Fatalf("camera MAC not masked: % x", ack[4:20])
	}
	for _, n := range []string{capture.FileDescription, capture.FileNotify} {
		b := read(n)
		if bytes.Contains(b, []byte("020000000001")) || bytes.Contains(b, []byte("127.0.0.1")) {
			t.Fatalf("%s not masked:\n%s", n, b)
		}
	}
	oi, err := ptpip.ParseObjectInfo(read(capture.FileObjectInfo))
	if err != nil || oi.Filename != "DSC01000.JPG" {
		t.Fatalf("%+v %v", oi, err)
	}
}
