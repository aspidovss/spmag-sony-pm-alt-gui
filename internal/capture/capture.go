// Package capture records a camera's raw protocol answers as test fixtures
// (testdata/<model>/). Serial numbers, the camera MAC address and IP
// addresses are masked unless raw output is requested.
package capture

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ptpip"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ssdp"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/store"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/transfer"
)

// MaskedIP replaces the camera's IP address in captured text.
const MaskedIP = "192.168.0.100"

// Fixture file names inside testdata/<model>/.
const (
	FileDeviceInfo  = "deviceinfo.bin"
	FileInitAck     = "initcmdack.bin"
	FileStorageInfo = "storageinfo.bin"
	FileFolderInfo  = "objectinfo_folder.bin"
	FileObjectInfo  = "objectinfo_file.bin"
	FileDescription = "description.xml"
	FileNotify      = "notify.txt"
	FileReadme      = "README.txt"
)

// Result describes what was written.
type Result struct {
	Dir   string
	Model string
	Files []string
}

// Run connects to the camera at ip and writes fixtures under outRoot.
// notify is the raw SSDP announcement (may be nil).
func Run(ctx context.Context, ip string, notify []byte, cfg config.Settings, outRoot string, raw bool, logf func(string, ...any)) (*Result, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	host := ip
	if i := strings.LastIndexByte(ip, ':'); i > 0 {
		host = ip[:i]
	}
	location := "http://" + host + ":1900/DeviceDescription.xml"
	if notify != nil {
		if m, err := ssdp.ParseMessage(notify); err == nil && m.Headers["LOCATION"] != "" {
			location = m.Headers["LOCATION"]
		}
	}
	desc, derr := ssdp.FetchRaw(ctx, location)
	if derr != nil {
		logf("device description not captured: %v", derr)
	}

	tmpDB, _ := store.Open(filepath.Join(os.TempDir(), fmt.Sprintf("wpr-capture-%d.json", time.Now().UnixNano())))
	s, err := transfer.Connect(ctx, ip, cfg, tmpDB, logf)
	if err != nil {
		return nil, err
	}
	defer s.End(false)
	c := s.Client

	files := map[string][]byte{}
	files[FileInitAck] = c.InitAckRaw
	di, err := c.GetData(ctx, ptpip.OpGetDeviceInfo)
	if err != nil {
		return nil, fmt.Errorf("DeviceInfo: %w", err)
	}
	files[FileDeviceInfo] = di
	info, err := ptpip.ParseDeviceInfo(di)
	if err != nil {
		return nil, err
	}
	ids, err := c.GetStorageIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if id&0xFFFF == 0 {
			continue
		}
		if b, err := c.GetData(ctx, ptpip.OpGetStorageInfo, id); err == nil {
			files[FileStorageInfo] = b
		}
		root, err := c.GetObjectHandles(ctx, id, 0, 0xFFFFFFFF)
		if err != nil {
			return nil, err
		}
		findFile(ctx, c, id, root, files, 0)
		break
	}
	if desc != nil {
		files[FileDescription] = desc
	}
	if notify != nil {
		files[FileNotify] = notify
	}

	model := info.Model
	if model == "" {
		model = "camera"
	}
	if !raw {
		mask(files, info, c.InitAckRaw, host)
	}
	dir := filepath.Join(outRoot, safeName(model))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	res := &Result{Dir: dir, Model: model}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return nil, err
		}
		res.Files = append(res.Files, name)
	}
	note := fmt.Sprintf("Captured %s from %s %s, firmware %s, with wpr capture.\n"+
		"Serial number, camera MAC address and IP address are masked: %v.\n"+
		"These are the camera's own protocol answers, used as test fixtures.\n",
		time.Now().Format("2006-01-02"), info.Manufacturer, info.Model, info.DeviceVersion, !raw)
	_ = os.WriteFile(filepath.Join(dir, FileReadme), []byte(note), 0o644)
	res.Files = append(res.Files, FileReadme)
	return res, nil
}

// findFile stores the ObjectInfo of the first folder and the first file.
func findFile(ctx context.Context, c *ptpip.Client, storage uint32, handles []uint32, files map[string][]byte, depth int) bool {
	if depth > 6 {
		return false
	}
	for _, h := range handles {
		b, err := c.GetData(ctx, ptpip.OpGetObjectInfo, h)
		if err != nil {
			continue
		}
		oi, err := ptpip.ParseObjectInfo(b)
		if err != nil {
			continue
		}
		if oi.IsFolder() {
			if _, ok := files[FileFolderInfo]; !ok {
				files[FileFolderInfo] = b
			}
			sub, err := c.GetObjectHandles(ctx, storage, 0, h)
			if err == nil && findFile(ctx, c, storage, sub, files, depth+1) {
				return true
			}
			continue
		}
		files[FileObjectInfo] = b
		return true
	}
	return false
}

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, x := range u {
		b = append(b, byte(x), byte(x>>8))
	}
	return b
}

func zeros(s string) string { return strings.Repeat("0", len(s)) }

// mask removes identifying data in place.
func mask(files map[string][]byte, info *ptpip.DeviceInfo, initAck []byte, ip string) {
	serial := strings.TrimSpace(info.SerialNumber)
	// camera MAC: init ack payload = conn(4) + GUID(16) + name; GUID = 8x00 ff ff MAC(6)
	var mac []byte
	if len(initAck) >= 20 && initAck[12] == 0xff && initAck[13] == 0xff {
		mac = append([]byte(nil), initAck[14:20]...)
	}
	for name, b := range files {
		if serial != "" && strings.Trim(serial, "0") != "" {
			b = bytes.ReplaceAll(b, utf16le(serial), utf16le(zeros(serial)))
			b = bytes.ReplaceAll(b, []byte(serial), []byte(zeros(serial)))
		}
		if mac != nil {
			b = bytes.ReplaceAll(b, mac, make([]byte, 6))
			for _, h := range []string{hex.EncodeToString(mac), strings.ToUpper(hex.EncodeToString(mac))} {
				b = bytes.ReplaceAll(b, []byte(h), []byte("000000000000"))
			}
		}
		if ip != "" && ip != MaskedIP {
			b = bytes.ReplaceAll(b, []byte(ip), []byte(MaskedIP))
		}
		files[name] = b
	}
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

// LocationHost returns the host of a LOCATION URL (helper for callers).
func LocationHost(loc string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
