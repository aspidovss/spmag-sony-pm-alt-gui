//go:build linux

package pairing

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestSgIoHdrSize(t *testing.T) {
	want := uintptr(64)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		want = 88
	}
	if got := unsafe.Sizeof(sgIoHdr{}); got != want {
		t.Fatalf("sg_io_hdr size %d, want %d", got, want)
	}
}

func TestListDrives(t *testing.T) {
	root := t.TempDir()
	usbDev := filepath.Join(root, "devices", "pci0", "usb1", "1-1", "host6", "target6:0:0", "6:0:0:0")
	diskDev := filepath.Join(root, "devices", "pci0", "ata1", "host0", "target0:0:0", "0:0:0:0")
	for _, d := range []string{usbDev, diskDev} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name, v string) { _ = os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644) }
	write(usbDev, "vendor", "Sony    ")
	write(usbDev, "model", "ILCA-77M2")
	write(diskDev, "vendor", "ATA")
	write(diskDev, "model", "Samsung SSD")
	sys := filepath.Join(root, "class", "scsi_generic")
	for name, dev := range map[string]string{"sg0": diskDev, "sg1": usbDev} {
		if err := os.MkdirAll(filepath.Join(sys, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dev, filepath.Join(sys, name, "device")); err != nil {
			t.Fatal(err)
		}
	}
	ds, err := listDrives(sys, "/dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].Path != "/dev/sg1" || !ds[0].IsSony() || ds[0].Product != "ILCA-77M2" {
		t.Fatalf("unexpected drives: %+v", ds)
	}
}
