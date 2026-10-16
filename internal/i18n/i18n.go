// Package i18n translates the user interface (windows, dialogs, tray and the
// status texts shown in them). Log files and command-line output stay in
// English. The translations were produced with AI assistance; corrections are
// welcome as pull requests (see README, "Languages").
package i18n

import (
	"fmt"
	"strings"
	"sync"
)

// Language is an interface language offered in the settings.
type Language struct {
	Code string `json:"code"` // "en", "ru", ...
	Name string `json:"name"` // shown in its own language
}

// Languages lists the supported interface languages. English comes first and
// is the fallback for any missing text.
var Languages = []Language{
	{"en", "English"},
	{"zh", "简体中文"},
	{"es", "Español"},
	{"fr", "Français"},
	{"de", "Deutsch"},
	{"pt", "Português (Brasil)"},
	{"ru", "Русский"},
	{"ja", "日本語"},
	{"ko", "한국어"},
	{"it", "Italiano"},
}

var (
	mu     sync.RWMutex
	tables = map[string]map[string]string{}
	cur    = "en"
)

func register(code string, m map[string]string) { tables[code] = m }

// Normalize maps a locale tag such as "ru-RU", "zh_CN.UTF-8" or "pt-BR" to one
// of the supported codes. It returns "" if the language is not supported.
func Normalize(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_.@"); i >= 0 {
		tag = tag[:i]
	}
	for _, l := range Languages {
		if l.Code == tag {
			return tag
		}
	}
	return ""
}

// Set selects the interface language. An empty or unknown code selects the
// system language (English when that is not supported). It returns the code
// that is now active.
func Set(code string) string {
	c := Normalize(code)
	if c == "" {
		c = Normalize(systemLocale())
	}
	if c == "" {
		c = "en"
	}
	mu.Lock()
	cur = c
	mu.Unlock()
	return c
}

// Current returns the active language code.
func Current() string {
	mu.RLock()
	defer mu.RUnlock()
	return cur
}

// T returns the text for key in the active language, formatted with args.
// Missing translations fall back to English, then to the key itself.
func T(key string, args ...any) string {
	mu.RLock()
	c := cur
	mu.RUnlock()
	s, ok := tables[c][key]
	if !ok {
		if s, ok = tables["en"][key]; !ok {
			s = key
		}
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Strings returns every text of the active language (English fills any gap).
// The web interface downloads it to translate itself.
func Strings() map[string]string {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]string, len(tables["en"]))
	for k, v := range tables["en"] {
		out[k] = v
	}
	for k, v := range tables[cur] {
		out[k] = v
	}
	return out
}
