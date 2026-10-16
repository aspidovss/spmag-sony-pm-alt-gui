// wpr is the command-line companion of SPMAG Wifi Photo Receiver. It is useful for
// troubleshooting a camera and for scripting:
//
//	wpr discover            listen for cameras in "Send to Computer" mode
//	wpr list   [-ip IP]     connect and show which files are received/missing
//	wpr pull   [-ip IP]     download missing files, then tell the camera "done"
//	wpr guid                show this PC's IDs (own and PlayMemories-style)
//	wpr usb                 list USB drives (Windows) for pairing
//	wpr pair   -drive PATH  write this PC's ID into the camera over USB (admin)
//	wpr capture [-ip IP]    record the camera's protocol answers as test fixtures
//	wpr fakecam             run a simulated camera on this PC for testing
//
// Settings (save folder, filters, ID) are shared with the GUI.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/app"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/capture"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/config"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/fakecam"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/pairing"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ssdp"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/store"
	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/transfer"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: wpr <command> [flags]

commands:
  discover   listen for cameras (Ctrl+C to stop)
  list       connect and show received / missing files
  pull       download missing files
  guid       show this PC's IDs
  usb        list USB drives (Windows)
  pair       write this PC's ID into the camera over USB (Windows, admin)
  capture    record the camera's answers as test fixtures (serial/MAC masked)
  fakecam    run a simulated camera for testing

run "wpr <command> -h" for flags`)
	os.Exit(2)
}

func logf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+f+"\n", a...)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch cmd {
	case "discover":
		err = cmdDiscover(ctx, args)
	case "list":
		err = cmdList(ctx, args, false)
	case "pull":
		err = cmdList(ctx, args, true)
	case "guid":
		err = cmdGUID(args)
	case "usb":
		err = cmdUSB()
	case "pair":
		err = cmdPair(args)
	case "capture":
		err = cmdCapture(ctx, args)
	case "fakecam":
		err = cmdFakecam(ctx, args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdDiscover(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	timeout := fs.Duration("t", 0, "stop after this long (0 = until Ctrl+C)")
	verbose := fs.Bool("v", false, "verbose")
	_ = fs.Parse(args)
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	seen := map[string]bool{}
	l := &ssdp.Listener{OnCamera: func(c ssdp.Camera) {
		if !seen[c.IP] {
			seen[c.IP] = true
			fmt.Printf("%s\t%s\t%s\t%s\n", c.IP, c.FriendlyName, c.Model, c.Location)
		}
	}}
	if *verbose {
		l.Logf = logf
	}
	logf("Listening for cameras. Start MENU → Wireless → Send to Computer on the camera.")
	return l.Run(ctx)
}

func findCamera(ctx context.Context, ip string) (string, error) {
	if ip != "" {
		return ip, nil
	}
	logf("Waiting for a camera (use -ip to skip discovery)…")
	found := make(chan string, 1)
	dctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	l := &ssdp.Listener{OnCamera: func(c ssdp.Camera) {
		logf("Found %s at %s", c.FriendlyName, c.IP)
		select {
		case found <- c.IP:
		default:
		}
	}}
	go l.Run(dctx)
	select {
	case ip := <-found:
		return ip, nil
	case <-dctx.Done():
		return "", fmt.Errorf("no camera found")
	}
}

func cmdList(ctx context.Context, args []string, pull bool) error {
	name := "list"
	if pull {
		name = "pull"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	ip := fs.String("ip", "", "camera IP (optionally ip:port); default: discover")
	dest := fs.String("dest", "", "save folder (default: from settings)")
	all := fs.Bool("all", false, "list: show received files too")
	keep := fs.Bool("keep", false, "pull: do not tell the camera the transfer is finished")
	_ = fs.Parse(args)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *dest != "" {
		cfg.DestDir = *dest
	}
	db, err := store.Open(filepath.Join(config.Dir(), "received.json"))
	if err != nil {
		return err
	}
	host, err := findCamera(ctx, *ip)
	if err != nil {
		return err
	}
	s, err := transfer.Connect(ctx, host, cfg, db, logf)
	if err != nil {
		return err
	}
	finish := false
	defer func() { s.End(finish) }()
	if err := s.Scan(ctx, nil); err != nil {
		return err
	}
	var idx transfer.DestIndex
	if cfg.MatchExisting {
		idx = transfer.BuildDestIndex(cfg.DestDir)
	}
	s.Reconcile(idx)
	if !pull {
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "FILE\tTYPE\tTAKEN\tSIZE\tSTATUS\tLOCAL")
		var counts [6]int
		for _, it := range s.Items() {
			counts[it.Status]++
			if !*all && it.Status == transfer.StatusReceived {
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", it.Name, it.Kind, it.Captured.Format("2006-01-02 15:04"),
				app.HumanBytes(it.Size), it.Status, strings.TrimSpace(it.LocalPath+" "+it.Note))
		}
		tw.Flush()
		fmt.Printf("\n%d files: %d received, %d missing, %d excluded by filter\n",
			len(s.Items()), counts[transfer.StatusReceived], counts[transfer.StatusMissing], counts[transfer.StatusExcluded])
		return db.Save()
	}
	res, err := s.Download(ctx, nil, func(p transfer.Progress) {
		if p.Item.Status == transfer.StatusReceived {
			fmt.Printf("[%d/%d] %s  %s\n", p.Index, p.Count, p.Item.Name, app.HumanBytes(uint64(p.Total)))
		}
	})
	fmt.Printf("Downloaded %d file(s), %s in %s; %d failed\n", res.Downloaded, app.HumanBytes(uint64(res.Bytes)), res.Duration.Round(time.Second), res.Failed)
	finish = err == nil && res.Failed == 0 && !*keep
	return err
}

func cmdGUID(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Println("own ID:", cfg.GUID)
	ip := "192.168.0.1"
	if len(args) > 0 {
		ip = args[0]
	}
	for _, g := range pairing.PMHGuidsFor(ip) {
		fmt.Println("PlayMemories-style ID:", g)
	}
	return nil
}

func cmdUSB() error {
	ds, err := pairing.ListUSBDrives()
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Println("no USB drives found")
	}
	for _, d := range ds {
		mark := ""
		if d.IsSony() {
			mark = "  <- Sony"
		}
		fmt.Printf("%s\t%s\t%s%s\n", d.Path, d.Vendor, d.Product, mark)
	}
	return nil
}

func cmdPair(args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	drive := fs.String("drive", "", `drive path, e.g. \\.\PhysicalDrive2 (default: the only Sony drive)`)
	_ = fs.Parse(args)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *drive == "" {
		ds, err := pairing.ListUSBDrives()
		if err != nil {
			return err
		}
		for _, d := range ds {
			if d.IsSony() {
				if *drive != "" {
					return fmt.Errorf("several Sony drives found; choose one with -drive")
				}
				*drive = d.Path
			}
		}
		if *drive == "" {
			return fmt.Errorf("no Sony camera in Mass Storage mode found")
		}
	}
	fmt.Printf("Writing ID %s to %s…\n", cfg.GUID, *drive)
	if err := pairing.WriteGUID(*drive, cfg.OwnGUID(), cfg.EffectiveHostName()); err != nil {
		return err
	}
	fmt.Println("OK. Disconnect USB and start Send to Computer on the camera.")
	return nil
}

func cmdFakecam(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fakecam", flag.ExitOnError)
	pairs := fs.Int("pairs", 12, "number of JPEG+ARW pairs on the simulated card")
	model := fs.String("model", "ILCE-77M2", "model name")
	bind := fs.String("ip", "", "local IP to announce (default: the adapter's IP)")
	strict := fs.Bool("strict", false, "only accept this PC's own ID (to test pairing errors)")
	_ = fs.Parse(args)
	cam := fakecam.New(*model, *pairs)
	cam.Logf = logf
	if *strict {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cam.AllowedGUID[cfg.OwnGUID()] = true
	}
	ip := *bind
	if ip == "" {
		lip, err := pairing.LocalIPToward("239.255.255.250")
		if err != nil {
			return err
		}
		ip = lip.String()
	}
	if err := cam.Start(ip + ":15740"); err != nil {
		return fmt.Errorf("PTP/IP listener: %w", err)
	}
	defer cam.Stop()
	loc, err := cam.StartHTTP(ip + ":0")
	if err != nil {
		return err
	}
	logf("Simulated %s with %d files at %s (PTP/IP %s). Ctrl+C to stop.", *model, len(cam.Files), ip, cam.Addr())
	return cam.Announce(ctx, loc, 5*time.Second)
}

func cmdCapture(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	ip := fs.String("ip", "", "camera IP (default: wait for the camera's announcement)")
	out := fs.String("out", "testdata", "output folder; files go to <out>/<model>/")
	raw := fs.Bool("raw", false, "do not mask serial number, MAC and IP (keep private!)")
	_ = fs.Parse(args)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var notify []byte
	host := *ip
	if host == "" {
		logf("Waiting for the camera. Start MENU → Wireless → Send to Computer on it.")
		type found struct {
			ip     string
			notify []byte
		}
		ch := make(chan found, 1)
		var mu sync.Mutex
		var last []byte
		dctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		l := &ssdp.Listener{
			OnDatagram: func(b []byte, src string) { mu.Lock(); last = b; mu.Unlock() },
			OnCamera: func(c ssdp.Camera) {
				mu.Lock()
				n := last
				mu.Unlock()
				select {
				case ch <- found{c.IP, n}:
				default:
				}
			},
		}
		go l.Run(dctx)
		select {
		case f := <-ch:
			host, notify = f.ip, f.notify
			logf("Found camera at %s", host)
		case <-dctx.Done():
			return fmt.Errorf("no camera found")
		}
		cancel()
	}
	// Make sure automatic transfers in the GUI do not interfere.
	logf("Connecting… (close SPMAG Wifi Photo Receiver first if it is running)")
	res, err := capture.Run(ctx, host, notify, cfg, *out, *raw, logf)
	if err != nil {
		return err
	}
	fmt.Printf("\nSaved %d files for %s to:\n  %s\n", len(res.Files), res.Model, res.Dir)
	for _, f := range res.Files {
		fmt.Println("   ", f)
	}
	if *raw {
		fmt.Println("\nWARNING: -raw output contains the camera serial number and MAC address.")
	} else {
		fmt.Println("\nSerial number, camera MAC and IP are masked. Zip this folder and send it.")
	}
	return nil
}
