package ssdp

import (
	"bytes"
	"testing"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/fixtures"
)

func TestSyntheticMessages(t *testing.T) {
	alive := "NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nLOCATION: http://192.168.0.100:1900/DeviceDescription.xml\r\n" +
		"NT: urn:microsoft-com:service:MtpNullService:1\r\nNTS: ssdp:alive\r\n" +
		"USN: uuid:00000000-0001-0010-8000-000000000000::urn:microsoft-com:service:MtpNullService:1\r\n\r\n"
	m, err := ParseMessage([]byte(alive))
	if err != nil || !m.IsCameraAlive() || m.Headers["LOCATION"] != "http://192.168.0.100:1900/DeviceDescription.xml" {
		t.Fatalf("%v %+v", err, m)
	}
	bye, _ := ParseMessage([]byte("NOTIFY * HTTP/1.1\r\nNT: urn:microsoft-com:service:MtpNullService:1\r\nNTS: ssdp:byebye\r\n\r\n"))
	if bye.IsCameraAlive() {
		t.Fatal("byebye treated as alive")
	}
	other, _ := ParseMessage([]byte("NOTIFY * HTTP/1.1\r\nNT: urn:schemas-upnp-org:device:MediaRenderer:1\r\nNTS: ssdp:alive\r\nLOCATION: http://x/\r\n\r\n"))
	if other.IsCameraAlive() {
		t.Fatal("media renderer treated as camera")
	}
}

// Real camera announcements and descriptions recorded with "wpr capture".
func TestCapturedDiscovery(t *testing.T) {
	for _, c := range fixtures.All(t) {
		t.Run(c.Model, func(t *testing.T) {
			if n := c.Read("notify.txt"); n != nil {
				m, err := ParseMessage(n)
				if err != nil || !m.IsCameraAlive() || m.Headers["LOCATION"] == "" {
					t.Errorf("notify not recognised: %v %+v", err, m)
				}
			} else {
				t.Log("notify.txt not captured")
			}
			d := c.Read("description.xml")
			if d == nil {
				t.Skip("description.xml not captured")
			}
			cam, err := ParseDescription(bytes.NewReader(d))
			if err != nil {
				t.Fatal(err)
			}
			if cam.Manufacturer != "Sony Corporation" || cam.Model == "" || cam.UDN == "" {
				t.Fatalf("%+v", cam)
			}
		})
	}
}
