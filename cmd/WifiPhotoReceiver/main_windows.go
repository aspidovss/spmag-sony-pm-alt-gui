//go:build windows

// SPMAG Wifi Photo Receiver — receives photos from Sony cameras over Wi-Fi
// ("Send to Computer"), a lightweight replacement for the Wi-Fi import part
// of PlayMemories Home.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/app"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/i18n"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/icon"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/pairing"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/store"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/transfer"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/winutil"
)

var version = "0.1.0"

const firewallRuleName = "SPMAG Wifi Photo Receiver (camera discovery)"

// names used before the rename to SPMAG (cleaned up automatically)
const (
	legacyFirewallRule = "WiFi Photo Receiver (camera discovery)"
	legacyAutostart    = "WifiPhotoReceiver"
	autostartName      = "SPMAG Wifi Photo Receiver"
)

func main() {
	args := os.Args[1:]
	// Helper modes run elevated by the GUI.
	if len(args) >= 1 {
		switch args[0] {
		case "--pair-usb":
			os.Exit(helperPairUSB(args[1:]))
		case "--add-firewall-rule":
			os.Exit(helperFirewall(args[1:]))
		}
	}
	cfg, cfgErr := config.Load()
	i18n.Set(cfg.Language)
	// After a language change the old process is still shutting down.
	attempts := 1
	if contains(args, "--restarted") {
		attempts = 50
	}
	got := false
	for i := 0; i < attempts && !got; i++ {
		if got = winutil.SingleInstance("WifiPhotoReceiver-7f3c"); !got {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if !got {
		if !winutil.ActivateWindow(config.AppName) {
			walk.MsgBox(nil, config.AppName, i18n.T("app.alreadyRunning", config.AppName), walk.MsgBoxIconInformation)
		}
		return
	}
	if cfgErr != nil {
		walk.MsgBox(nil, config.AppName, i18n.T("app.settingsReadErr", cfgErr.Error()), walk.MsgBoxIconWarning)
	}
	var err error
	startHidden := cfg.StartMinimized && contains(args, "--tray")
	db, err := store.Open(filepath.Join(config.Dir(), "received.json"))
	if err != nil {
		walk.MsgBox(nil, config.AppName, i18n.T("app.dbErr", err.Error()), walk.MsgBoxIconError)
		return
	}
	ui := &gui{cfg: cfg, db: db}
	ui.run(startHidden)
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------

type gui struct {
	cfg  config.Settings
	db   *store.DB
	ctl  *app.Controller
	mw   *walk.MainWindow
	ni   *walk.NotifyIcon
	logF *os.File

	// transfer page
	statusLbl, countsLbl, progLbl      *walk.Label
	detailLbl                          *walk.TextLabel
	iconIdle, iconBusy                 *walk.Icon
	lastBusy                           int
	prog                               *walk.ProgressBar
	table                              *walk.TableView
	model                              *fileModel
	filterCB                           *walk.ComboBox
	btnMissing, btnSelected, btnCancel *walk.PushButton
	btnRescan, btnFinish               *walk.PushButton
	// settings page
	destLE, patternExample, guidLE, hostLE *walk.LineEdit
	patternCB                              *walk.ComboBox
	cbJPEG, cbRAW, cbVideo, cbOther        *walk.CheckBox
	cbAuto, cbFinish, cbMatch, cbStartWin  *walk.CheckBox
	cbStartMin, cbNotify, cbPMH            *walk.CheckBox
	// log page
	logTE  *walk.TextEdit
	langCB *walk.ComboBox

	pauseAction *walk.Action
	exiting     bool
	hiddenOnce  bool

	refreshMu      sync.Mutex
	refreshPending bool
	logMu          sync.Mutex
	logLines       []string
}

var patterns = []string{
	`{yyyy}\{yyyy}-{MM}-{dd}`,
	`{yyyy}-{MM}-{dd}`,
	`{yyyy}\{MM}`,
	`{camera}\{yyyy}-{MM}-{dd}`,
	``,
}

func (g *gui) run(startHidden bool) {
	g.model = newFileModel()
	g.openLogFile()
	appIcon, _ := walk.NewIconFromImageForDPI(icon.Draw(256, icon.Lens), 96)

	mainWin := MainWindow{
		AssignTo: &g.mw,
		Title:    config.AppName,
		Icon:     appIcon,
		Size:     Size{Width: 1040, Height: 680},
		MinSize:  Size{Width: 720, Height: 480},
		Visible:  !startHidden,
		Layout:   VBox{MarginsZero: false},
		Children: []Widget{
			TabWidget{
				Pages: []TabPage{
					g.transferPage(),
					g.settingsPage(),
					g.logPage(),
				},
			},
		},
	}
	if err := mainWin.Create(); err != nil {
		walk.MsgBox(nil, config.AppName, i18n.T("app.windowErr", err.Error()), walk.MsgBoxIconError)
		return
	}
	g.loadSettingsIntoForm()
	g.setupTray(appIcon)

	g.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		// Only the user's close button hides to the tray; Exit, logoff and
		// shutdown really close the program.
		if g.exiting || reason != walk.CloseReasonUser {
			return
		}
		*canceled = true
		g.mw.Hide()
		if !g.hiddenOnce && g.ni != nil {
			g.hiddenOnce = true
			_ = g.ni.ShowInfo(config.AppName, i18n.T("tray.hidden"))
		}
	})

	g.ctl = app.New(g.cfg, g.db)
	g.ctl.OnLog = g.appendLog
	g.ctl.OnChange = g.scheduleRefresh
	g.ctl.OnNotify = func(title, text string) {
		if !g.ctl.Settings().Notifications || g.ni == nil {
			return
		}
		g.mw.Synchronize(func() { _ = g.ni.ShowInfo(title, text) })
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.ctl.Start(ctx)
	g.appendLog(time.Now().Format("15:04:05 ") + fmt.Sprintf("%s %s © 2026 AspidovSS — %s", config.AppName, version, config.RepoURL))
	g.appendLog(time.Now().Format("15:04:05 ") + "Settings and database: " + config.Dir())
	g.warnAboutPMH()
	g.refresh()

	g.mw.Run()
	cancel()
	select {
	case <-g.ctl.Stopped():
	case <-time.After(5 * time.Second):
	}
	if g.ni != nil {
		g.ni.Dispose()
	}
	if g.logF != nil {
		g.logF.Close()
	}
}

func (g *gui) transferPage() TabPage {
	return TabPage{
		Title:  i18n.T("tab.transfer"),
		Layout: VBox{},
		Children: []Widget{
			Label{AssignTo: &g.statusLbl, Font: Font{PointSize: 11, Bold: true}, Text: i18n.T("status.starting")},
			TextLabel{AssignTo: &g.detailLbl, Text: " "},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					ProgressBar{AssignTo: &g.prog, MinValue: 0, MaxValue: 1000, StretchFactor: 2},
					Label{AssignTo: &g.progLbl, Text: "", StretchFactor: 3},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{AssignTo: &g.btnMissing, Text: i18n.T("btn.downloadMissing"), OnClicked: func() { g.ctl.DownloadMissing() }},
					PushButton{AssignTo: &g.btnSelected, Text: i18n.T("btn.downloadSelected"), OnClicked: g.downloadSelected},
					PushButton{AssignTo: &g.btnCancel, Text: i18n.T("btn.stop"), OnClicked: func() { g.ctl.CancelDownload() }},
					PushButton{AssignTo: &g.btnRescan, Text: i18n.T("btn.recheck"), OnClicked: func() { g.ctl.Rescan() }},
					PushButton{AssignTo: &g.btnFinish, Text: i18n.T("btn.finish"), OnClicked: func() { g.ctl.Disconnect(true) }},
					HSpacer{},
					PushButton{Text: i18n.T("btn.connectIP"), OnClicked: g.connectIP},
					PushButton{Text: i18n.T("btn.openFolder"), OnClicked: func() { winutil.OpenFolder(g.ctl.Settings().DestDir) }},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{Text: i18n.T("lbl.show")},
					ComboBox{
						AssignTo:     &g.filterCB,
						Model:        filterNames(),
						CurrentIndex: 0,
						OnCurrentIndexChanged: func() {
							g.model.setFilter(g.filterCB.CurrentIndex())
						},
					},
					HSpacer{},
					Label{AssignTo: &g.countsLbl, Text: ""},
				},
			},
			TableView{
				AssignTo:            &g.table,
				Model:               g.model,
				MultiSelection:      true,
				AlternatingRowBG:    true,
				ColumnsOrderable:    true,
				LastColumnStretched: true,
				Columns: []TableViewColumn{
					{Title: i18n.T("col.file"), Width: 120},
					{Title: i18n.T("col.type"), Width: 55},
					{Title: i18n.T("col.taken"), Width: 135},
					{Title: i18n.T("col.size"), Width: 75, Alignment: AlignFar},
					{Title: i18n.T("col.status"), Width: 130},
					{Title: i18n.T("col.folder"), Width: 95},
					{Title: i18n.T("col.saved"), Width: 300},
				},
				StyleCell: func(style *walk.CellStyle) {
					if style.Col() != 4 {
						return
					}
					switch g.model.statusOf(style.Row()) {
					case transfer.StatusReceived:
						style.TextColor = walk.RGB(0x1E, 0x7B, 0x34)
					case transfer.StatusMissing:
						style.TextColor = walk.RGB(0xB2, 0x6B, 0x00)
					case transfer.StatusFailed:
						style.TextColor = walk.RGB(0xC0, 0x1C, 0x28)
					case transfer.StatusDownloading, transfer.StatusQueued:
						style.TextColor = walk.RGB(0x1B, 0x5F, 0xA8)
					case transfer.StatusExcluded:
						style.TextColor = walk.RGB(0x80, 0x80, 0x80)
					}
				},
				OnItemActivated: func() {
					if i := g.table.CurrentIndex(); i >= 0 && i < len(g.model.rows) && g.model.rows[i].LocalPath != "" {
						winutil.OpenFolder(g.model.rows[i].LocalPath)
					}
				},
				ContextMenuItems: []MenuItem{
					Action{Text: i18n.T("menu.downloadSel"), OnTriggered: g.downloadSelected},
					Action{Text: i18n.T("menu.showInFolder"), OnTriggered: func() {
						if i := g.table.CurrentIndex(); i >= 0 && i < len(g.model.rows) && g.model.rows[i].LocalPath != "" {
							winutil.OpenFolder(g.model.rows[i].LocalPath)
						}
					}},
					Separator{},
					Action{Text: i18n.T("menu.markNotReceived"), OnTriggered: func() {
						keys := g.model.keys(g.table.SelectedIndexes())
						if len(keys) > 0 {
							g.ctl.Forget(keys)
						}
					}},
				},
			},
		},
	}
}

func (g *gui) settingsPage() TabPage {
	return TabPage{
		Title:  i18n.T("tab.settings"),
		Layout: VBox{},
		Children: []Widget{
			ScrollView{
				HorizontalFixed: true,
				Layout:          VBox{},
				Children: []Widget{
					GroupBox{
						Title:  i18n.T("set.where"),
						Layout: Grid{Columns: 3},
						Children: []Widget{
							Label{Text: i18n.T("set.folder")},
							LineEdit{AssignTo: &g.destLE},
							PushButton{Text: i18n.T("set.browse"), OnClicked: g.browseDest},
							Label{Text: i18n.T("set.subfolders")},
							ComboBox{AssignTo: &g.patternCB, Editable: true, Model: patternDisplay(), OnEditingFinished: g.updatePatternExample, OnCurrentIndexChanged: g.updatePatternExample},
							Label{Text: i18n.T("set.patternHelp")},
							Label{Text: i18n.T("set.example")},
							LineEdit{AssignTo: &g.patternExample, ReadOnly: true, ColumnSpan: 2},
						},
					},
					GroupBox{
						Title:  i18n.T("set.types"),
						Layout: HBox{},
						Children: []Widget{
							CheckBox{AssignTo: &g.cbJPEG, Text: i18n.T("set.jpeg")},
							CheckBox{AssignTo: &g.cbRAW, Text: i18n.T("set.raw")},
							CheckBox{AssignTo: &g.cbVideo, Text: i18n.T("set.video")},
							CheckBox{AssignTo: &g.cbOther, Text: i18n.T("set.other")},
							HSpacer{},
						},
					},
					GroupBox{
						Title:  i18n.T("set.behaviour"),
						Layout: VBox{},
						Children: []Widget{
							CheckBox{AssignTo: &g.cbAuto, Text: i18n.T("set.auto")},
							CheckBox{AssignTo: &g.cbFinish, Text: i18n.T("set.finish")},
							CheckBox{AssignTo: &g.cbMatch, Text: i18n.T("set.match")},
							CheckBox{AssignTo: &g.cbStartWin, Text: i18n.T("set.startWin")},
							CheckBox{AssignTo: &g.cbStartMin, Text: i18n.T("set.startMin")},
							CheckBox{AssignTo: &g.cbNotify, Text: i18n.T("set.notify")},
						},
					},
					GroupBox{
						Title:  i18n.T("set.pairing"),
						Layout: Grid{Columns: 3},
						Children: []Widget{
							Label{Text: i18n.T("set.guid")},
							LineEdit{AssignTo: &g.guidLE, ReadOnly: true},
							PushButton{Text: i18n.T("set.newID"), OnClicked: g.newGUID},
							Label{Text: i18n.T("set.hostName")},
							LineEdit{AssignTo: &g.hostLE, CueBanner: hostnameOr("PC")},
							Label{Text: ""},
							CheckBox{AssignTo: &g.cbPMH, ColumnSpan: 3, Text: i18n.T("set.pmh")},
							Composite{
								ColumnSpan: 3,
								Layout:     HBox{MarginsZero: true},
								Children: []Widget{
									PushButton{Text: i18n.T("set.pairUSB"), OnClicked: g.pairUSB},
									PushButton{Text: i18n.T("set.firewall"), OnClicked: g.addFirewall},
									HSpacer{},
								},
							},
							TextLabel{ColumnSpan: 3, Text: i18n.T("set.pairHelp")},
						},
					},
					GroupBox{
						Title:  i18n.T("set.language"),
						Layout: HBox{},
						Children: []Widget{
							Label{Text: i18n.T("set.languageLbl")},
							ComboBox{AssignTo: &g.langCB, Model: languageNames(), MinSize: Size{Width: 220}},
							Label{Text: i18n.T("set.langHint")},
							HSpacer{},
						},
					},
					Composite{
						Layout: HBox{},
						Children: []Widget{
							LinkLabel{
								Text: fmt.Sprintf(`%s %s · © 2026 AspidovSS · MIT License · <a id="repo" href="%s">%s</a>`,
									config.AppName, version, config.RepoURL, strings.TrimPrefix(config.RepoURL, "https://")),
								OnLinkActivated: func(link *walk.LinkLabelLink) { winutil.OpenURL(link.URL()) },
							},
							HSpacer{},
							PushButton{Text: i18n.T("set.save"), OnClicked: g.saveSettings},
						},
					},
					VSpacer{},
				},
			},
		},
	}
}

func (g *gui) logPage() TabPage {
	return TabPage{
		Title:  i18n.T("tab.log"),
		Layout: VBox{},
		Children: []Widget{
			TextEdit{AssignTo: &g.logTE, ReadOnly: true, VScroll: true, Font: Font{Family: "Consolas", PointSize: 9}},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{Text: i18n.T("log.openFolder"), OnClicked: func() { winutil.OpenFolder(config.Dir()) }},
				},
			},
		},
	}
}

func patternDisplay() []string {
	out := make([]string, len(patterns))
	for i, p := range patterns {
		if p == "" {
			out[i] = i18n.T("set.noSubfolders")
		} else {
			out[i] = p
		}
	}
	return out
}

func hostnameOr(def string) string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return def
	}
	return h
}

// ---------------------------------------------------------------------------
// tray

func (g *gui) setupTray(ic *walk.Icon) {
	ni, err := walk.NewNotifyIcon(g.mw)
	if err != nil {
		return
	}
	g.ni = ni
	_ = ni.SetIcon(ic)
	_ = ni.SetToolTip(config.AppName)
	ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			g.showWindow()
		}
	})
	ni.MessageClicked().Attach(g.showWindow)

	open := walk.NewAction()
	_ = open.SetText(i18n.T("tray.open"))
	open.Triggered().Attach(g.showWindow)
	g.pauseAction = walk.NewAction()
	_ = g.pauseAction.SetText(i18n.T("tray.pause"))
	_ = g.pauseAction.SetCheckable(true)
	g.pauseAction.Triggered().Attach(func() {
		g.ctl.SetPaused(g.pauseAction.Checked())
	})
	folder := walk.NewAction()
	_ = folder.SetText(i18n.T("tray.openSave"))
	folder.Triggered().Attach(func() { winutil.OpenFolder(g.ctl.Settings().DestDir) })
	exit := walk.NewAction()
	_ = exit.SetText(i18n.T("tray.exit"))
	exit.Triggered().Attach(func() {
		g.exiting = true
		g.mw.Close()
	})
	acts := ni.ContextMenu().Actions()
	_ = acts.Add(open)
	_ = acts.Add(folder)
	_ = acts.Add(g.pauseAction)
	_ = acts.Add(walk.NewSeparatorAction())
	_ = acts.Add(exit)
	_ = ni.SetVisible(true)
}

func (g *gui) showWindow() {
	g.mw.Show()
	g.mw.SetFocus()
	winutil.ActivateWindow(config.AppName)
}

// ---------------------------------------------------------------------------
// refresh

func (g *gui) scheduleRefresh() {
	g.refreshMu.Lock()
	if g.refreshPending {
		g.refreshMu.Unlock()
		return
	}
	g.refreshPending = true
	g.refreshMu.Unlock()
	g.mw.Synchronize(func() {
		g.refreshMu.Lock()
		g.refreshPending = false
		g.refreshMu.Unlock()
		g.refresh()
	})
}

func (g *gui) refresh() {
	if g.ctl == nil {
		return
	}
	s := g.ctl.Snapshot()
	title := s.State.Text()
	if s.Paused && s.State == app.StateWaiting {
		title = i18n.T("status.paused")
	}
	if s.Camera != "" {
		title += " — " + s.Camera
	}
	_ = g.statusLbl.SetText(title)
	detail := s.Message
	if s.IP != "" && s.State != app.StateWaiting {
		detail += "\n" + i18n.T("detail.address", s.IP)
		if s.GUIDSource != "" {
			detail += i18n.T("detail.pairing", s.GUIDSource)
		}
	}
	_ = g.detailLbl.SetText(detail)

	if s.Progress != nil && s.Progress.QueueBytes > 0 {
		// the bar shows the whole run; the label shows file, speed and time left
		g.prog.SetValue(int(s.Progress.BytesAll * 1000 / s.Progress.QueueBytes))
		_ = g.progLbl.SetText(app.ProgressText(s.Progress))
	} else {
		g.prog.SetValue(0)
		_ = g.progLbl.SetText("")
	}
	if s.Counts.Total > 0 {
		_ = g.countsLbl.SetText(i18n.T("counts",
			s.Counts.Total, s.Counts.Received, s.Counts.Missing, app.HumanBytes(s.Counts.MissingBytes), s.Counts.Failed, s.Counts.Excluded))
	} else {
		_ = g.countsLbl.SetText("")
	}

	ready := s.State == app.StateReady
	g.btnMissing.SetEnabled(ready && (s.Counts.Missing > 0 || s.Counts.Failed > 0))
	g.btnSelected.SetEnabled(ready)
	g.btnRescan.SetEnabled(ready)
	g.btnFinish.SetEnabled(ready)
	g.btnCancel.SetEnabled(s.State == app.StateDownloading)

	sel := map[string]bool{}
	for _, k := range g.model.keys(g.table.SelectedIndexes()) {
		sel[k] = true
	}
	before := len(g.model.rows)
	g.model.setItems(s.Items)
	if len(sel) > 0 && before != len(g.model.rows) {
		_ = g.table.SetSelectedIndexes(g.model.indexesOf(sel))
	}

	if g.ni != nil {
		tip := config.AppName + "\n" + title
		if p := s.Progress; p != nil && p.Speed > 0 {
			tip = config.AppName + "\n" + i18n.T("tray.tip", p.Index, p.Count,
				app.HumanBytes(uint64(p.Speed)), app.HumanDuration(p.ETA))
		}
		if len(tip) > 120 {
			tip = tip[:120]
		}
		_ = g.ni.SetToolTip(tip)
	}
	busy := 1
	if s.State == app.StateDownloading {
		busy = 2
	}
	if g.ni != nil && busy != g.lastBusy {
		g.lastBusy = busy
		if g.iconIdle == nil {
			g.iconIdle, _ = walk.NewIconFromImageForDPI(icon.Draw(32, icon.Lens), 96)
			g.iconBusy, _ = walk.NewIconFromImageForDPI(icon.Draw(32, icon.Busy), 96)
		}
		ic := g.iconIdle
		if busy == 2 {
			ic = g.iconBusy
		}
		if ic != nil {
			_ = g.ni.SetIcon(ic)
		}
	}
}

// ---------------------------------------------------------------------------
// actions

func (g *gui) downloadSelected() {
	keys := g.model.keys(g.table.SelectedIndexes())
	if len(keys) == 0 {
		walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.selectFirst"), walk.MsgBoxIconInformation)
		return
	}
	g.ctl.DownloadKeys(keys)
}

func (g *gui) connectIP() {
	ip := g.ctl.Settings().ManualIP
	if !inputBox(g.mw, i18n.T("dlg.connectTitle"), i18n.T("dlg.connectPrompt"), &ip) {
		return
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}
	cfg := g.ctl.Settings()
	cfg.ManualIP = ip
	_ = config.Save(cfg)
	g.ctl.SetSettings(cfg)
	g.ctl.ConnectIP(ip)
}

func inputBox(owner walk.Form, title, prompt string, value *string) bool {
	var dlg *walk.Dialog
	var le *walk.LineEdit
	var ok, cancel *walk.PushButton
	res, err := Dialog{
		AssignTo:      &dlg,
		Title:         title,
		DefaultButton: &ok,
		CancelButton:  &cancel,
		MinSize:       Size{Width: 380, Height: 140},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: prompt},
			LineEdit{AssignTo: &le, Text: *value},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &ok, Text: i18n.T("btn.connect"), OnClicked: func() { *value = le.Text(); dlg.Accept() }},
					PushButton{AssignTo: &cancel, Text: i18n.T("btn.cancel"), OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(owner)
	return err == nil && res == walk.DlgCmdOK
}

func (g *gui) browseDest() {
	dlg := &walk.FileDialog{Title: i18n.T("dlg.chooseFolder"), InitialDirPath: g.destLE.Text()}
	if ok, err := dlg.ShowBrowseFolder(g.mw); err == nil && ok {
		_ = g.destLE.SetText(dlg.FilePath)
		g.updatePatternExample()
	}
}

func (g *gui) currentPattern() string {
	t := strings.TrimSpace(g.patternCB.Text())
	if t == i18n.T("set.noSubfolders") {
		return ""
	}
	return t
}

func (g *gui) updatePatternExample() {
	if g.patternExample == nil || g.destLE == nil {
		return
	}
	ex := filepath.Join(g.destLE.Text(), transfer.ExpandPattern(g.currentPattern(), time.Now(), "ILCE-77M2"), "DSC01234.JPG")
	_ = g.patternExample.SetText(ex)
}

func (g *gui) loadSettingsIntoForm() {
	c := g.cfg
	_ = g.destLE.SetText(c.DestDir)
	pat := c.FolderPattern
	if pat == "" {
		pat = i18n.T("set.noSubfolders")
	}
	_ = g.patternCB.SetText(pat)
	g.cbJPEG.SetChecked(c.IncludeJPEG)
	g.cbRAW.SetChecked(c.IncludeRAW)
	g.cbVideo.SetChecked(c.IncludeVideo)
	g.cbOther.SetChecked(c.IncludeOther)
	g.cbAuto.SetChecked(c.AutoDownload)
	g.cbFinish.SetChecked(c.FinishAfterAuto)
	g.cbMatch.SetChecked(c.MatchExisting)
	g.cbStartWin.SetChecked(c.StartWithWindows)
	g.cbStartMin.SetChecked(c.StartMinimized)
	g.cbNotify.SetChecked(c.Notifications)
	g.cbPMH.SetChecked(c.AcceptPMHPairing)
	_ = g.guidLE.SetText(c.GUID)
	_ = g.hostLE.SetText(c.HostName)
	_ = g.langCB.SetCurrentIndex(languageIndex(c.Language))
	g.updatePatternExample()
}

func (g *gui) formSettings() config.Settings {
	c := g.ctl.Settings()
	c.DestDir = strings.TrimSpace(g.destLE.Text())
	c.FolderPattern = g.currentPattern()
	c.IncludeJPEG = g.cbJPEG.Checked()
	c.IncludeRAW = g.cbRAW.Checked()
	c.IncludeVideo = g.cbVideo.Checked()
	c.IncludeOther = g.cbOther.Checked()
	c.AutoDownload = g.cbAuto.Checked()
	c.FinishAfterAuto = g.cbFinish.Checked()
	c.MatchExisting = g.cbMatch.Checked()
	c.StartWithWindows = g.cbStartWin.Checked()
	c.StartMinimized = g.cbStartMin.Checked()
	c.Notifications = g.cbNotify.Checked()
	c.AcceptPMHPairing = g.cbPMH.Checked()
	c.GUID = g.guidLE.Text()
	c.HostName = strings.TrimSpace(g.hostLE.Text())
	c.Language = languageCode(g.langCB.CurrentIndex())
	return c
}

func (g *gui) saveSettings() {
	c := g.formSettings()
	oldLang := g.cfg.Language
	if c.DestDir == "" {
		walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.needFolder"), walk.MsgBoxIconWarning)
		return
	}
	if err := os.MkdirAll(c.DestDir, 0o755); err != nil {
		walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.cannotCreate", err.Error()), walk.MsgBoxIconError)
		return
	}
	_ = winutil.SetAutostart(legacyAutostart, false, "")
	if err := winutil.SetAutostart(autostartName, c.StartWithWindows, "--tray"); err != nil {
		walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.autostartErr", err.Error()), walk.MsgBoxIconWarning)
	}
	if err := config.Save(c); err != nil {
		walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.saveErr", err.Error()), walk.MsgBoxIconError)
		return
	}
	g.cfg = c
	g.ctl.SetSettings(c)
	g.appendLog(time.Now().Format("15:04:05 ") + "Settings saved")
	if c.Language != oldLang {
		walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.langRestart"), walk.MsgBoxIconInformation)
		g.restart()
	}
}

// restart starts a new copy of the program (it waits for this one to exit)
// and closes this one, so that a new interface language takes effect.
func (g *gui) restart() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if err := exec.Command(exe, "--restarted").Start(); err != nil {
		return
	}
	g.exiting = true
	g.mw.Close()
}

func (g *gui) newGUID() {
	if walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.newIDQuestion"),
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	_ = g.guidLE.SetText(pairing.NewGUID().String())
	g.saveSettings()
}

func (g *gui) pairUSB() {
	msg := i18n.T("dlg.pairInstr")
	if walk.MsgBox(g.mw, i18n.T("dlg.pairTitle"), msg, walk.MsgBoxOKCancel|walk.MsgBoxIconInformation) != walk.DlgCmdOK {
		return
	}
	g.saveSettings()
	drives, err := pairing.ListUSBDrives()
	if err != nil {
		walk.MsgBox(g.mw, config.AppName, err.Error(), walk.MsgBoxIconError)
		return
	}
	var sony []pairing.Drive
	var names []string
	for _, d := range drives {
		names = append(names, d.String())
		if d.IsSony() {
			sony = append(sony, d)
		}
	}
	if len(sony) == 0 {
		found := i18n.T("dlg.none")
		if len(names) > 0 {
			found = strings.Join(names, "\n")
		}
		walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.noCamera", found), walk.MsgBoxIconWarning)
		return
	}
	d := sony[0]
	cfg := g.ctl.Settings()
	name := cfg.EffectiveHostName()
	resFile := filepath.Join(os.TempDir(), fmt.Sprintf("wpr-pair-%d.txt", time.Now().UnixNano()))
	defer os.Remove(resFile)
	g.appendLog(time.Now().Format("15:04:05 ") + "Pairing via USB with " + d.String() + ", ID " + cfg.GUID)
	var perr error
	if winutil.IsElevated() {
		perr = pairing.WriteGUID(d.Path, cfg.OwnGUID(), name)
	} else {
		code, err := winutil.RunElevated(fmt.Sprintf(`--pair-usb "%s" "%s" "%s" "%s"`, d.Path, cfg.GUID, name, resFile))
		if err != nil {
			perr = err
		} else if code != 0 {
			b, _ := os.ReadFile(resFile)
			perr = fmt.Errorf("%s", strings.TrimSpace(string(b)))
		}
	}
	if perr != nil {
		g.appendLog(time.Now().Format("15:04:05 ") + "Pairing failed: " + perr.Error())
		walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.pairFail", perr.Error()), walk.MsgBoxIconError)
		return
	}
	g.appendLog(time.Now().Format("15:04:05 ") + "Pairing command accepted by the camera")
	walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.pairOK"), walk.MsgBoxIconInformation)
}

func (g *gui) addFirewall() {
	resFile := filepath.Join(os.TempDir(), fmt.Sprintf("wpr-fw-%d.txt", time.Now().UnixNano()))
	defer os.Remove(resFile)
	var err error
	if winutil.IsElevated() {
		err = winutil.AddFirewallRule(firewallRuleName, legacyFirewallRule)
	} else {
		code, e := winutil.RunElevated(fmt.Sprintf(`--add-firewall-rule "%s"`, resFile))
		if e != nil {
			err = e
		} else if code != 0 {
			b, _ := os.ReadFile(resFile)
			err = fmt.Errorf("%s", strings.TrimSpace(string(b)))
		}
	}
	if err != nil {
		walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.fwFail", err.Error()), walk.MsgBoxIconError)
		return
	}
	walk.MsgBox(g.mw, config.AppName, i18n.T("dlg.fwOK"), walk.MsgBoxIconInformation)
}

func (g *gui) warnAboutPMH() {
	procs := winutil.RunningProcesses("PMB", "PlayMemories")
	if len(procs) == 0 {
		return
	}
	g.appendLog(time.Now().Format("15:04:05 ") + "Note: PlayMemories Home components are running (" + strings.Join(procs, ", ") +
		"). They may grab the camera first. Close PlayMemories Home or disable its Wi-Fi import if transfers do not start here.")
}

// ---------------------------------------------------------------------------
// log

func (g *gui) openLogFile() {
	p := filepath.Join(config.Dir(), "log.txt")
	if st, err := os.Stat(p); err == nil && st.Size() > 2<<20 {
		_ = os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		g.logF = f
		fmt.Fprintf(f, "\r\n===== %s %s started %s =====\r\n", config.AppName, version, time.Now().Format(time.RFC3339))
	}
}

func (g *gui) appendLog(line string) {
	g.logMu.Lock()
	if g.logF != nil {
		fmt.Fprintf(g.logF, "%s\r\n", line)
	}
	g.logLines = append(g.logLines, line)
	if len(g.logLines) > 3000 {
		g.logLines = g.logLines[len(g.logLines)-2000:]
	}
	g.logMu.Unlock()
	if g.mw == nil {
		return
	}
	g.mw.Synchronize(func() {
		if g.logTE != nil {
			g.logTE.AppendText(line + "\r\n")
		}
	})
}

// ---------------------------------------------------------------------------
// elevated helpers

func writeResult(file, msg string) {
	if file != "" {
		_ = os.WriteFile(file, []byte(msg), 0o644)
	}
}

func helperPairUSB(args []string) int {
	if len(args) < 4 {
		return 2
	}
	path, guidStr, name, resFile := args[0], args[1], args[2], args[3]
	g, err := pairing.ParseGUID(guidStr)
	if err != nil {
		writeResult(resFile, err.Error())
		return 1
	}
	if err := pairing.WriteGUID(path, g, name); err != nil {
		writeResult(resFile, err.Error())
		return 1
	}
	writeResult(resFile, "OK")
	return 0
}

func helperFirewall(args []string) int {
	res := ""
	if len(args) > 0 {
		res = args[0]
	}
	if err := winutil.AddFirewallRule(firewallRuleName, legacyFirewallRule); err != nil {
		writeResult(res, err.Error())
		return 1
	}
	writeResult(res, "OK")
	return 0
}
