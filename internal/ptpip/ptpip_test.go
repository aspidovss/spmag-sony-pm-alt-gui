package ptpip_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/fakecam"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ptpip"
)

func TestParseTime(t *testing.T) {
	for _, s := range []string{"20150725T174331", "20150725T174331.0", "20150725T174331+0300", "20150725T174331Z"} {
		tm, ok := ptpip.ParsePTPTime(s)
		if !ok || tm.Year() != 2015 || tm.Minute() != 43 {
			t.Errorf("%s -> %v %v", s, tm, ok)
		}
	}
}

func TestSessionAgainstFakeCamera(t *testing.T) {
	cam := fakecam.New("ILCE-77M2", 3)
	guid := [16]byte{8: 0xff, 9: 0xff, 10: 1, 11: 2, 12: 3, 13: 4, 14: 5, 15: 6}
	cam.AllowedGUID[guid] = true
	if err := cam.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer cam.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// wrong GUID must be refused
	if _, err := ptpip.Dial(ctx, cam.Addr(), ptpip.Options{HostName: "TEST"}); err != ptpip.ErrNotPaired {
		t.Fatalf("expected ErrNotPaired, got %v", err)
	}

	c, err := ptpip.Dial(ctx, cam.Addr(), ptpip.Options{GUID: guid, HostName: "MY-PC"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.CameraName != "ILCE-77M2" || cam.LastName != "MY-PC" {
		t.Fatalf("names: %q %q", c.CameraName, cam.LastName)
	}
	if err := c.OpenSession(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.SonyBeginTransfer(ctx); err != nil {
		t.Fatal(err)
	}
	ids, err := c.GetStorageIDs(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	root, err := c.GetObjectHandles(ctx, ids[0], 0, 0xFFFFFFFF)
	if err != nil || len(root) != 1 {
		t.Fatal(root, err)
	}
	files, err := c.GetObjectHandles(ctx, ids[0], 0, root[0])
	if err != nil || len(files) != 6 {
		t.Fatal(files, err)
	}
	oi, err := c.GetObjectInfo(ctx, files[1])
	if err != nil {
		t.Fatal(err)
	}
	if oi.Filename != cam.Files[1].Name || int(oi.CompressedSize) != len(cam.Files[1].Data) {
		t.Fatalf("objectinfo %+v", oi)
	}
	var buf bytes.Buffer
	var last int64
	if err := c.GetObject(ctx, files[1], &buf, func(d, total int64) { last = d }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), cam.Files[1].Data) || last != int64(buf.Len()) {
		t.Fatal("data mismatch")
	}
	if err := c.SonyEndTransfer(ctx); err != nil {
		t.Fatal(err)
	}
	if got := cam.SonyStates; len(got) != 3 || got[0] != 2 || got[1] != 4 || got[2] != 5 {
		t.Fatalf("sony states %v", got)
	}
}
