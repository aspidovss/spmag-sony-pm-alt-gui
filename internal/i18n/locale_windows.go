//go:build windows

package i18n

import "golang.org/x/sys/windows"

var getUserDefaultUILanguage = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")

// systemLocale returns a tag for the Windows display language.
func systemLocale() string {
	id, _, _ := getUserDefaultUILanguage.Call()
	switch id & 0x3ff { // primary language identifier
	case 0x04:
		return "zh"
	case 0x0a:
		return "es"
	case 0x0c:
		return "fr"
	case 0x07:
		return "de"
	case 0x16:
		return "pt"
	case 0x19:
		return "ru"
	case 0x11:
		return "ja"
	case 0x12:
		return "ko"
	case 0x10:
		return "it"
	}
	return "en"
}
