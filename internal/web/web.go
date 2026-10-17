// Package web is the browser interface of SPMAG Wifi Photo Receiver, built
// into "wpr watch -web". It is meant for Linux servers, NAS boxes and Docker,
// where no desktop is available. The page is a small single-page app embedded
// in the binary; it polls a JSON API served by this package.
package web

import (
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/acl"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/app"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/i18n"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/pairing"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/transfer"
)

//go:embed static
var staticFS embed.FS

// Options configure the web interface.
type Options struct {
	Ctl     *app.Controller
	Logs    *LogBuffer
	Version string
	// User and Password enable HTTP Basic authentication. An empty User accepts
	// any user name. An empty Password means no authentication, which is only
	// acceptable on a loopback address.
	User     string
	Password string
	// LoopbackOnly restricts the Host header to localhost / loopback IPs, which
	// defeats DNS-rebinding attacks against an unauthenticated local server.
	LoopbackOnly bool
	// Manager, when set, lets the settings page change the web options
	// (enable, port, login, password) while the program runs.
	Manager *Manager
	// Quit, when set, adds an "Exit" button to the page that stops the program
	// (used by the AppImage, which has no window or tray icon of its own).
	Quit func()
}

// LogBuffer keeps the most recent log lines for the Log tab.
type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	first int // absolute index of lines[0]
}

const (
	maxLogLines = 1000
	maxLogLine  = 2000 // bytes; log lines can contain text from the network
)

// Append adds a line.
func (b *LogBuffer) Append(line string) {
	if len(line) > maxLogLine {
		line = strings.ToValidUTF8(line[:maxLogLine], "") + "…"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > maxLogLines {
		drop := len(b.lines) - maxLogLines
		b.lines = append([]string(nil), b.lines[drop:]...)
		b.first += drop
	}
}

// Since returns the lines from absolute index n on, and the index to ask for next.
func (b *LogBuffer) Since(n int) ([]string, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n < b.first {
		n = b.first
	}
	i := n - b.first
	if i > len(b.lines) {
		i = len(b.lines)
	}
	return append([]string(nil), b.lines[i:]...), b.first + len(b.lines)
}

// IsLoopbackAddr reports whether a listen address ("127.0.0.1:8080",
// "localhost:80", "[::1]:80") only accepts connections from this machine.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

type server struct {
	lim      *authLimiter
	o        Options
	pwHash   [32]byte
	userHash [32]byte
	usePW    bool
	static   http.Handler
}

// Handler returns the HTTP handler of the web interface.
func Handler(o Options) http.Handler {
	if o.Logs == nil {
		o.Logs = &LogBuffer{}
	}
	sub, _ := fs.Sub(staticFS, "static")
	s := &server{o: o, static: http.FileServer(http.FS(sub)), lim: newAuthLimiter()}
	if o.Password != "" {
		s.usePW = true
		s.pwHash = sha256.Sum256([]byte(o.Password))
		s.userHash = sha256.Sum256([]byte(o.User))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", s.getState)
	mux.HandleFunc("/api/strings", s.getStrings)
	mux.HandleFunc("/api/log", s.getLog)
	mux.HandleFunc("/api/settings", s.settings)
	mux.HandleFunc("/api/action", s.action)
	mux.HandleFunc("/api/usb", s.usb)
	mux.HandleFunc("/api/pair", s.pair)
	mux.HandleFunc("/api/quit", s.quit)
	mux.Handle("/", s.static)
	return s.guard(mux)
}

// ---------------------------------------------------------------------------
// protection

func hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if s.o.LoopbackOnly && !hostAllowed(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if s.usePW {
			ip := clientIP(r)
			if d := s.lim.blockedFor(ip); d > 0 {
				h.Set("Retry-After", retryAfter(d))
				http.Error(w, "too many failed logins, try again later", http.StatusTooManyRequests)
				return
			}
			user, pw, ok := r.BasicAuth()
			sum := sha256.Sum256([]byte(pw))
			usum := sha256.Sum256([]byte(user))
			userOK := subtle.ConstantTimeCompare(usum[:], s.userHash[:]) == 1 || s.o.User == ""
			passOK := subtle.ConstantTimeCompare(sum[:], s.pwHash[:]) == 1
			if !ok || !userOK || !passOK {
				if ok { // a browser's first request carries no credentials: not a failed guess
					s.lim.fail(ip)
				}
				time.Sleep(400 * time.Millisecond) // slow down guessing
				h.Set("WWW-Authenticate", `Basic realm="`+config.AppName+`", charset="UTF-8"`)
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
			s.lim.ok(ip)
		}
		if r.Method == http.MethodPost {
			// A custom header cannot be sent cross-site without a CORS
			// preflight, which this server never allows.
			if r.Header.Get("X-SPMAG") != "1" {
				http.Error(w, "missing X-SPMAG header", http.StatusForbidden)
				return
			}
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request", http.StatusForbidden)
					return
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// JSON helpers

type gzipWriter struct {
	http.ResponseWriter
	zw *gzip.Writer
}

func (g gzipWriter) Write(b []byte) (int, error) { return g.zw.Write(b) }

func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var out io.Writer = w
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		zw := gzip.NewWriter(w)
		defer zw.Close()
		out = zw
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(out).Encode(v)
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeJSON(w, r, status, map[string]string{"error": msg})
}

// ---------------------------------------------------------------------------
// state

type itemDTO struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Kind       int    `json:"kind"`
	KindText   string `json:"kindText"`
	Size       uint64 `json:"size"`
	SizeText   string `json:"sizeText"`
	Taken      string `json:"taken"`
	Status     int    `json:"status"`
	StatusText string `json:"statusText"`
	Folder     string `json:"folder"`
	Path       string `json:"path"`
	Note       string `json:"note"`
}

type progressDTO struct {
	Percent int    `json:"percent"` // whole run, 0..1000
	Text    string `json:"text"`
}

type stateDTO struct {
	Version  string       `json:"version"`
	Lang     string       `json:"lang"`
	State    int          `json:"state"`
	Title    string       `json:"title"`
	Detail   string       `json:"detail"`
	Counts   string       `json:"counts"`
	Paused   bool         `json:"paused"`
	CanQuit  bool         `json:"canQuit"`
	Progress *progressDTO `json:"progress"`
	Items    []itemDTO    `json:"items"`
}

func (s *server) getState(w http.ResponseWriter, r *http.Request) {
	snap := s.o.Ctl.Snapshot()
	d := stateDTO{
		Version: s.o.Version, Lang: i18n.Current(), State: int(snap.State),
		Title: snap.Title(), Detail: snap.Detail(), Counts: snap.CountsText(), Paused: snap.Paused, CanQuit: s.o.Quit != nil,
		Items: make([]itemDTO, 0, len(snap.Items)),
	}
	if p := snap.Progress; p != nil && p.QueueBytes > 0 {
		d.Progress = &progressDTO{Percent: int(p.BytesAll * 1000 / p.QueueBytes), Text: app.ProgressText(p)}
	}
	for i := range snap.Items {
		it := &snap.Items[i]
		st := app.StatusText(it.Status)
		if it.Status == transfer.StatusDownloading && it.Size > 0 {
			st = i18n.T("st.downloadingPct", int(it.Done*100/int64(it.Size)))
		}
		taken := ""
		if !it.Captured.IsZero() {
			taken = it.Captured.Format("2006-01-02 15:04:05")
		}
		d.Items = append(d.Items, itemDTO{
			Key: it.Key, Name: it.Name, Kind: int(it.Kind), KindText: app.KindText(it.Kind),
			Size: it.Size, SizeText: app.HumanBytes(it.Size), Taken: taken,
			Status: int(it.Status), StatusText: st, Folder: it.Folder, Path: it.LocalPath, Note: it.Note,
		})
	}
	writeJSON(w, r, http.StatusOK, d)
}

func (s *server) getStrings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, http.StatusOK, map[string]any{"lang": i18n.Current(), "strings": i18n.Strings()})
}

func (s *server) getLog(w http.ResponseWriter, r *http.Request) {
	var since int
	fmt.Sscanf(r.URL.Query().Get("since"), "%d", &since)
	lines, next := s.o.Logs.Since(since)
	writeJSON(w, r, http.StatusOK, map[string]any{"lines": lines, "next": next})
}

// ---------------------------------------------------------------------------
// settings

type settingsDTO struct {
	DestDir       string `json:"destDir"`
	FolderPattern string `json:"folderPattern"`
	JPEG          bool   `json:"jpeg"`
	RAW           bool   `json:"raw"`
	Video         bool   `json:"video"`
	Other         bool   `json:"other"`
	Auto          bool   `json:"auto"`
	Finish        bool   `json:"finish"`
	Match         bool   `json:"match"`
	PMH           bool   `json:"pmh"`
	GUID          string `json:"guid"`
	HostName      string `json:"hostName"`
	Language      string `json:"language"`

	// web interface options (the password is only ever received, never sent)
	WebEnabled       bool   `json:"webEnabled"`
	WebPort          int    `json:"webPort"`
	WebUser          string `json:"webUser"`
	WebPassword      string `json:"webPassword,omitempty"`
	WebClearPassword bool   `json:"webClearPassword,omitempty"`
	WebRemote        bool   `json:"webRemote"`

	AllowedCameras []string `json:"allowedCameras"`
}

type webStatusDTO struct {
	Running bool   `json:"running"`
	Addr    string `json:"addr"`
	Error   string `json:"error"`
}

type settingsReply struct {
	settingsDTO
	Languages    []i18n.Language `json:"languages"`
	USBSupported bool            `json:"usbSupported"`
	WebPassSet   bool            `json:"webPasswordSet"`
	WebLocked    bool            `json:"webLocked"` // fixed by command-line options
	WebStatus    webStatusDTO    `json:"webStatus"`
	WebRestart   bool            `json:"webRestart,omitempty"`
}

func (s *server) reply(c config.Settings, restart bool) settingsReply {
	a := AccessFrom(c).Normalize()
	r := settingsReply{
		settingsDTO: settingsDTO{
			DestDir: c.DestDir, FolderPattern: c.FolderPattern,
			JPEG: c.IncludeJPEG, RAW: c.IncludeRAW, Video: c.IncludeVideo, Other: c.IncludeOther,
			Auto: c.AutoDownload, Finish: c.FinishAfterAuto, Match: c.MatchExisting, PMH: c.AcceptPMHPairing,
			GUID: c.GUID, HostName: c.HostName, Language: c.Language,
			WebEnabled: a.Enabled, WebPort: a.Port, WebUser: a.User, WebRemote: a.Remote,
			AllowedCameras: append([]string{}, c.AllowedCameras...),
		},
		Languages: i18n.Languages, USBSupported: pairing.USBSupported,
		WebPassSet: a.Password != "", WebRestart: restart,
	}
	if m := s.o.Manager; m != nil {
		st := m.Status()
		r.WebStatus = webStatusDTO{Running: st.Running, Addr: st.Addr, Error: st.Err}
		r.WebLocked = m.Pinned()
	}
	return r
}

func validPattern(p string) bool {
	if p == "" {
		return true
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || strings.Contains(p, ":") {
		return false
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	return true
}

func (s *server) saveSettings(c config.Settings) error {
	if err := config.Save(c); err != nil {
		return err
	}
	s.o.Ctl.SetSettings(c)
	return nil
}

func (s *server) settings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, r, http.StatusOK, s.reply(s.o.Ctl.Settings(), false))
	case http.MethodPost:
		var in settingsDTO
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, r, http.StatusBadRequest, err.Error())
			return
		}
		in.DestDir = strings.TrimSpace(in.DestDir)
		in.FolderPattern = strings.TrimSpace(in.FolderPattern)
		if in.DestDir == "" || !filepath.IsAbs(in.DestDir) {
			writeErr(w, r, http.StatusBadRequest, i18n.T("dlg.needFolder"))
			return
		}
		if !validPattern(in.FolderPattern) {
			writeErr(w, r, http.StatusBadRequest, "invalid subfolder pattern")
			return
		}
		if in.Language != "" && i18n.Normalize(in.Language) != in.Language {
			writeErr(w, r, http.StatusBadRequest, "unknown language")
			return
		}
		if err := os.MkdirAll(in.DestDir, 0o755); err != nil {
			writeErr(w, r, http.StatusBadRequest, i18n.T("dlg.cannotCreate", err.Error()))
			return
		}
		c := s.o.Ctl.Settings()
		c.DestDir, c.FolderPattern = in.DestDir, in.FolderPattern
		c.IncludeJPEG, c.IncludeRAW, c.IncludeVideo, c.IncludeOther = in.JPEG, in.RAW, in.Video, in.Other
		c.AutoDownload, c.FinishAfterAuto, c.MatchExisting, c.AcceptPMHPairing = in.Auto, in.Finish, in.Match, in.PMH
		c.HostName = strings.TrimSpace(in.HostName)
		c.Language = in.Language
		list := acl.Split(strings.Join(in.AllowedCameras, "\n"))
		if _, err := acl.Parse(list); err != nil {
			writeErr(w, r, http.StatusBadRequest, err.Error())
			return
		}
		c.AllowedCameras = list

		// Web access options: only when this server can restart itself and
		// the command line has not fixed them.
		var newAccess *Access
		if m := s.o.Manager; m != nil && !m.Pinned() {
			pw := in.WebPassword
			if pw == "" && !in.WebClearPassword {
				pw = c.WebPassword
			}
			a := Access{Enabled: in.WebEnabled, Port: in.WebPort, User: in.WebUser, Password: pw, Remote: in.WebRemote}.Normalize()
			if err := a.Validate(); err != nil {
				writeErr(w, r, http.StatusBadRequest, err.Error())
				return
			}
			if a != AccessFrom(c).Normalize() {
				newAccess = &a
			}
			a.Apply(&c)
		}

		i18n.Set(c.Language)
		if err := s.saveSettings(c); err != nil {
			writeErr(w, r, http.StatusInternalServerError, i18n.T("dlg.saveErr", err.Error()))
			return
		}
		if newAccess != nil {
			// Restart the listener after this reply has gone out.
			m, a := s.o.Manager, *newAccess
			go func() {
				time.Sleep(400 * time.Millisecond)
				_ = m.Apply(a)
			}()
		}
		if m := s.o.Manager; m != nil && m.OnSettings != nil {
			m.OnSettings(c)
		}
		writeJSON(w, r, http.StatusOK, s.reply(c, newAccess != nil))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// actions

func (s *server) action(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Action string   `json:"action"`
		Keys   []string `json:"keys"`
		IP     string   `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, r, http.StatusBadRequest, err.Error())
		return
	}
	ctl := s.o.Ctl
	switch in.Action {
	case "downloadMissing":
		ctl.DownloadMissing()
	case "downloadSelected":
		if len(in.Keys) == 0 {
			writeErr(w, r, http.StatusBadRequest, i18n.T("dlg.selectFirst"))
			return
		}
		ctl.DownloadKeys(in.Keys)
	case "stop":
		ctl.CancelDownload()
	case "rescan":
		ctl.Rescan()
	case "finish":
		ctl.Disconnect(true)
	case "forget":
		if len(in.Keys) == 0 {
			writeErr(w, r, http.StatusBadRequest, i18n.T("dlg.selectFirst"))
			return
		}
		ctl.Forget(in.Keys)
	case "pause":
		ctl.SetPaused(true)
	case "resume":
		ctl.SetPaused(false)
	case "connectIP":
		ip := net.ParseIP(strings.TrimSpace(in.IP))
		if ip == nil {
			writeErr(w, r, http.StatusBadRequest, i18n.T("acl.needIP"))
			return
		}
		if err := ctl.CheckAddr(ip.String()); err != nil { // access list, and no public addresses
			writeErr(w, r, http.StatusBadRequest, err.Error())
			return
		}
		c := ctl.Settings()
		c.ManualIP = ip.String()
		if err := s.saveSettings(c); err != nil {
			writeErr(w, r, http.StatusInternalServerError, i18n.T("dlg.saveErr", err.Error()))
			return
		}
		ctl.ConnectIP(ip.String())
	case "newGUID":
		c := ctl.Settings()
		c.GUID = pairing.NewGUID().String()
		if err := s.saveSettings(c); err != nil {
			writeErr(w, r, http.StatusInternalServerError, i18n.T("dlg.saveErr", err.Error()))
			return
		}
	default:
		writeErr(w, r, http.StatusBadRequest, "unknown action")
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) quit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.o.Quit == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]bool{"ok": true})
	go func() {
		time.Sleep(300 * time.Millisecond) // let the reply go out
		s.o.Quit()
	}()
}

// ---------------------------------------------------------------------------
// USB pairing (Linux; also Windows when run as administrator)

func (s *server) usb(w http.ResponseWriter, r *http.Request) {
	var list []string
	if pairing.USBSupported {
		if ds, err := pairing.ListUSBDrives(); err == nil {
			for _, d := range ds {
				list = append(list, d.String())
			}
		}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"supported": pairing.USBSupported, "drives": list})
}

func (s *server) pair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pairing.USBSupported {
		writeErr(w, r, http.StatusNotImplemented, i18n.T("web.pairNA"))
		return
	}
	ds, err := pairing.ListUSBDrives()
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	var sony []pairing.Drive
	var names []string
	for _, d := range ds {
		names = append(names, d.String())
		if d.IsSony() {
			sony = append(sony, d)
		}
	}
	if len(sony) != 1 {
		found := i18n.T("dlg.none")
		if len(names) > 0 {
			found = strings.Join(names, "\n")
		}
		writeErr(w, r, http.StatusBadRequest, i18n.T("dlg.noCamera", found))
		return
	}
	c := s.o.Ctl.Settings()
	if err := pairing.WriteGUID(sony[0].Path, c.OwnGUID(), c.EffectiveHostName()); err != nil {
		writeErr(w, r, http.StatusInternalServerError, i18n.T("dlg.pairFail", err.Error()))
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]string{"message": i18n.T("dlg.pairOK")})
}
