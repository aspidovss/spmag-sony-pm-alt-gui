// Package transfer connects to a camera, lists its files, reconciles them
// with what was already received and downloads the missing ones.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/acl"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/pairing"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ptpip"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ssdp"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/store"
)

// Kind is the file category used by the type filter.
type Kind int

const (
	KindJPEG Kind = iota
	KindRAW
	KindVideo
	KindOther
)

func (k Kind) String() string {
	return [...]string{"JPEG", "RAW", "Video", "Other"}[k]
}

// KindOf classifies a file by extension.
func KindOf(name string) Kind {
	switch strings.ToUpper(filepath.Ext(name)) {
	case ".JPG", ".JPEG", ".HIF", ".HEIF", ".HEIC":
		return KindJPEG
	case ".ARW", ".SR2", ".SRF", ".DNG":
		return KindRAW
	case ".MP4", ".MTS", ".M2TS", ".MOV", ".AVI", ".MPG", ".XAVC":
		return KindVideo
	}
	return KindOther
}

// Included reports whether the settings include the kind.
func Included(cfg *config.Settings, k Kind) bool {
	switch k {
	case KindJPEG:
		return cfg.IncludeJPEG
	case KindRAW:
		return cfg.IncludeRAW
	case KindVideo:
		return cfg.IncludeVideo
	}
	return cfg.IncludeOther
}

// Status of a camera file relative to the PC.
type Status int

const (
	StatusMissing Status = iota
	StatusReceived
	StatusQueued
	StatusDownloading
	StatusFailed
	StatusExcluded
)

func (s Status) String() string {
	return [...]string{"Missing", "Received", "Queued", "Downloading", "Failed", "Excluded by filter"}[s]
}

// Item is one file on the camera.
type Item struct {
	Key         string
	LegacyKey   string // key used before records were per camera (v0.4.1 and older)
	Handle      uint32
	Storage     uint32
	Folder      string
	Name        string
	Size        uint64
	CapturedRaw string
	Captured    time.Time
	Kind        Kind
	Status      Status
	LocalPath   string
	Note        string
	Done        int64 // bytes received during download
}

// Session is an open connection to a camera.
type Session struct {
	Client     *ptpip.Client
	Info       *ptpip.DeviceInfo
	IP         string
	CameraName string
	CameraID   string
	Serial     string
	UsedGUID   pairing.GUID
	GUIDSource string // "own" or "PlayMemories (adapter MAC)"

	cfg   config.Settings
	db    *store.DB
	logf  func(string, ...any)
	mu    sync.Mutex
	items []*Item
	sony  bool
}

// ErrNoPairing means no candidate GUID was accepted.
var ErrNoPairing = errors.New("the camera refused this PC: it is not paired. Pair the camera via USB (Settings → Pair camera) or once with PlayMemories Home")

// Connect opens a PTP/IP session with the camera at ip (optionally ip:port), an
// address somebody typed in: without an access list it must be a local one.
func Connect(ctx context.Context, ip string, cfg config.Settings, db *store.DB, logf func(string, ...any)) (*Session, error) {
	return connect(ctx, ip, true, cfg, db, logf)
}

// ConnectDiscovered is Connect for a camera found by its SSDP announcement.
func ConnectDiscovered(ctx context.Context, ip string, cfg config.Settings, db *store.DB, logf func(string, ...any)) (*Session, error) {
	return connect(ctx, ip, false, cfg, db, logf)
}

func connect(ctx context.Context, ip string, manual bool, cfg config.Settings, db *store.DB, logf func(string, ...any)) (*Session, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// Access list: check the address before anything (our PC's ID included) is
	// sent to it. An invalid list refuses everything rather than allowing all.
	rules, err := acl.Parse(cfg.AllowedCameras)
	if err != nil {
		return nil, err
	}
	if err := rules.AllowAddr(ip, manual); err != nil {
		return nil, err
	}
	type cand struct {
		g   pairing.GUID
		src string
	}
	var cands []cand
	if g := cfg.OwnGUID(); !g.IsZero() {
		cands = append(cands, cand{g, "own"})
	}
	if cfg.AcceptPMHPairing {
		host := ip
		if h, _, err := splitHost(ip); err == nil {
			host = h
		}
		for _, g := range pairing.PMHGuidsFor(host) {
			cands = append(cands, cand{g, "PlayMemories (adapter MAC)"})
		}
	}
	var c *ptpip.Client
	var used cand
	var lastErr error = ErrNoPairing
	for _, cd := range cands {
		cl, err := ptpip.Dial(ctx, ip, ptpip.Options{
			GUID:     cd.g,
			HostName: cfg.EffectiveHostName(),
			Logf:     logf,
		})
		if err == nil {
			c, used = cl, cd
			break
		}
		if errors.Is(err, ptpip.ErrNotPaired) {
			logf("GUID %s (%s) not accepted", cd.g, cd.src)
			// the camera may need a moment before accepting the next connection
			time.Sleep(300 * time.Millisecond)
			continue
		}
		lastErr = err
		break
	}
	if c == nil {
		if errors.Is(lastErr, ptpip.ErrNotPaired) {
			return nil, ErrNoPairing
		}
		return nil, lastErr
	}
	s := &Session{Client: c, IP: ip, CameraName: c.CameraName, UsedGUID: used.g, GUIDSource: used.src, cfg: cfg, db: db, logf: logf}
	logf("Connected using %s GUID %s", used.src, used.g)
	if err := c.OpenSession(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("open session: %w", err)
	}
	info, err := c.GetDeviceInfo(ctx)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("device info: %w", err)
	}
	if err := rules.AllowDevice(info.Model, info.SerialNumber); err != nil {
		c.Close() // before any file list is read
		return nil, err
	}
	// text from the device ends up in logs and on screen
	info.Manufacturer, info.Model, info.SerialNumber, info.DeviceVersion =
		ssdp.CleanText(info.Manufacturer), ssdp.CleanText(info.Model), ssdp.CleanText(info.SerialNumber), ssdp.CleanText(info.DeviceVersion)
	s.Info = info
	s.CameraID = strings.TrimSpace(info.Model + " " + info.SerialNumber)
	s.Serial = info.SerialNumber
	if info.Model != "" {
		s.CameraName = info.Model
	}
	logf("Camera: %s %s, firmware %s", info.Manufacturer, info.Model, info.DeviceVersion)
	if info.Supports(ptpip.OpSonySDIOConnect) && info.Supports(ptpip.OpSonyGetExtDevInfo) {
		s.sony = true
		if err := c.SonyBeginTransfer(ctx); err != nil {
			logf("Sony start handshake: %v (continuing)", err)
		}
	}
	return s, nil
}

func splitHost(s string) (string, string, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s, "", errors.New("no port")
	}
	return s[:i], s[i+1:], nil
}

// Scan lists all files on all storages. progress receives the number of
// files found so far.
func (s *Session) Scan(ctx context.Context, progress func(n int)) error {
	ids, err := s.Client.GetStorageIDs(ctx)
	if err != nil {
		return fmt.Errorf("list storages: %w", err)
	}
	var items []*Item
	seen := map[uint32]bool{}
	var walk func(storage, parent uint32, folder string, depth int) error
	walk = func(storage, parent uint32, folder string, depth int) error {
		if depth > 8 {
			return nil
		}
		hs, err := s.Client.GetObjectHandles(ctx, storage, 0, parent)
		if err != nil {
			return err
		}
		for _, h := range hs {
			if seen[h] {
				continue
			}
			seen[h] = true
			oi, err := s.Client.GetObjectInfo(ctx, h)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				s.logf("object 0x%X: %v", h, err)
				continue
			}
			if oi.IsFolder() {
				if err := walk(storage, h, oi.Filename, depth+1); err != nil {
					return err
				}
				continue
			}
			size := uint64(oi.CompressedSize)
			if oi.CompressedSize == 0xFFFFFFFF {
				if v, err := s.Client.GetObjectSize64(ctx, h); err == nil {
					size = v
				}
			}
			name := ssdp.CleanTextN(oi.Filename, 255) // no line breaks etc. in logs and tables
			capRaw := oi.CaptureDate
			if capRaw == "" {
				capRaw = oi.ModificationDate
			}
			t, ok := ptpip.ParsePTPTime(capRaw)
			if !ok {
				t = time.Time{}
			}
			items = append(items, &Item{
				Key: store.CameraKey(s.CameraID, name, size, capRaw), LegacyKey: store.Key(name, size, capRaw), Handle: h, Storage: storage,
				Folder: folder, Name: name, Size: size, CapturedRaw: capRaw,
				Captured: t, Kind: KindOf(oi.Filename),
			})
			if progress != nil {
				progress(len(items))
			}
		}
		return nil
	}
	for _, id := range ids {
		if id&0xFFFF == 0 { // storage not present (e.g. empty card slot)
			continue
		}
		if err := walk(id, 0xFFFFFFFF, "", 0); err != nil {
			return fmt.Errorf("list files: %w", err)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Captured.Equal(items[j].Captured) {
			return items[i].Captured.Before(items[j].Captured)
		}
		return items[i].Name < items[j].Name
	})
	s.mu.Lock()
	s.items = items
	s.mu.Unlock()
	s.logf("Found %d files on the camera", len(items))
	return nil
}

// Reconcile recomputes the status of every item.
func (s *Session) Reconcile(idx DestIndex) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.Status == StatusDownloading || it.Status == StatusQueued {
			continue
		}
		reconcileItem(it, &s.cfg, s.db, idx, s.CameraID)
	}
}

func reconcileItem(it *Item, cfg *config.Settings, db *store.DB, idx DestIndex, camID string) {
	it.Note = ""
	it.LocalPath = ""
	rec, ok := db.Get(it.Key)
	if !ok && it.LegacyKey != "" && it.LegacyKey != it.Key {
		// A record written by an older version: take it over if it is this
		// camera's (or has no camera noted), and keep it under the new key.
		if old, found := db.Get(it.LegacyKey); found && (old.Camera == "" || old.Camera == camID) {
			db.Delete(it.LegacyKey)
			old.Key, old.Camera = it.Key, camID
			db.Put(old)
			rec, ok = old, true
		}
	}
	if ok {
		if st, err := os.Stat(rec.LocalPath); err == nil && uint64(st.Size()) == it.Size {
			it.Status = StatusReceived
			it.LocalPath = rec.LocalPath
			return
		}
		it.Note = "received earlier, but the local file is gone"
	} else if cfg.MatchExisting && idx != nil {
		if p := idx.Find(SafeFileName(it.Name), it.Size); p != "" {
			it.Status = StatusReceived
			it.LocalPath = p
			it.Note = "found in destination folder"
			db.Put(store.Record{Key: it.Key, Name: it.Name, Size: it.Size, Captured: it.CapturedRaw, Camera: camID, LocalPath: p, ReceivedAt: time.Now()})
			return
		}
	}
	if !Included(cfg, it.Kind) {
		it.Status = StatusExcluded
		return
	}
	if it.Status != StatusFailed {
		it.Status = StatusMissing
	}
}

// Items returns a snapshot of the item list.
func (s *Session) Items() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Item, len(s.items))
	for i, it := range s.items {
		out[i] = *it
	}
	return out
}

// UpdateSettings replaces the settings used for filtering and paths.
func (s *Session) UpdateSettings(cfg config.Settings) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

// Progress is reported during Download.
type Progress struct {
	Index, Count int   // file number (1-based) and number of files
	Item         Item  // current file
	Done, Total  int64 // bytes of the current file
	BytesAll     int64 // bytes of all files so far (including skipped identical files)
	QueueBytes   int64 // bytes of all files in this run
	Transferred  int64 // bytes actually received over the network in this run

	Speed float64       // bytes per second (filled in by the caller's RateMeter)
	ETA   time.Duration // estimated time left for the whole run (0 = unknown)
}

// RateMeter estimates a transfer rate over a sliding window.
type RateMeter struct {
	Window  time.Duration
	samples []rateSample
}

type rateSample struct {
	t time.Time
	n int64
}

// Add records the cumulative byte count at time t.
func (m *RateMeter) Add(t time.Time, total int64) {
	if m.Window == 0 {
		m.Window = 10 * time.Second
	}
	m.samples = append(m.samples, rateSample{t, total})
	cut := 0
	for cut < len(m.samples)-2 && t.Sub(m.samples[cut+1].t) >= m.Window {
		cut++
	}
	m.samples = m.samples[cut:]
}

// Rate returns bytes per second, or 0 if not enough data yet.
func (m *RateMeter) Rate() float64 {
	if len(m.samples) < 2 {
		return 0
	}
	a, b := m.samples[0], m.samples[len(m.samples)-1]
	dt := b.t.Sub(a.t).Seconds()
	if dt < 1 {
		return 0
	}
	return float64(b.n-a.n) / dt
}

// ETA returns the time needed for remaining bytes at the current rate.
func (m *RateMeter) ETA(remaining int64) time.Duration {
	r := m.Rate()
	if r <= 0 || remaining <= 0 {
		return 0
	}
	return time.Duration(float64(remaining) / r * float64(time.Second))
}

// Result summarises a download run.
type Result struct {
	Downloaded int
	Failed     int
	Bytes      int64
	Duration   time.Duration
}

// Download fetches the given items (by key). If keys is nil, all missing
// items that pass the type filter are downloaded.
func (s *Session) Download(ctx context.Context, keys []string, onProgress func(Progress)) (Result, error) {
	start := time.Now()
	s.mu.Lock()
	var queue []*Item
	if keys == nil {
		for _, it := range s.items {
			if it.Status == StatusMissing || (it.Status == StatusFailed && Included(&s.cfg, it.Kind)) {
				queue = append(queue, it)
			}
		}
	} else {
		want := map[string]bool{}
		for _, k := range keys {
			want[k] = true
		}
		for _, it := range s.items {
			if want[it.Key] {
				queue = append(queue, it)
			}
		}
	}
	for _, it := range queue {
		it.Status = StatusQueued
	}
	cfg := s.cfg
	s.mu.Unlock()

	var res Result
	var all, xfer, queueBytes int64
	for _, it := range queue {
		queueBytes += int64(it.Size)
	}
	lastSave := time.Now()
	for i, it := range queue {
		if ctx.Err() != nil {
			break
		}
		s.setStatus(it, StatusDownloading, "")
		idx := i + 1
		path, got, err := s.downloadOne(ctx, it, cfg, func(done, total int64) {
			s.mu.Lock()
			it.Done = done
			snap := *it
			s.mu.Unlock()
			if onProgress != nil {
				onProgress(Progress{Index: idx, Count: len(queue), Item: snap, Done: done, Total: total,
					BytesAll: all + done, QueueBytes: queueBytes, Transferred: xfer + done})
			}
		})
		xfer += got
		if err != nil {
			res.Failed++
			s.setStatus(it, StatusFailed, err.Error())
			s.logf("FAILED %s: %v", it.Name, err)
			if ctx.Err() != nil || isConnErr(err) {
				break
			}
			continue
		}
		all += int64(it.Size)
		res.Downloaded++
		s.mu.Lock()
		it.Status = StatusReceived
		it.LocalPath = path
		it.Note = ""
		s.mu.Unlock()
		s.db.Put(store.Record{Key: it.Key, Name: it.Name, Size: it.Size, Captured: it.CapturedRaw, Camera: s.CameraID, LocalPath: path, ReceivedAt: time.Now()})
		if time.Since(lastSave) > 3*time.Second {
			_ = s.db.Save()
			lastSave = time.Now()
		}
		s.logf("Saved %s → %s", it.Name, path)
		if onProgress != nil {
			onProgress(Progress{Index: idx, Count: len(queue), Item: *it, Done: int64(it.Size), Total: int64(it.Size),
				BytesAll: all, QueueBytes: queueBytes, Transferred: xfer})
		}
	}
	// anything still queued (cancelled) goes back to missing
	s.mu.Lock()
	for _, it := range queue {
		if it.Status == StatusQueued || it.Status == StatusDownloading {
			it.Status = StatusMissing
		}
	}
	s.mu.Unlock()
	_ = s.db.Save()
	res.Bytes = xfer
	res.Duration = time.Since(start)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}

func isConnErr(err error) bool {
	var pe *ptpip.Error
	if errors.As(err, &pe) {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection") || strings.Contains(s, "EOF") || strings.Contains(s, "timeout") || strings.Contains(s, "i/o")
}

func (s *Session) setStatus(it *Item, st Status, note string) {
	s.mu.Lock()
	it.Status = st
	it.Note = note
	s.mu.Unlock()
}

func (s *Session) downloadOne(ctx context.Context, it *Item, cfg config.Settings, progress func(done, total int64)) (string, int64, error) {
	dir := filepath.Join(cfg.DestDir, ExpandPattern(cfg.FolderPattern, it.Captured, s.CameraName, s.Serial))
	// The file name comes from the device on the network and is not trusted.
	name := SafeFileName(it.Name)
	if name == "" {
		name = fmt.Sprintf("file_%08x", it.Handle)
	}
	target := filepath.Join(dir, name)
	if !within(cfg.DestDir, target) {
		return "", 0, fmt.Errorf("refusing to write outside the destination folder: %q", it.Name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	if st, err := os.Stat(target); err == nil {
		if uint64(st.Size()) == it.Size {
			return target, 0, nil // identical file already there
		}
		target = uniqueName(target)
	}
	part := target + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", 0, err
	}
	bw := &countingWriter{w: f}
	err = s.Client.GetObject(ctx, it.Handle, bw, progress)
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && it.Size != 0 && uint64(bw.n) != it.Size {
		err = fmt.Errorf("size mismatch: got %d bytes, expected %d", bw.n, it.Size)
	}
	if err != nil {
		os.Remove(part)
		return "", bw.n, err
	}
	if err := os.Rename(part, target); err != nil {
		os.Remove(part)
		return "", bw.n, err
	}
	if !it.Captured.IsZero() {
		_ = os.Chtimes(target, it.Captured, it.Captured)
	}
	return target, bw.n, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// within reports whether path is inside root (after cleaning both).
func within(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

var reservedNames = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

// SafeFileName turns a file name reported by a device into a plain file name
// that is safe to create on Windows and Linux: no folders (a name such as
// "..\..\x" keeps only "x"), no control or reserved characters, no trailing
// dots or spaces, no Windows device names, at most 200 bytes. It returns ""
// when nothing usable is left.
func SafeFileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = sanitize(name)
	name = strings.TrimRight(name, ". ")
	name = strings.TrimLeft(name, " ")
	if name == "" || name == "." || name == ".." {
		return ""
	}
	stem := strings.ToUpper(strings.TrimRight(strings.SplitN(name, ".", 2)[0], " "))
	if reservedNames[stem] || ((strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && len(stem) == 4 && stem[3] >= '1' && stem[3] <= '9') {
		name = "_" + name
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stemPart := name[:200-len(ext)]
		for len(stemPart) > 0 && !utf8.ValidString(stemPart) {
			stemPart = stemPart[:len(stemPart)-1]
		}
		name = stemPart + ext
	}
	return name
}

func uniqueName(p string) string {
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		c := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := os.Stat(c); os.IsNotExist(err) {
			return c
		}
	}
}

// ExpandPattern turns a folder pattern into a relative path.
// Tokens: {yyyy} {yy} {MM} {dd} {camera} (model) {serial} (serial number).
func ExpandPattern(pattern string, t time.Time, camera, serial string) string {
	if strings.TrimSpace(pattern) == "" {
		return ""
	}
	if t.IsZero() {
		t = time.Now()
	}
	r := strings.NewReplacer(
		"{yyyy}", t.Format("2006"),
		"{yy}", t.Format("06"),
		"{MM}", t.Format("01"),
		"{dd}", t.Format("02"),
		"{camera}", sanitize(camera),
		"{serial}", sanitize(serial),
	)
	out := r.Replace(pattern)
	out = strings.ReplaceAll(out, "/", string(filepath.Separator))
	out = strings.ReplaceAll(out, `\`, string(filepath.Separator))
	parts := strings.Split(out, string(filepath.Separator))
	clean := parts[:0]
	for _, p := range parts {
		p = sanitize(p)
		if p != "" && p != "." && p != ".." {
			clean = append(clean, p)
		}
	}
	return filepath.Join(clean...)
}

func sanitize(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, s))
}

// End finishes the session. With finish=true a Sony camera is told that the
// transfer is complete (it shows the result and usually switches off).
func (s *Session) End(finish bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if finish && s.sony {
		if err := s.Client.SonyEndTransfer(ctx); err != nil {
			s.logf("end transfer: %v", err)
		}
	} else {
		_ = s.Client.CloseSession(ctx)
	}
	s.Client.Close()
	_ = s.db.Save()
}

// DestIndex maps upper-case file names to files present in the destination.
type DestIndex map[string][]destFile

type destFile struct {
	path string
	size int64
}

// BuildDestIndex scans dir recursively (skipping hidden folders).
func BuildDestIndex(dir string) DestIndex {
	idx := DestIndex{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		k := strings.ToUpper(d.Name())
		idx[k] = append(idx[k], destFile{p, info.Size()})
		return nil
	})
	return idx
}

// Find returns the path of a file with the same name and size, or "".
func (idx DestIndex) Find(name string, size uint64) string {
	for _, f := range idx[strings.ToUpper(name)] {
		if uint64(f.size) == size {
			return f.path
		}
	}
	return ""
}
