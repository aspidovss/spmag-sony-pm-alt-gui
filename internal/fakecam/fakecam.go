// Package fakecam is a small simulated Sony camera in "Send to Computer"
// mode: a PTP/IP responder plus SSDP announcements and the UPnP device
// description. It exists for automated tests and for trying the program
// without a real camera.
package fakecam

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ptpip"
)

var le = binary.LittleEndian

// File is one file on the simulated memory card.
type File struct {
	Folder string // e.g. "10161016" (Sony DCIM folder) or a date folder
	Name   string
	Data   []byte
	Taken  time.Time
	handle uint32
	parent uint32
}

// Camera is a fake camera.
type Camera struct {
	Model       string
	Serial      string
	AllowedGUID map[[16]byte]bool // empty = accept any GUID
	Files       []*File
	Logf        func(string, ...any)

	// Observed by tests.
	mu          sync.Mutex
	Ops         []uint16
	SonyStates  []uint32 // data2 values seen in 0x9280 mode 4
	LastName    string
	Connections int

	folders map[string]uint32
	ln      net.Listener
	httpSrv *http.Server
	udn     string
}

// New creates a camera with n generated JPEG+ARW pairs.
func New(model string, pairs int) *Camera {
	c := &Camera{Model: model, Serial: "0000000012345678", AllowedGUID: map[[16]byte]bool{}}
	base := time.Date(2026, 10, 14, 9, 30, 0, 0, time.Local)
	for i := 0; i < pairs; i++ {
		t := base.Add(time.Duration(i) * 37 * time.Minute)
		folder := "100MSDCF"
		n := 1000 + i
		jpg := make([]byte, 20000+i*137)
		raw := make([]byte, 60000+i*251)
		_, _ = rand.Read(jpg)
		_, _ = rand.Read(raw)
		copy(jpg, []byte{0xFF, 0xD8, 0xFF, 0xE1})
		c.Files = append(c.Files,
			&File{Folder: folder, Name: fmt.Sprintf("DSC%05d.JPG", n), Data: jpg, Taken: t},
			&File{Folder: folder, Name: fmt.Sprintf("DSC%05d.ARW", n), Data: raw, Taken: t},
		)
	}
	return c
}

func (c *Camera) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf("[fakecam] "+f, a...)
	}
}

// Addr returns the PTP/IP listen address.
func (c *Camera) Addr() string { return c.ln.Addr().String() }

func (c *Camera) index() {
	c.folders = map[string]uint32{}
	var next uint32 = 0x00010000
	for _, f := range c.Files {
		if _, ok := c.folders[f.Folder]; !ok {
			next += 0x100
			c.folders[f.Folder] = next
		}
		next++
		f.handle = next
		f.parent = c.folders[f.Folder]
	}
}

// Start listens for PTP/IP on addr (e.g. "127.0.0.1:0" or ":15740").
func (c *Camera) Start(addr string) error {
	c.index()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	c.ln = ln
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go c.serve(conn)
		}
	}()
	return nil
}

// Stop stops all listeners.
func (c *Camera) Stop() {
	if c.ln != nil {
		c.ln.Close()
	}
	if c.httpSrv != nil {
		c.httpSrv.Close()
	}
}

func (c *Camera) serve(conn net.Conn) {
	defer conn.Close()
	p, err := ptpip.ReadPacket(conn)
	if err != nil {
		return
	}
	switch p.Type {
	case ptpip.PktInitCommandRequest:
		if len(p.Payload) < 16 {
			return
		}
		var g [16]byte
		copy(g[:], p.Payload[:16])
		name, _ := ptpip.DecodeUTF16Z(p.Payload[16:])
		if len(c.AllowedGUID) > 0 && !c.AllowedGUID[g] {
			c.logf("rejecting GUID % x", g[:])
			_ = ptpip.WritePacket(conn, ptpip.PktInitFail, le.AppendUint32(nil, 1))
			return
		}
		c.mu.Lock()
		c.LastName = name
		c.Connections++
		connNum := uint32(c.Connections)
		c.mu.Unlock()
		ack := le.AppendUint32(nil, connNum)
		ack = append(ack, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 0x02, 0x00, 0x00, 0x00, 0x00, 0x01)
		ack = append(ack, ptpip.EncodeUTF16Z(c.Model)...)
		ack = le.AppendUint32(ack, 0x00010000)
		_ = ptpip.WritePacket(conn, ptpip.PktInitCommandAck, ack)
		c.commandLoop(conn)
	case ptpip.PktInitEventRequest:
		_ = ptpip.WritePacket(conn, ptpip.PktInitEventAck, nil)
		_ = ptpip.WritePacket(conn, ptpip.PktProbeRequest, nil)
		// keep the event channel open until the client closes it
		buf := make([]byte, 64)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}
}

func (c *Camera) deviceInfo() *ptpip.DeviceInfo {
	return &ptpip.DeviceInfo{
		StandardVersion: 100, VendorExtensionID: 0xFFFFFFFF, VendorExtensionVersion: 100,
		Operations:   []uint16{0x1001, 0x1002, 0x1003, 0x1004, 0x1005, 0x1007, 0x1008, 0x1009, 0x101B, 0x9803, 0x9280, 0x9281},
		Manufacturer: "Sony Corporation", Model: c.Model, DeviceVersion: "1.00", SerialNumber: c.Serial,
	}
}

func (c *Camera) findHandle(h uint32) (*File, string) {
	for _, f := range c.Files {
		if f.handle == h {
			return f, ""
		}
	}
	for name, fh := range c.folders {
		if fh == h {
			return nil, name
		}
	}
	return nil, ""
}

func (c *Camera) commandLoop(conn net.Conn) {
	session := false
	for {
		p, err := ptpip.ReadPacket(conn)
		if err != nil {
			return
		}
		if p.Type != ptpip.PktCmdRequest || len(p.Payload) < 10 {
			continue
		}
		op := le.Uint16(p.Payload[4:6])
		tid := le.Uint32(p.Payload[6:10])
		var params []uint32
		for o := 10; o+4 <= len(p.Payload); o += 4 {
			params = append(params, le.Uint32(p.Payload[o:]))
		}
		c.mu.Lock()
		c.Ops = append(c.Ops, op)
		c.mu.Unlock()
		var dataOut []byte
		if le.Uint32(p.Payload[0:4]) == ptpip.DataPhaseOut {
			for {
				dp, err := ptpip.ReadPacket(conn)
				if err != nil {
					return
				}
				if dp.Type == ptpip.PktData || dp.Type == ptpip.PktEndData {
					dataOut = append(dataOut, dp.Payload[4:]...)
				}
				if dp.Type == ptpip.PktEndData {
					break
				}
			}
		}
		code, data := c.handle(op, params, dataOut, &session)
		if data != nil {
			sendData(conn, tid, data)
		}
		resp := le.AppendUint16(nil, code)
		resp = le.AppendUint32(resp, tid)
		_ = ptpip.WritePacket(conn, ptpip.PktCmdResponse, resp)
		if op == ptpip.OpSonySDIOConnect && len(dataOut) >= 8 && le.Uint32(dataOut[4:8]) == 5 {
			c.logf("transfer finished; camera powers off")
			return
		}
	}
}

func sendData(conn net.Conn, tid uint32, data []byte) {
	start := le.AppendUint32(nil, tid)
	start = le.AppendUint64(start, uint64(len(data)))
	_ = ptpip.WritePacket(conn, ptpip.PktStartData, start)
	const chunk = 32 * 1024
	for {
		n := len(data)
		t := ptpip.PktEndData
		if n > chunk {
			n, t = chunk, ptpip.PktData
		}
		pl := le.AppendUint32(nil, tid)
		pl = append(pl, data[:n]...)
		_ = ptpip.WritePacket(conn, t, pl)
		data = data[n:]
		if t == ptpip.PktEndData {
			return
		}
	}
}

func (c *Camera) handle(op uint16, params []uint32, dataOut []byte, session *bool) (uint16, []byte) {
	arg := func(i int) uint32 {
		if i < len(params) {
			return params[i]
		}
		return 0
	}
	if op != ptpip.OpOpenSession && op != ptpip.OpGetDeviceInfo && !*session {
		return ptpip.RespSessionNotOpen, nil
	}
	switch op {
	case ptpip.OpOpenSession:
		if *session {
			return ptpip.RespSessionAlreadyOpen, nil
		}
		*session = true
		return ptpip.RespOK, nil
	case ptpip.OpCloseSession:
		*session = false
		return ptpip.RespOK, nil
	case ptpip.OpGetDeviceInfo:
		return ptpip.RespOK, c.deviceInfo().Encode()
	case ptpip.OpSonySDIOConnect:
		if len(dataOut) >= 8 && arg(0) == 4 {
			c.mu.Lock()
			c.SonyStates = append(c.SonyStates, le.Uint32(dataOut[4:8]))
			c.mu.Unlock()
		}
		return ptpip.RespOK, nil
	case ptpip.OpSonyGetExtDevInfo:
		return ptpip.RespOK, make([]byte, 64)
	case ptpip.OpGetStorageIDs:
		w := &ptpip.Builder{}
		w.U32Array([]uint32{0x00010001})
		return ptpip.RespOK, w.B
	case ptpip.OpGetStorageInfo:
		w := &ptpip.Builder{}
		w.U16(4)
		w.U16(2)
		w.U16(0)
		w.U64(64 << 30)
		w.U64(32 << 30)
		w.U32(0xFFFFFFFF)
		w.Str("Storage Media")
		w.Str("")
		return ptpip.RespOK, w.B
	case ptpip.OpGetObjectHandles:
		parent := arg(2)
		var hs []uint32
		switch parent {
		case 0xFFFFFFFF:
			// root: the folders
			seen := map[uint32]bool{}
			for _, f := range c.Files {
				if !seen[f.parent] {
					seen[f.parent] = true
					hs = append(hs, f.parent)
				}
			}
		case 0:
			for _, f := range c.Files {
				hs = append(hs, f.handle)
			}
		default:
			for _, f := range c.Files {
				if f.parent == parent {
					hs = append(hs, f.handle)
				}
			}
		}
		w := &ptpip.Builder{}
		w.U32Array(hs)
		return ptpip.RespOK, w.B
	case ptpip.OpGetObjectInfo:
		f, folder := c.findHandle(arg(0))
		if f == nil && folder == "" {
			return 0x2009, nil
		}
		oi := &ptpip.ObjectInfo{StorageID: 0x00010001}
		if f == nil {
			oi.ObjectFormat = ptpip.FormatAssociation
			oi.AssociationType = 1
			oi.Filename = folder
			oi.ParentObject = 0
		} else {
			oi.ObjectFormat = 0x3801
			if strings.HasSuffix(f.Name, ".ARW") {
				oi.ObjectFormat = 0xB101
			}
			oi.CompressedSize = uint32(len(f.Data))
			oi.ParentObject = f.parent
			oi.Filename = f.Name
			oi.CaptureDate = f.Taken.Format("20060102T150405")
			oi.ModificationDate = oi.CaptureDate
		}
		return ptpip.RespOK, oi.Encode()
	case ptpip.OpGetObject:
		f, _ := c.findHandle(arg(0))
		if f == nil {
			return 0x2009, nil
		}
		return ptpip.RespOK, f.Data
	case ptpip.OpGetObjectPropValue:
		f, _ := c.findHandle(arg(0))
		if f == nil {
			return 0x2009, nil
		}
		if uint16(arg(1)) == ptpip.PropObjectSize {
			return ptpip.RespOK, le.AppendUint64(nil, uint64(len(f.Data)))
		}
		return ptpip.RespParamNotSupported, nil
	}
	return ptpip.RespOperationNotSupp, nil
}

// --- SSDP / UPnP side ---

const descTemplate = `<?xml version="1.0" encoding="utf-8" ?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
<specVersion><major>1</major><minor>0</minor></specVersion>
<device>
<deviceType>urn:microsoft-com:device:mtp:1</deviceType>
<friendlyName>%s</friendlyName>
<manufacturer>Sony Corporation</manufacturer>
<modelName>%s</modelName>
<UDN>%s</UDN>
<serviceList><service>
<serviceType>urn:microsoft-com:service:MtpNullService:1</serviceType>
<serviceId>urn:microsoft-com:serviceId:MtpNullServiceId</serviceId>
</service></serviceList>
</device>
</root>`

// StartHTTP serves /DeviceDescription.xml on addr and returns its URL.
func (c *Camera) StartHTTP(addr string) (string, error) {
	c.udn = "uuid:00000000-0001-0010-8000-020000000001"
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/DeviceDescription.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
		fmt.Fprintf(w, descTemplate, c.Model, c.Model, c.udn)
	})
	c.httpSrv = &http.Server{Handler: mux}
	go c.httpSrv.Serve(ln)
	return "http://" + ln.Addr().String() + "/DeviceDescription.xml", nil
}

// NotifyMessage returns an ssdp:alive NOTIFY for the MtpNullService.
func (c *Camera) NotifyMessage(location string) []byte {
	var b bytes.Buffer
	b.WriteString("NOTIFY * HTTP/1.1\r\n")
	b.WriteString("HOST: 239.255.255.250:1900\r\n")
	b.WriteString("CACHE-CONTROL: max-age=1800\r\n")
	b.WriteString("LOCATION: " + location + "\r\n")
	b.WriteString("NT: urn:microsoft-com:service:MtpNullService:1\r\n")
	b.WriteString("NTS: ssdp:alive\r\n")
	b.WriteString("SERVER: wpr-fakecam UPnP/1.0\r\n")
	b.WriteString("USN: " + c.udn + "::urn:microsoft-com:service:MtpNullService:1\r\n\r\n")
	return b.Bytes()
}

// Announce sends NOTIFY messages to the SSDP multicast group every interval
// until ctx is done. Use it together with StartHTTP on the same IP.
func (c *Camera) Announce(ctx context.Context, location string, every time.Duration) error {
	conn, err := net.Dial("udp4", "239.255.255.250:1900")
	if err != nil {
		return err
	}
	defer conn.Close()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		_, _ = conn.Write(c.NotifyMessage(location))
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Port returns the PTP/IP port as int (helper for tests).
func (c *Camera) Port() int {
	_, p, _ := net.SplitHostPort(c.Addr())
	n, _ := strconv.Atoi(p)
	return n
}

// Reader returns a reader of a file's content (helper for tests).
func (f *File) Reader() io.Reader { return bytes.NewReader(f.Data) }
