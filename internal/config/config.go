// Package config stores user settings as JSON under the user's AppData.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/pairing"
)

// AppName is used for folders, registry values and window titles.
const AppName = "SPMAG Wifi Photo Receiver"

// legacyDirName is the data folder used before the rename to SPMAG.
const legacyDirName = "WiFi Photo Receiver"

// RepoURL is the project's home page.
const RepoURL = "https://github.com/aspidovss/spmag-sony-pm-alt-gui"

// Settings are the user-configurable options.
type Settings struct {
	DestDir       string `json:"dest_dir"`
	FolderPattern string `json:"folder_pattern"` // e.g. {yyyy}\{yyyy}-{MM}-{dd}; "" = flat

	IncludeJPEG  bool `json:"include_jpeg"`
	IncludeRAW   bool `json:"include_raw"`
	IncludeVideo bool `json:"include_video"`
	IncludeOther bool `json:"include_other"`

	AutoDownload     bool `json:"auto_download"`     // download missing files as soon as the camera connects
	FinishAfterAuto  bool `json:"finish_after_auto"` // tell the camera "done" after an automatic transfer
	MatchExisting    bool `json:"match_existing"`    // treat files already in DestDir (same name+size) as received
	StartWithWindows bool `json:"start_with_windows"`
	StartMinimized   bool `json:"start_minimized"`
	Notifications    bool `json:"notifications"`

	GUID             string `json:"guid"`               // our own PTP/IP GUID
	AcceptPMHPairing bool   `json:"accept_pmh_pairing"` // also try PlayMemories-style (MAC) GUIDs
	HostName         string `json:"host_name"`          // name shown on the camera; "" = computer name
	ManualIP         string `json:"manual_ip"`
	Language         string `json:"language"` // "" = system language, else en, ru, de, ...
}

// Defaults returns the default settings.
func Defaults() Settings {
	home, _ := os.UserHomeDir()
	return Settings{
		DestDir:          filepath.Join(home, "Pictures", "Camera Wi-Fi Import"),
		FolderPattern:    `{yyyy}` + string(filepath.Separator) + `{yyyy}-{MM}-{dd}`,
		IncludeJPEG:      true,
		IncludeRAW:       true,
		IncludeVideo:     true,
		IncludeOther:     false,
		AutoDownload:     true,
		FinishAfterAuto:  true,
		MatchExisting:    true,
		StartMinimized:   true,
		Notifications:    true,
		AcceptPMHPairing: true,
		GUID:             pairing.NewGUID().String(),
	}
}

// DirEnv overrides the data folder (e.g. for a demo or a portable setup).
const DirEnv = "SPMAG_CONFIG_DIR"

// Dir returns the per-user data folder (created on demand).
func Dir() string {
	if d := os.Getenv(DirEnv); d != "" {
		_ = os.MkdirAll(d, 0o755)
		return d
	}
	base, err := os.UserConfigDir() // %APPDATA% on Windows
	if err != nil || base == "" {
		base = os.TempDir()
	}
	name := AppName
	if runtime.GOOS != "windows" {
		name = "spmag-wifi-photo-receiver"
	}
	d := filepath.Join(base, name)
	if runtime.GOOS == "windows" {
		// Keep settings, pairing ID and the received-files database from
		// versions named "WiFi Photo Receiver".
		old := filepath.Join(base, legacyDirName)
		if _, err := os.Stat(d); os.IsNotExist(err) {
			if st, err := os.Stat(old); err == nil && st.IsDir() {
				_ = os.Rename(old, d)
			}
		}
	}
	_ = os.MkdirAll(d, 0o755)
	return d
}

func path() string { return filepath.Join(Dir(), "settings.json") }

// Load reads settings, filling in defaults; a fresh GUID is saved on first run.
func Load() (Settings, error) {
	s := Defaults()
	if os.Getenv(DirEnv) != "" {
		// separate data folder: keep its photos next to it by default
		s.DestDir = filepath.Join(Dir(), "photos")
	}
	b, err := os.ReadFile(path())
	if err != nil {
		if os.IsNotExist(err) {
			return s, Save(s)
		}
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Defaults(), err
	}
	if _, err := pairing.ParseGUID(s.GUID); err != nil {
		s.GUID = pairing.NewGUID().String()
		_ = Save(s)
	}
	return s, nil
}

// Save writes settings atomically.
func Save(s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path())
}

// OwnGUID returns the parsed GUID.
func (s Settings) OwnGUID() pairing.GUID {
	g, err := pairing.ParseGUID(s.GUID)
	if err != nil {
		return pairing.GUID{}
	}
	return g
}

// EffectiveHostName returns the name announced to the camera.
func (s Settings) EffectiveHostName() string {
	if s.HostName != "" {
		return s.HostName
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "PC"
	}
	return h
}
