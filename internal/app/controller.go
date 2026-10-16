// Package app ties discovery, sessions and settings together. It is UI
// independent: the Windows GUI and the tests drive it the same way.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ssdp"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/store"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/transfer"
)

// State of the controller.
type State int

const (
	StateWaiting State = iota
	StateConnecting
	StateScanning
	StateReady
	StateDownloading
)

func (s State) String() string {
	return [...]string{"Waiting for camera", "Connecting", "Reading file list", "Connected", "Downloading"}[s]
}

// Counts summarises the file list.
type Counts struct {
	Total, Received, Missing, Failed, Excluded int
	MissingBytes                               uint64
}

// Snapshot is what the UI shows.
type Snapshot struct {
	State      State
	Message    string
	Camera     string
	IP         string
	GUIDSource string
	Items      []transfer.Item
	Counts     Counts
	Progress   *transfer.Progress
	Paused     bool
}

type cmdKind int

const (
	cmdConnect cmdKind = iota
	cmdDownload
	cmdRescan
	cmdDisconnect
	cmdDiscovered
	cmdSettings
	cmdForget
)

type command struct {
	kind   cmdKind
	ip     string
	keys   []string
	finish bool
	cam    ssdp.Camera
	cfg    config.Settings
}

// Controller runs the import workflow.
type Controller struct {
	OnChange func()                   // called after any state change (any goroutine)
	OnLog    func(line string)        // log lines
	OnNotify func(title, text string) // notifications (transfer finished etc.)

	mu       sync.Mutex
	cfg      config.Settings
	db       *store.DB
	snap     Snapshot
	cmds     chan command
	cancelDL context.CancelFunc
	cooldown map[string]time.Time
	sess     *transfer.Session
	lastProg time.Time
	stopped  chan struct{}
}

// New creates a controller.
func New(cfg config.Settings, db *store.DB) *Controller {
	return &Controller{cfg: cfg, db: db, cmds: make(chan command, 32), cooldown: map[string]time.Time{}, stopped: make(chan struct{}),
		snap: Snapshot{State: StateWaiting, Message: waitingText}}
}

const waitingText = "Waiting for camera… On the camera choose MENU → Wireless → Send to Computer."

func (c *Controller) logf(format string, args ...any) {
	line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, args...)
	if c.OnLog != nil {
		c.OnLog(line)
	}
}

func (c *Controller) changed() {
	if c.OnChange != nil {
		c.OnChange()
	}
}

func (c *Controller) update(f func(s *Snapshot)) {
	c.mu.Lock()
	f(&c.snap)
	c.mu.Unlock()
	c.changed()
}

// Snapshot returns the current state (items are copied).
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.snap
	s.Items = append([]transfer.Item(nil), c.snap.Items...)
	if c.snap.Progress != nil {
		p := *c.snap.Progress
		s.Progress = &p
	}
	return s
}

// Settings returns the current settings.
func (c *Controller) Settings() config.Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// Start runs discovery and the worker until ctx is cancelled.
func (c *Controller) Start(ctx context.Context) {
	l := &ssdp.Listener{
		Logf: c.logf,
		OnCamera: func(cam ssdp.Camera) {
			select {
			case c.cmds <- command{kind: cmdDiscovered, cam: cam}:
			default:
			}
		},
	}
	go l.Run(ctx)
	go c.worker(ctx)
	c.logf("Started. Listening for cameras on the local network.")
}

// ConnectIP connects to a camera by address (bypasses discovery).
func (c *Controller) ConnectIP(ip string) { c.cmds <- command{kind: cmdConnect, ip: ip} }

// DownloadMissing downloads all missing files that pass the filter.
func (c *Controller) DownloadMissing() { c.cmds <- command{kind: cmdDownload} }

// DownloadKeys downloads the given files regardless of the filter.
func (c *Controller) DownloadKeys(keys []string) {
	if keys == nil {
		keys = []string{}
	}
	c.cmds <- command{kind: cmdDownload, keys: keys}
}

// Rescan re-reads the camera file list.
func (c *Controller) Rescan() { c.cmds <- command{kind: cmdRescan} }

// Disconnect ends the session; finish=true tells the camera the transfer is done.
func (c *Controller) Disconnect(finish bool) { c.cmds <- command{kind: cmdDisconnect, finish: finish} }

// Forget marks files as not received (removes them from the database).
func (c *Controller) Forget(keys []string) { c.cmds <- command{kind: cmdForget, keys: keys} }

// CancelDownload stops a running download after the current packet.
func (c *Controller) CancelDownload() {
	c.mu.Lock()
	cf := c.cancelDL
	c.mu.Unlock()
	if cf != nil {
		cf()
	}
}

// SetSettings applies new settings.
func (c *Controller) SetSettings(cfg config.Settings) { c.cmds <- command{kind: cmdSettings, cfg: cfg} }

// SetPaused stops reacting to camera announcements.
func (c *Controller) SetPaused(p bool) {
	c.update(func(s *Snapshot) { s.Paused = p })
	if p {
		c.logf("Automatic import paused")
	} else {
		c.logf("Automatic import resumed")
	}
}

// Stopped is closed when the worker has exited after Start's context ended.
func (c *Controller) Stopped() <-chan struct{} { return c.stopped }

func (c *Controller) worker(ctx context.Context) {
	defer close(c.stopped)
	for {
		var done <-chan struct{}
		if c.sess != nil {
			done = c.sess.Client.Done()
		}
		select {
		case <-ctx.Done():
			if c.sess != nil {
				c.sess.End(false)
			}
			_ = c.db.Save()
			return
		case <-done:
			c.logf("Camera disconnected")
			c.dropSession("Camera disconnected. " + waitingText)
		case cmd := <-c.cmds:
			c.handle(ctx, cmd)
		}
	}
}

func (c *Controller) dropSession(msg string) {
	if c.sess != nil {
		c.sess.Client.Close()
		c.cooldown[c.sess.IP] = time.Now().Add(15 * time.Second)
		c.sess = nil
	}
	_ = c.db.Save()
	c.update(func(s *Snapshot) {
		s.State = StateWaiting
		s.Message = msg
		s.Camera, s.IP, s.GUIDSource = "", "", ""
		s.Items = nil
		s.Counts = Counts{}
		s.Progress = nil
	})
}

func (c *Controller) handle(ctx context.Context, cmd command) {
	switch cmd.kind {
	case cmdSettings:
		c.mu.Lock()
		c.cfg = cmd.cfg
		c.mu.Unlock()
		if c.sess != nil {
			c.sess.UpdateSettings(cmd.cfg)
			c.reconcile()
		}
	case cmdDiscovered:
		c.mu.Lock()
		paused := c.snap.Paused
		c.mu.Unlock()
		if c.sess != nil || paused {
			return
		}
		if until, ok := c.cooldown[cmd.cam.IP]; ok && time.Now().Before(until) {
			return
		}
		c.logf("Found %s (%s) at %s", cmd.cam.FriendlyName, cmd.cam.Model, cmd.cam.IP)
		c.connect(ctx, cmd.cam.IP, true)
	case cmdConnect:
		if c.sess != nil {
			c.logf("Already connected to %s", c.sess.IP)
			return
		}
		c.connect(ctx, cmd.ip, false)
	case cmdRescan:
		if c.sess == nil {
			return
		}
		c.scan(ctx)
	case cmdDownload:
		if c.sess == nil {
			return
		}
		c.download(ctx, cmd.keys)
	case cmdForget:
		for _, k := range cmd.keys {
			c.db.Delete(k)
		}
		_ = c.db.Save()
		if c.sess != nil {
			c.reconcileNoIndex()
		}
	case cmdDisconnect:
		if c.sess == nil {
			return
		}
		c.sess.End(cmd.finish)
		c.cooldown[c.sess.IP] = time.Now().Add(90 * time.Second)
		c.sess = nil
		c.dropSession(waitingText)
	}
}

func (c *Controller) connect(ctx context.Context, ip string, auto bool) {
	cfg := c.Settings()
	c.update(func(s *Snapshot) {
		s.State = StateConnecting
		s.Message = "Connecting to camera at " + ip + "…"
		s.IP = ip
	})
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	sess, err := transfer.Connect(cctx, ip, cfg, c.db, c.logf)
	cancel()
	if err != nil {
		c.logf("Connection failed: %v", err)
		wait := 10 * time.Second
		if errors.Is(err, transfer.ErrNoPairing) {
			wait = 30 * time.Second
			if c.OnNotify != nil {
				c.OnNotify("Camera not paired", "The camera refused this PC. Pair it via USB in Settings.")
			}
		}
		c.cooldown[ip] = time.Now().Add(wait)
		c.dropSession("Connection failed: " + err.Error())
		return
	}
	c.sess = sess
	c.update(func(s *Snapshot) {
		s.Camera = sess.CameraName
		s.GUIDSource = sess.GUIDSource
	})
	if !c.scan(ctx) {
		return
	}
	if auto && cfg.AutoDownload {
		res := c.download(ctx, nil)
		if c.sess != nil && cfg.FinishAfterAuto {
			snap := c.Snapshot()
			if snap.Counts.Failed == 0 && snap.Counts.Missing == 0 {
				c.sess.End(true)
				c.cooldown[ip] = time.Now().Add(90 * time.Second)
				c.sess = nil
				c.dropSession(fmt.Sprintf("Last transfer at %s: %d new file(s) downloaded; %d of %d camera files are on this PC. %s",
					time.Now().Format("15:04"), res.Downloaded, snap.Counts.Received, snap.Counts.Total, waitingText))
			}
		}
	}
}

func (c *Controller) scan(ctx context.Context) bool {
	c.update(func(s *Snapshot) {
		s.State = StateScanning
		s.Message = "Reading the file list from the camera…"
		s.Progress = nil
	})
	sctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	err := c.sess.Scan(sctx, func(n int) {
		if n%25 == 0 {
			c.update(func(s *Snapshot) { s.Message = fmt.Sprintf("Reading the file list from the camera… %d files", n) })
		}
	})
	cancel()
	if err != nil {
		c.logf("Scan failed: %v", err)
		c.dropSession("Reading the file list failed: " + err.Error())
		return false
	}
	c.reconcile()
	return true
}

func (c *Controller) reconcile() {
	cfg := c.Settings()
	var idx transfer.DestIndex
	if cfg.MatchExisting {
		idx = transfer.BuildDestIndex(cfg.DestDir)
	}
	c.sess.Reconcile(idx)
	c.publishItems(StateReady, "")
}

func (c *Controller) reconcileNoIndex() {
	c.sess.Reconcile(nil)
	c.publishItems(StateReady, "")
}

func count(items []transfer.Item) Counts {
	var k Counts
	k.Total = len(items)
	for _, it := range items {
		switch it.Status {
		case transfer.StatusReceived:
			k.Received++
		case transfer.StatusMissing, transfer.StatusQueued, transfer.StatusDownloading:
			k.Missing++
			k.MissingBytes += it.Size
		case transfer.StatusFailed:
			k.Failed++
		case transfer.StatusExcluded:
			k.Excluded++
		}
	}
	return k
}

func (c *Controller) publishItems(st State, msg string) {
	items := c.sess.Items()
	k := count(items)
	if msg == "" {
		msg = fmt.Sprintf("Connected to %s. %d files on camera: %d received, %d missing (%s), %d excluded by filter",
			c.sess.CameraName, k.Total, k.Received, k.Missing, HumanBytes(k.MissingBytes), k.Excluded)
		if k.Failed > 0 {
			msg += fmt.Sprintf(", %d failed", k.Failed)
		}
		msg += "."
	}
	c.update(func(s *Snapshot) {
		s.State = st
		s.Items = items
		s.Counts = k
		s.Message = msg
		if st != StateDownloading {
			s.Progress = nil
		}
	})
}

func (c *Controller) download(ctx context.Context, keys []string) transfer.Result {
	dctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancelDL = cancel
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.cancelDL = nil
		c.mu.Unlock()
	}()
	c.publishItems(StateDownloading, "Downloading…")
	meter := &transfer.RateMeter{Window: 10 * time.Second}
	meter.Add(time.Now(), 0)
	res, err := c.sess.Download(dctx, keys, func(p transfer.Progress) {
		now := time.Now()
		if p.Done != p.Total && now.Sub(c.lastProg) < 150*time.Millisecond {
			return
		}
		c.lastProg = now
		meter.Add(now, p.Transferred)
		p.Speed = meter.Rate()
		p.ETA = meter.ETA(p.QueueBytes - p.BytesAll)
		pp := p
		items := c.sess.Items()
		c.update(func(s *Snapshot) {
			s.Progress = &pp
			s.Items = items
			s.Counts = count(items)
			s.Message = fmt.Sprintf("Downloading %d of %d: %s", p.Index, p.Count, p.Item.Name)
		})
	})
	rate := ""
	if res.Duration > time.Second && res.Bytes > 0 {
		rate = fmt.Sprintf(" at %s/s", HumanBytes(uint64(float64(res.Bytes)/res.Duration.Seconds())))
	}
	summary := fmt.Sprintf("Downloaded %d file(s), %s in %s%s", res.Downloaded, HumanBytes(uint64(res.Bytes)), res.Duration.Round(time.Second), rate)
	if res.Failed > 0 {
		summary += fmt.Sprintf("; %d failed", res.Failed)
	}
	if errors.Is(err, context.Canceled) {
		summary += " (cancelled)"
	}
	c.logf("%s", summary)
	if c.OnNotify != nil && (res.Downloaded > 0 || res.Failed > 0) {
		c.OnNotify("Transfer finished", summary)
	}
	// the connection may have dropped during the transfer
	select {
	case <-c.sess.Client.Done():
		c.dropSession("Camera disconnected. " + summary)
		return res
	default:
	}
	c.reconcileNoIndex()
	return res
}

// HumanBytes formats a byte count.
func HumanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// HumanDuration formats a remaining-time estimate ("45 s", "3 min", "1 h 05 min").
func HumanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	sec := int(d.Round(time.Second).Seconds())
	switch {
	case sec < 60:
		return fmt.Sprintf("%d s", sec)
	case sec < 3600:
		m, s := sec/60, sec%60
		if m >= 10 {
			return fmt.Sprintf("%d min", (sec+30)/60)
		}
		return fmt.Sprintf("%d min %02d s", m, s)
	}
	return fmt.Sprintf("%d h %02d min", sec/3600, (sec%3600)/60)
}

// ProgressText describes a running transfer in one line.
func ProgressText(p *transfer.Progress) string {
	t := fmt.Sprintf("File %d of %d", p.Index, p.Count)
	if p.Total > 0 {
		t += fmt.Sprintf(" (%d%%)", p.Done*100/p.Total)
	}
	t += fmt.Sprintf(" · %s of %s", HumanBytes(uint64(p.BytesAll)), HumanBytes(uint64(p.QueueBytes)))
	if p.Speed > 0 {
		t += fmt.Sprintf(" · %s/s · about %s left", HumanBytes(uint64(p.Speed)), HumanDuration(p.ETA))
	} else {
		t += " · measuring speed…"
	}
	return t
}
