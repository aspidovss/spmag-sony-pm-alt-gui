//go:build !windows

package i18n

import "os"

// systemLocale returns the first locale variable that is set.
func systemLocale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		if v := os.Getenv(k); v != "" && v != "C" && v != "POSIX" {
			return v
		}
	}
	return "en"
}
