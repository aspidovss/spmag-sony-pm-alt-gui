package i18n

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var verbRE = regexp.MustCompile(`%(?:\[\d+\])?[-+# 0-9.]*[a-zA-Z%]`)

func verbs(s string) []string {
	var out []string
	for _, v := range verbRE.FindAllString(s, -1) {
		v = regexp.MustCompile(`\[\d+\]`).ReplaceAllString(v, "")
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func TestAllLanguagesComplete(t *testing.T) {
	en := tables["en"]
	if len(en) < 100 {
		t.Fatalf("english table too small: %d", len(en))
	}
	for _, l := range Languages {
		tab := tables[l.Code]
		if tab == nil {
			t.Fatalf("language %s has no table", l.Code)
		}
		for k, ev := range en {
			v, ok := tab[k]
			if !ok || strings.TrimSpace(v) == "" {
				t.Errorf("%s: missing %q", l.Code, k)
				continue
			}
			if strings.Join(verbs(ev), "") != strings.Join(verbs(v), "") {
				t.Errorf("%s: %q format verbs differ: %q vs %q", l.Code, k, ev, v)
				continue
			}
			// format with dummy arguments in English order
			var args []any
			for _, vb := range verbRE.FindAllString(ev, -1) {
				switch vb[len(vb)-1] {
				case 'd':
					args = append(args, 7)
				case '%':
				default:
					args = append(args, "x")
				}
			}
			if out := fmt.Sprintf(v, args...); strings.Contains(out, "%!") {
				t.Errorf("%s: %q formats badly: %q -> %q", l.Code, k, v, out)
			}
			if strings.Contains(strings.ReplaceAll(v, "&&", ""), "&") {
				t.Errorf("%s: %q has a single & (use && for a literal ampersand)", l.Code, k)
			}
		}
		for k := range tab {
			if _, ok := en[k]; !ok {
				t.Errorf("%s: unknown key %q", l.Code, k)
			}
		}
	}
}

func TestNormalizeAndSet(t *testing.T) {
	cases := map[string]string{"ru-RU": "ru", "zh_CN.UTF-8": "zh", "pt-BR": "pt", "DE": "de", "xx": "", "": ""}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q)=%q want %q", in, got, want)
		}
	}
	defer Set("en")
	if Set("ja") != "ja" || Current() != "ja" {
		t.Fatal("Set(ja)")
	}
	if got := T("btn.cancel"); got != "キャンセル" {
		t.Errorf("got %q", got)
	}
	if got := T("no.such.key"); got != "no.such.key" {
		t.Errorf("fallback: %q", got)
	}
}
