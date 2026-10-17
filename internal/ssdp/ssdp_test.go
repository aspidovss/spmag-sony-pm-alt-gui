package ssdp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"ILCE-77M2":             "ILCE-77M2",
		"evil\nSaved x → y":     "evil Saved x → y",
		"a\r\nb\tc\x00d\x1b[2J": "a  b c d [2J",
		"  padded  ":            "padded",
		"line sep":              "line sep",
		"":                      "",
	} {
		if got := CleanText(in); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("あ", 500)
	if n := len([]rune(CleanText(long))); n != 100 {
		t.Errorf("default cap: %d", n)
	}
	if n := len([]rune(CleanTextN(long, 255))); n != 255 {
		t.Errorf("custom cap: %d", n)
	}
}

func TestDescriptionIsCleaned(t *testing.T) {
	xml := "<root><device><friendlyName>Cam\nera</friendlyName><manufacturer>Sony\r\nCorp</manufacturer><modelName>ILCE\t1</modelName></device></root>"
	c, err := ParseDescription(strings.NewReader(xml))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(c.FriendlyName+c.Manufacturer+c.Model, "\n\r\t\x00") {
		t.Fatalf("control characters survived: %+v", c)
	}
	// the XML parser itself refuses a NUL byte
	if _, err := ParseDescription(strings.NewReader("<root><device><friendlyName>a\x00b</friendlyName></device></root>")); err == nil {
		t.Fatal("NUL in XML must be an error")
	}
}

func TestFetchDoesNotFollowRedirects(t *testing.T) {
	hit := make(chan struct{}, 1)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		w.Write([]byte("<root/>"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	if _, err := FetchRaw(context.Background(), srv.URL); err == nil {
		t.Fatal("a redirect must be an error")
	}
	select {
	case <-hit:
		t.Fatal("the redirect target was contacted")
	case <-time.After(200 * time.Millisecond):
	}
}
