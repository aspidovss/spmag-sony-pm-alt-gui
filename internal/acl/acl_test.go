package acl

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/i18n"
)

func mustParse(t *testing.T, entries ...string) *Rules {
	t.Helper()
	r, err := Parse(entries)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSplit(t *testing.T) {
	got := Split("192.168.1.5, 10.0.0.0/8\n# a comment\nserial:ABC  model:ILCA-77M2 # trailing\n\n192.168.1.5;;")
	want := []string{"192.168.1.5", "10.0.0.0/8", "serial:ABC", "model:ILCA-77M2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestParseRejectsTypos(t *testing.T) {
	i18n.Set("en")
	for _, bad := range []string{"192.168.1", "192.168.1.0/33", "256.1.1.1", "serial:", "model: ", "camera.local", "192.168.1.0/24/1", "1.2.3.4-1.2.3.9"} {
		if _, err := Parse([]string{bad}); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	if _, err := Parse([]string{"192.168.1.5", "fe80::1", "10.0.0.0/8", "::1/128", "SERIAL:1", "Model:X"}); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyListAllowsOnlyLocalAddresses(t *testing.T) {
	i18n.Set("en")
	r := mustParse(t)
	for addr, ok := range map[string]bool{
		"192.168.0.10": true, "10.1.2.3": true, "172.16.0.9": true, "172.32.0.1": false,
		"169.254.1.1": true, "127.0.0.1": true, "127.0.0.1:15740": true, "fe80::1": true, "fd00::5": true, "[fd00::5]:15740": true,
		"8.8.8.8": false, "1.1.1.1:15740": false, "2001:db8::1": false,
		"::ffff:192.168.0.10": true, "::ffff:8.8.8.8": false,
	} {
		err := r.AllowAddr(addr, true)
		if (err == nil) != ok {
			t.Errorf("%s: err=%v, want allowed=%v", addr, err, ok)
		}
		if err != nil && !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s: %v is not ErrNotAllowed", addr, err)
		}
	}
	for _, bad := range []string{"", "camera.local", "not an ip", "999.1.1.1"} {
		if err := r.AllowAddr(bad, true); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestDiscoveredCameraMayHaveAnyAddress(t *testing.T) {
	i18n.Set("en")
	// announced on the local segment by SSDP: no private-address requirement ...
	if err := mustParse(t).AllowAddr("203.0.113.5", false); err != nil {
		t.Fatal(err)
	}
	// ... but a list is still a list
	if err := mustParse(t, "192.168.1.0/24").AllowAddr("203.0.113.5", false); err == nil {
		t.Fatal("the list must apply to discovered cameras too")
	}
	if err := mustParse(t).AllowAddr("203.0.113.5", true); err == nil {
		t.Fatal("a typed-in public address must be refused")
	}
}

func TestAddressRules(t *testing.T) {
	i18n.Set("en")
	r := mustParse(t, "192.168.1.50", "10.0.0.0/24", "8.8.8.8")
	for addr, ok := range map[string]bool{
		"192.168.1.50": true, "192.168.1.51": false, "10.0.0.7": true, "10.0.1.7": false,
		"8.8.8.8":            true, // a public address is allowed when it is listed explicitly
		"192.168.1.50:15740": true, "::ffff:192.168.1.50": true, "127.0.0.1": false,
	} {
		if err := r.AllowAddr(addr, true); (err == nil) != ok {
			t.Errorf("%s: err=%v, want allowed=%v", addr, err, ok)
		}
	}
}

func TestDeviceRules(t *testing.T) {
	i18n.Set("en")
	any := mustParse(t, "192.168.1.0/24")
	if err := any.AllowDevice("ILCA-77M2", "123"); err != nil {
		t.Fatal(err)
	}
	r := mustParse(t, "serial:0012345", "serial:ab 77", "model:ilca-77m2")
	for _, c := range []struct {
		model, serial string
		ok            bool
	}{
		{"ILCA-77M2", "12345", true},
		{"ILCA-77M2", "0000000012345", true},
		{" ilca-77m2 ", "AB77", true},
		{"ILCA-77M2", "99999", false}, // right model, wrong serial
		{"ILCE-7M3", "12345", false},  // right serial, wrong model
		{"ILCA-77M2", "", false},
	} {
		err := r.AllowDevice(c.model, c.serial)
		if (err == nil) != c.ok {
			t.Errorf("%+v: err=%v", c, err)
		}
		if err != nil && !errors.Is(err, ErrNotAllowed) {
			t.Errorf("not ErrNotAllowed: %v", err)
		}
	}
	if (&Rules{}).Empty() != true || r.Empty() {
		t.Error("Empty()")
	}
}

func TestCheckFailsClosedOnBadEntry(t *testing.T) {
	i18n.Set("en")
	if err := Check([]string{"192.168.1.5", "oops"}, "192.168.1.5", true); err == nil {
		t.Fatal("a list with a typo must not allow anything")
	}
}
