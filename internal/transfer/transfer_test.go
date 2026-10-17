package transfer

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestExpandPattern(t *testing.T) {
	tm := time.Date(2026, 3, 7, 10, 0, 0, 0, time.Local)
	cases := map[string]string{
		`{yyyy}\{yyyy}-{MM}-{dd}`: filepath.Join("2026", "2026-03-07"),
		`{yyyy}/{MM}`:             filepath.Join("2026", "03"),
		``:                        "",
		`{camera}\..\{dd}`:        filepath.Join("ILCE-77M2", "07"),
		`a:b`:                     "a_b",
	}
	for in, want := range cases {
		if got := ExpandPattern(in, tm, "ILCE-77M2"); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestKindOf(t *testing.T) {
	for n, k := range map[string]Kind{"DSC00001.JPG": KindJPEG, "DSC00001.ARW": KindRAW, "C0001.MP4": KindVideo, "00001.MTS": KindVideo, "x.XML": KindOther} {
		if KindOf(n) != k {
			t.Errorf("%s", n)
		}
	}
}

func TestRateMeter(t *testing.T) {
	m := &RateMeter{Window: 10 * time.Second}
	t0 := time.Now()
	if m.Rate() != 0 {
		t.Fatal("rate without samples")
	}
	// 3 MB/s for 20 s; only the last 10 s window counts
	for i := 0; i <= 20; i++ {
		m.Add(t0.Add(time.Duration(i)*time.Second), int64(i)*3_000_000)
	}
	if r := m.Rate(); r < 2.9e6 || r > 3.1e6 {
		t.Fatalf("rate %v", r)
	}
	if eta := m.ETA(30_000_000); eta < 9*time.Second || eta > 11*time.Second {
		t.Fatalf("eta %v", eta)
	}
	// speed drops to 1 MB/s: the window follows quickly
	n := int64(60_000_000)
	for i := 21; i <= 32; i++ {
		n += 1_000_000
		m.Add(t0.Add(time.Duration(i)*time.Second), n)
	}
	if r := m.Rate(); r < 0.9e6 || r > 1.2e6 {
		t.Fatalf("rate after slowdown %v", r)
	}
}

func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		"DSC00001.JPG":        "DSC00001.JPG",
		"../x.txt":            "x.txt",
		`..\..\x.txt`:         "x.txt",
		"a/b/../c.txt":        "c.txt",
		`C:\Windows\evil.exe`: "evil.exe",
		"/etc/passwd":         "passwd",
		"..":                  "",
		".":                   "",
		"":                    "",
		"   ":                 "",
		"name.":               "name",
		"nul\x00byte.txt":     "nul_byte.txt",
		"CON":                 "_CON",
		"con.txt":             "_con.txt",
		"aux.tar.gz":          "_aux.tar.gz",
		"COM1.txt":            "_COM1.txt",
		"COM10.txt":           "COM10.txt",
		"a<b>c:d|e?f*g.jpg":   "a_b_c_d_e_f_g.jpg",
		"Ünïcode-日本語.JPG":     "Ünïcode-日本語.JPG",
	}
	for in, want := range cases {
		if got := SafeFileName(in); got != want {
			t.Errorf("SafeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	long := SafeFileName(strings.Repeat("あ", 300) + ".ARW")
	if len(long) > 200 || !strings.HasSuffix(long, ".ARW") || !utf8.ValidString(long) {
		t.Errorf("long name: %d bytes %q", len(long), long)
	}
}

func TestWithin(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "data", "photos")
	for p, want := range map[string]bool{
		filepath.Join(root, "a.jpg"):              true,
		filepath.Join(root, "x", "y", "a.jpg"):    true,
		root:                                      false,
		filepath.Join(root, "..", "a.jpg"):        false,
		filepath.Join(root, "..", "photos2", "a"): false,
		filepath.Join(string(filepath.Separator), "etc", "passwd"): false,
	} {
		if got := within(root, p); got != want {
			t.Errorf("within(%q) = %v, want %v", p, got, want)
		}
	}
}
