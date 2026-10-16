// Package ssdp discovers Sony cameras that are in "Send to Computer" mode.
//
// When the mode is started, the camera announces a UPnP device of type
// urn:microsoft-com:device:mtp:1 with an MtpNullService via SSDP NOTIFY
// messages to 239.255.255.250:1900 and starts its PTP/IP server.
package ssdp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	groupAddr   = "239.255.255.250:1900"
	ServiceType = "urn:microsoft-com:service:MtpNullService:1"
)

// Camera describes a discovered camera.
type Camera struct {
	IP           string
	Location     string
	FriendlyName string
	Manufacturer string
	Model        string
	UDN          string
}

// Message is a parsed SSDP NOTIFY or M-SEARCH response.
type Message struct {
	StartLine string
	Headers   map[string]string // upper-case keys
}

// ParseMessage parses an SSDP datagram.
func ParseMessage(b []byte) (*Message, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	if !sc.Scan() {
		return nil, fmt.Errorf("empty")
	}
	m := &Message{StartLine: strings.TrimSpace(sc.Text()), Headers: map[string]string{}}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			break
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		m.Headers[strings.ToUpper(strings.TrimSpace(line[:i]))] = strings.TrimSpace(line[i+1:])
	}
	return m, nil
}

// IsCameraAlive reports whether the message is an alive announcement (or a
// search response) for the MTP null service a Sony camera publishes.
func (m *Message) IsCameraAlive() bool {
	if m.Headers["NTS"] == "ssdp:byebye" {
		return false
	}
	if !strings.HasPrefix(m.StartLine, "NOTIFY") && !strings.HasPrefix(m.StartLine, "HTTP/1.1 200") {
		return false
	}
	all := m.Headers["NT"] + " " + m.Headers["ST"] + " " + m.Headers["USN"]
	return strings.Contains(all, "MtpNullService") || strings.Contains(all, "urn:microsoft-com:device:mtp:1")
}

type deviceDesc struct {
	Device struct {
		DeviceType   string `xml:"deviceType"`
		FriendlyName string `xml:"friendlyName"`
		Manufacturer string `xml:"manufacturer"`
		ModelName    string `xml:"modelName"`
		UDN          string `xml:"UDN"`
	} `xml:"device"`
}

// ParseDescription parses the UPnP device description XML.
func ParseDescription(r io.Reader) (*Camera, error) {
	var d deviceDesc
	if err := xml.NewDecoder(r).Decode(&d); err != nil {
		return nil, err
	}
	return &Camera{
		FriendlyName: d.Device.FriendlyName,
		Manufacturer: d.Device.Manufacturer,
		Model:        d.Device.ModelName,
		UDN:          d.Device.UDN,
	}, nil
}

// Fetch downloads and parses the device description.
func Fetch(ctx context.Context, location string) (*Camera, error) {
	b, err := FetchRaw(ctx, location)
	if err != nil {
		return nil, err
	}
	cam, err := ParseDescription(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	cam.Location = location
	return cam, nil
}

// FetchRaw downloads the device description XML.
func FetchRaw(ctx context.Context, location string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	// The camera's tiny web server is on the LAN: never use a proxy.
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("description: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// Listener watches the network for cameras.
type Listener struct {
	// OnCamera is called (from a background goroutine) for every verified
	// camera announcement. It may be called repeatedly for the same camera.
	OnCamera func(Camera)
	// OnDatagram receives every alive announcement of an MTP device before
	// verification (diagnostics).
	OnDatagram func(b []byte, srcIP string)
	// OnByeBye is called when a camera leaves.
	OnByeBye func(ip string)
	Logf     func(string, ...any)
	// AcceptAnyManufacturer disables the "Sony" check (for testing).
	AcceptAnyManufacturer bool

	mu      sync.Mutex
	conns   map[string]*net.UDPConn
	pending map[string]time.Time
}

func (l *Listener) logf(f string, a ...any) {
	if l.Logf != nil {
		l.Logf(f, a...)
	}
}

// Run listens until ctx is cancelled. It joins the multicast group on every
// IPv4 interface (re-checking periodically, so Wi-Fi reconnects are picked
// up) and also sends M-SEARCH requests.
func (l *Listener) Run(ctx context.Context) error {
	l.mu.Lock()
	l.conns = map[string]*net.UDPConn{}
	if l.pending == nil {
		l.pending = map[string]time.Time{}
	}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		for _, c := range l.conns {
			c.Close()
		}
		l.mu.Unlock()
	}()
	search, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err == nil {
		go l.readLoop(ctx, search, "search")
		defer search.Close()
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		l.refreshInterfaces(ctx)
		if search != nil {
			l.sendSearch(search)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (l *Listener) refreshInterfaces(ctx context.Context) {
	ifs, err := net.Interfaces()
	if err != nil {
		return
	}
	group := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	seen := map[string]bool{}
	for i := range ifs {
		ifc := ifs[i]
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagMulticast == 0 {
			continue
		}
		if !hasIPv4(&ifc) {
			continue
		}
		key := fmt.Sprintf("%d/%s", ifc.Index, ifc.Name)
		seen[key] = true
		l.mu.Lock()
		_, ok := l.conns[key]
		l.mu.Unlock()
		if ok {
			continue
		}
		c, err := net.ListenMulticastUDP("udp4", &ifc, group)
		if err != nil {
			l.logf("SSDP: cannot listen on %s: %v", ifc.Name, err)
			continue
		}
		_ = c.SetReadBuffer(256 * 1024)
		l.mu.Lock()
		l.conns[key] = c
		l.mu.Unlock()
		l.logf("SSDP: listening on %s", ifc.Name)
		go l.readLoop(ctx, c, key)
	}
	l.mu.Lock()
	for k, c := range l.conns {
		if !seen[k] {
			c.Close()
			delete(l.conns, k)
		}
	}
	l.mu.Unlock()
}

func hasIPv4(ifc *net.Interface) bool {
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			return true
		}
	}
	return ifc.Flags&net.FlagLoopback != 0
}

func (l *Listener) sendSearch(c *net.UDPConn) {
	msg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + ServiceType + "\r\n\r\n"
	dst, _ := net.ResolveUDPAddr("udp4", groupAddr)
	_, _ = c.WriteToUDP([]byte(msg), dst)
}

func (l *Listener) readLoop(ctx context.Context, c *net.UDPConn, key string) {
	buf := make([]byte, 4096)
	for {
		n, src, err := c.ReadFromUDP(buf)
		if err != nil {
			l.mu.Lock()
			if l.conns[key] == c {
				delete(l.conns, key)
			}
			l.mu.Unlock()
			return
		}
		l.HandleDatagram(ctx, buf[:n], src.IP.String())
	}
}

// HandleDatagram processes one SSDP datagram received from srcIP.
func (l *Listener) HandleDatagram(ctx context.Context, b []byte, srcIP string) {
	m, err := ParseMessage(b)
	if err != nil {
		return
	}
	if m.Headers["NTS"] == "ssdp:byebye" && strings.Contains(m.Headers["USN"], "uuid:") {
		if l.OnByeBye != nil && strings.Contains(m.Headers["NT"]+m.Headers["USN"], "mtp") {
			l.OnByeBye(srcIP)
		}
		return
	}
	if !m.IsCameraAlive() {
		return
	}
	if l.OnDatagram != nil {
		l.OnDatagram(append([]byte(nil), b...), srcIP)
	}
	loc := m.Headers["LOCATION"]
	if loc == "" {
		return
	}
	// The LOCATION host must be the sender (avoids being pointed elsewhere).
	if u, err := url.Parse(loc); err != nil || (u.Hostname() != srcIP && !isLoopback(srcIP)) {
		return
	}
	l.mu.Lock()
	if l.pending == nil {
		l.pending = map[string]time.Time{}
	}
	if t, ok := l.pending[srcIP]; ok && time.Since(t) < 3*time.Second {
		l.mu.Unlock()
		return
	}
	l.pending[srcIP] = time.Now()
	l.mu.Unlock()
	go func() {
		cam, err := Fetch(ctx, loc)
		if err != nil {
			l.logf("SSDP: %s announced MTP service but description failed: %v", srcIP, err)
			return
		}
		if !l.AcceptAnyManufacturer && !strings.Contains(strings.ToLower(cam.Manufacturer), "sony") {
			return
		}
		cam.IP = srcIP
		if l.OnCamera != nil {
			l.OnCamera(*cam)
		}
	}()
}

func isLoopback(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.IsLoopback()
}
