package ptpip_test

import (
	"strings"
	"testing"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/fixtures"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ptpip"
)

// Parse real camera answers recorded with "wpr capture".
func TestCapturedDatasets(t *testing.T) {
	for _, c := range fixtures.All(t) {
		t.Run(c.Model, func(t *testing.T) {
			di := c.Read("deviceinfo.bin")
			if di == nil {
				t.Fatal("deviceinfo.bin missing")
			}
			d, err := ptpip.ParseDeviceInfo(di)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(d.Manufacturer, "Sony") || d.Model == "" {
				t.Fatalf("manufacturer %q model %q", d.Manufacturer, d.Model)
			}
			for _, op := range []uint16{ptpip.OpGetObjectHandles, ptpip.OpGetObjectInfo, ptpip.OpGetObject, ptpip.OpSonySDIOConnect, ptpip.OpSonyGetExtDevInfo} {
				if !d.Supports(op) {
					t.Errorf("operation 0x%04X not listed", op)
				}
			}
			if strings.Trim(d.SerialNumber, "0") != "" {
				t.Errorf("serial number is not masked: %q", d.SerialNumber)
			}
			if string(d.Encode()) != string(di) {
				t.Error("DeviceInfo does not round-trip through Encode")
			}
			t.Logf("%s %s firmware %s, %d operations", d.Manufacturer, d.Model, d.DeviceVersion, len(d.Operations))

			if ack := c.Read("initcmdack.bin"); ack != nil {
				if len(ack) < 22 {
					t.Fatal("init ack too short")
				}
				name, _ := ptpip.DecodeUTF16Z(ack[20:])
				if name != d.Model {
					t.Errorf("camera name in init ack %q != model %q", name, d.Model)
				}
			}
			if b := c.Read("storageinfo.bin"); b != nil {
				si, err := ptpip.ParseStorageInfo(b)
				if err != nil || si.MaxCapacity == 0 {
					t.Errorf("storage info: %v %+v", err, si)
				}
			}
			if b := c.Read("objectinfo_folder.bin"); b != nil {
				oi, err := ptpip.ParseObjectInfo(b)
				if err != nil || !oi.IsFolder() {
					t.Errorf("folder info: %v %+v", err, oi)
				}
				if string(oi.Encode()) != string(b) {
					t.Error("folder ObjectInfo does not round-trip")
				}
			}
			if b := c.Read("objectinfo_file.bin"); b != nil {
				oi, err := ptpip.ParseObjectInfo(b)
				if err != nil || oi.IsFolder() || oi.Filename == "" || oi.CompressedSize == 0 {
					t.Errorf("file info: %v %+v", err, oi)
				}
				// Non-image files (e.g. Sony's REGISTER.URL) may carry no dates.
				for _, ds := range []string{oi.CaptureDate, oi.ModificationDate} {
					if _, ok := ptpip.ParsePTPTime(ds); ds != "" && !ok {
						t.Errorf("date %q not parsed", ds)
					}
				}
				if string(oi.Encode()) != string(b) {
					t.Error("file ObjectInfo does not round-trip")
				}
				t.Logf("file %s, %d bytes, format 0x%04X, captured %q, modified %q", oi.Filename, oi.CompressedSize, oi.ObjectFormat, oi.CaptureDate, oi.ModificationDate)
			}
		})
	}
}
