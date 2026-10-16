package ptpip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// ErrNotPaired is returned when the camera answers the init request with
// InitFail, which in practice means the PC's GUID is not authorised.
var ErrNotPaired = errors.New("camera refused the connection (this PC is not paired with the camera)")

// Error is a PTP response code other than OK.
type Error struct {
	Op   uint16
	Code uint16
}

func (e *Error) Error() string {
	return fmt.Sprintf("PTP operation 0x%04X failed: %s", e.Op, RespName(e.Code))
}

// IsCode reports whether err is a PTP error with the given response code.
func IsCode(err error, code uint16) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.Code == code
}

// Response is the operation response.
type Response struct {
	Code   uint16
	TID    uint32
	Params []uint32
}

// Event is a PTP event received on the event channel.
type Event struct {
	Code   uint16
	Params []uint32
}

// Options configures a connection.
type Options struct {
	GUID     [16]byte
	HostName string        // shown on the camera ("Send to: <name>")
	Timeout  time.Duration // per network operation; default 15s
	Logf     func(format string, args ...any)
	OnEvent  func(Event)
}

// Client is a PTP/IP initiator connected to one camera.
type Client struct {
	opt        Options
	cmd        net.Conn
	evt        net.Conn
	mu         sync.Mutex
	tid        uint32
	session    bool
	CameraName string
	CameraGUID [16]byte
	InitAckRaw []byte // payload of the Init Command Ack (for diagnostics)
	Remote     string
	LocalIP    net.IP
	closeOnce  sync.Once
	evtDone    chan struct{}
}

func (c *Client) logf(format string, args ...any) {
	if c.opt.Logf != nil {
		c.opt.Logf(format, args...)
	}
}

// Dial connects to host (IP, optionally with :port) and performs the PTP/IP
// command and event channel handshakes.
func Dial(ctx context.Context, host string, opt Options) (*Client, error) {
	if opt.Timeout == 0 {
		opt.Timeout = 15 * time.Second
	}
	if opt.HostName == "" {
		opt.HostName = "PC"
	}
	addr := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		addr = net.JoinHostPort(host, strconv.Itoa(DefaultPort))
	}
	d := net.Dialer{Timeout: opt.Timeout}
	cmd, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to camera %s: %w", addr, err)
	}
	c := &Client{opt: opt, cmd: cmd, Remote: addr, evtDone: make(chan struct{})}
	if ta, ok := cmd.LocalAddr().(*net.TCPAddr); ok {
		c.LocalIP = ta.IP
	}
	if tcp, ok := cmd.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(10 * time.Second)
	}

	// --- Init Command Request ---
	payload := make([]byte, 0, 64)
	payload = append(payload, opt.GUID[:]...)
	payload = append(payload, EncodeUTF16Z(opt.HostName)...)
	payload = le.AppendUint32(payload, 0x00010000) // version 1.0
	_ = cmd.SetDeadline(time.Now().Add(opt.Timeout))
	if err := WritePacket(cmd, PktInitCommandRequest, payload); err != nil {
		cmd.Close()
		return nil, fmt.Errorf("send init request: %w", err)
	}
	p, err := ReadPacket(cmd)
	if err != nil {
		cmd.Close()
		if errors.Is(err, io.EOF) {
			return nil, ErrNotPaired
		}
		return nil, fmt.Errorf("read init ack: %w", err)
	}
	switch p.Type {
	case PktInitCommandAck:
	case PktInitFail:
		cmd.Close()
		reason := uint32(0)
		if len(p.Payload) >= 4 {
			reason = le.Uint32(p.Payload)
		}
		c.logf("camera sent InitFail (reason 0x%X)", reason)
		return nil, ErrNotPaired
	default:
		cmd.Close()
		return nil, fmt.Errorf("unexpected packet type %d during init", p.Type)
	}
	if len(p.Payload) < 20 {
		cmd.Close()
		return nil, fmt.Errorf("init ack too short")
	}
	connNum := le.Uint32(p.Payload[0:4])
	copy(c.CameraGUID[:], p.Payload[4:20])
	c.InitAckRaw = append([]byte(nil), p.Payload...)
	c.CameraName, _ = DecodeUTF16Z(p.Payload[20:])

	// --- Event channel ---
	evt, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		cmd.Close()
		return nil, fmt.Errorf("connect event channel: %w", err)
	}
	c.evt = evt
	_ = evt.SetDeadline(time.Now().Add(opt.Timeout))
	if err := WritePacket(evt, PktInitEventRequest, le.AppendUint32(nil, connNum)); err != nil {
		c.Close()
		return nil, fmt.Errorf("send event init: %w", err)
	}
	p, err = ReadPacket(evt)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("read event init ack: %w", err)
	}
	if p.Type != PktInitEventAck {
		c.Close()
		if p.Type == PktInitFail {
			return nil, ErrNotPaired
		}
		return nil, fmt.Errorf("unexpected packet type %d on event channel", p.Type)
	}
	_ = evt.SetDeadline(time.Time{})
	_ = cmd.SetDeadline(time.Time{})
	go c.eventLoop()
	c.logf("PTP/IP connected to %q (%s)", c.CameraName, addr)
	return c, nil
}

func (c *Client) eventLoop() {
	defer close(c.evtDone)
	for {
		p, err := ReadPacket(c.evt)
		if err != nil {
			return
		}
		switch p.Type {
		case PktProbeRequest:
			_ = WritePacket(c.evt, PktProbeResponse, nil)
		case PktEvent:
			if len(p.Payload) >= 6 {
				ev := Event{Code: le.Uint16(p.Payload[0:2])}
				for o := 6; o+4 <= len(p.Payload); o += 4 {
					ev.Params = append(ev.Params, le.Uint32(p.Payload[o:]))
				}
				c.logf("camera event 0x%04X %v", ev.Code, ev.Params)
				if c.opt.OnEvent != nil {
					c.opt.OnEvent(ev)
				}
			}
		}
	}
}

// Close closes both channels without any PTP-level goodbye.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.cmd != nil {
			c.cmd.Close()
		}
		if c.evt != nil {
			c.evt.Close()
		}
	})
	return nil
}

// Done is closed when the event channel ends (camera gone or Close called).
func (c *Client) Done() <-chan struct{} { return c.evtDone }

// Transaction runs one PTP operation. If dataOut is non-nil it is sent in
// the data phase. If dataIn is non-nil, incoming data is written to it and
// progress (if set) is called with bytes received so far and the total.
func (c *Client) Transaction(ctx context.Context, op uint16, params []uint32, dataOut []byte, dataIn io.Writer, progress func(done, total int64)) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(params) > 5 {
		return Response{}, fmt.Errorf("too many parameters")
	}
	tid := c.tid
	c.tid++

	// Abort the blocking I/O if the context is cancelled.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.cmd.SetDeadline(time.Now())
		case <-stop:
		}
	}()
	deadline := func() { _ = c.cmd.SetDeadline(time.Now().Add(c.opt.Timeout)) }
	fail := func(err error) (Response, error) {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, err
	}

	phase := DataPhaseNoneOrIn
	if dataOut != nil {
		phase = DataPhaseOut
	}
	req := make([]byte, 0, 10+4*len(params))
	req = le.AppendUint32(req, phase)
	req = le.AppendUint16(req, op)
	req = le.AppendUint32(req, tid)
	for _, p := range params {
		req = le.AppendUint32(req, p)
	}
	deadline()
	if err := WritePacket(c.cmd, PktCmdRequest, req); err != nil {
		return fail(fmt.Errorf("send op 0x%04X: %w", op, err))
	}

	if dataOut != nil {
		start := make([]byte, 0, 12)
		start = le.AppendUint32(start, tid)
		start = le.AppendUint64(start, uint64(len(dataOut)))
		if err := WritePacket(c.cmd, PktStartData, start); err != nil {
			return fail(err)
		}
		const chunk = 64 * 1024
		rest := dataOut
		for {
			n := len(rest)
			typ := PktEndData
			if n > chunk {
				n = chunk
				typ = PktData
			}
			pl := le.AppendUint32(make([]byte, 0, 4+n), tid)
			pl = append(pl, rest[:n]...)
			deadline()
			if err := WritePacket(c.cmd, typ, pl); err != nil {
				return fail(err)
			}
			rest = rest[n:]
			if typ == PktEndData {
				break
			}
		}
	}

	var total, got int64 = -1, 0
	for {
		deadline()
		p, err := ReadPacket(c.cmd)
		if err != nil {
			return fail(fmt.Errorf("read reply to op 0x%04X: %w", op, err))
		}
		switch p.Type {
		case PktStartData:
			if len(p.Payload) >= 12 {
				total = int64(le.Uint64(p.Payload[4:12]))
			}
			if progress != nil {
				progress(0, total)
			}
		case PktData, PktEndData:
			if len(p.Payload) < 4 {
				return fail(fmt.Errorf("short data packet"))
			}
			chunk := p.Payload[4:]
			if dataIn != nil && len(chunk) > 0 {
				if _, err := dataIn.Write(chunk); err != nil {
					return fail(fmt.Errorf("write data: %w", err))
				}
			}
			got += int64(len(chunk))
			if progress != nil {
				progress(got, total)
			}
		case PktCmdResponse:
			if len(p.Payload) < 6 {
				return fail(fmt.Errorf("short response packet"))
			}
			r := Response{Code: le.Uint16(p.Payload[0:2]), TID: le.Uint32(p.Payload[2:6])}
			for o := 6; o+4 <= len(p.Payload); o += 4 {
				r.Params = append(r.Params, le.Uint32(p.Payload[o:]))
			}
			_ = c.cmd.SetDeadline(time.Time{})
			if r.Code != RespOK {
				return r, &Error{Op: op, Code: r.Code}
			}
			if total >= 0 && got != total {
				return r, fmt.Errorf("op 0x%04X: received %d of %d bytes", op, got, total)
			}
			return r, nil
		case PktCancel:
			return fail(fmt.Errorf("camera cancelled op 0x%04X", op))
		default:
			c.logf("ignoring packet type %d during op 0x%04X", p.Type, op)
		}
	}
}

// bufWriter collects data in memory.
type bufWriter struct{ b []byte }

func (w *bufWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

// GetData runs an operation with an incoming data phase and returns the data.
func (c *Client) GetData(ctx context.Context, op uint16, params ...uint32) ([]byte, error) {
	return c.getData(ctx, op, params...)
}

func (c *Client) getData(ctx context.Context, op uint16, params ...uint32) ([]byte, error) {
	w := &bufWriter{}
	_, err := c.Transaction(ctx, op, params, nil, w, nil)
	return w.b, err
}

// OpenSession opens session 1. "Session already open" is treated as success.
func (c *Client) OpenSession(ctx context.Context) error {
	_, err := c.Transaction(ctx, OpOpenSession, []uint32{1}, nil, nil, nil)
	if err != nil && !IsCode(err, RespSessionAlreadyOpen) {
		return err
	}
	c.session = true
	return nil
}

// CloseSession closes the session.
func (c *Client) CloseSession(ctx context.Context) error {
	if !c.session {
		return nil
	}
	c.session = false
	_, err := c.Transaction(ctx, OpCloseSession, nil, nil, nil, nil)
	return err
}

// GetDeviceInfo fetches the DeviceInfo dataset.
func (c *Client) GetDeviceInfo(ctx context.Context) (*DeviceInfo, error) {
	b, err := c.getData(ctx, OpGetDeviceInfo)
	if err != nil {
		return nil, err
	}
	return ParseDeviceInfo(b)
}

// GetStorageIDs lists storage IDs.
func (c *Client) GetStorageIDs(ctx context.Context) ([]uint32, error) {
	b, err := c.getData(ctx, OpGetStorageIDs)
	if err != nil {
		return nil, err
	}
	r := &reader{b: b}
	ids := r.u32array()
	return ids, r.err
}

// GetStorageInfo fetches a StorageInfo dataset.
func (c *Client) GetStorageInfo(ctx context.Context, id uint32) (*StorageInfo, error) {
	b, err := c.getData(ctx, OpGetStorageInfo, id)
	if err != nil {
		return nil, err
	}
	return ParseStorageInfo(b)
}

// GetObjectHandles lists handles in storage under parent
// (0xFFFFFFFF = root, 0 = all objects).
func (c *Client) GetObjectHandles(ctx context.Context, storage, format, parent uint32) ([]uint32, error) {
	b, err := c.getData(ctx, OpGetObjectHandles, storage, format, parent)
	if err != nil {
		return nil, err
	}
	r := &reader{b: b}
	h := r.u32array()
	return h, r.err
}

// GetObjectInfo fetches an ObjectInfo dataset.
func (c *Client) GetObjectInfo(ctx context.Context, h uint32) (*ObjectInfo, error) {
	b, err := c.getData(ctx, OpGetObjectInfo, h)
	if err != nil {
		return nil, err
	}
	return ParseObjectInfo(b)
}

// GetObjectSize64 asks for the 64-bit ObjectSize property (for files > 4 GB).
func (c *Client) GetObjectSize64(ctx context.Context, h uint32) (uint64, error) {
	b, err := c.getData(ctx, OpGetObjectPropValue, h, uint32(PropObjectSize))
	if err != nil {
		return 0, err
	}
	if len(b) < 8 {
		return 0, ErrShort
	}
	return le.Uint64(b), nil
}

// GetObject streams the object to w.
func (c *Client) GetObject(ctx context.Context, h uint32, w io.Writer, progress func(done, total int64)) error {
	_, err := c.Transaction(ctx, OpGetObject, []uint32{h}, nil, w, progress)
	return err
}

// SonySDIO sends Sony vendor op 0x9280 ("SDIO connect"). extra holds the
// optional trailing bytes (libgphoto2 calls their count "additional").
func (c *Client) SonySDIO(ctx context.Context, mode uint32, data2, data3, data4 uint32, extra []byte) error {
	b := make([]byte, 0, 16+len(extra))
	b = le.AppendUint32(b, uint32(len(extra)))
	b = le.AppendUint32(b, data2)
	b = le.AppendUint32(b, data3)
	b = le.AppendUint32(b, data4)
	b = append(b, extra...)
	_, err := c.Transaction(ctx, OpSonySDIOConnect, []uint32{mode}, b, nil, nil)
	return err
}

// SonyExtInfo sends Sony vendor op 0x9281 and returns its data.
func (c *Client) SonyExtInfo(ctx context.Context, mode uint32) ([]byte, error) {
	return c.getData(ctx, OpSonyGetExtDevInfo, mode)
}

// SonyBeginTransfer tells a Sony camera that the PC is ready; the camera
// then shows "Sending..." and makes its file system available.
// Same sequence as libgphoto2 and PlayMemories Home.
func (c *Client) SonyBeginTransfer(ctx context.Context) error {
	if err := c.SonySDIO(ctx, 4, 2, 0, 0, []byte{1, 1}); err != nil {
		return err
	}
	_, err := c.SonyExtInfo(ctx, 4)
	return err
}

// SonyEndTransfer tells the camera the transfer is complete; the camera then
// shows its "transfer complete" screen and typically switches off.
func (c *Client) SonyEndTransfer(ctx context.Context) error {
	// PlayMemories Home first reports state 4 ("done") ...
	if err := c.SonySDIO(ctx, 4, 4, 0, 0, []byte{0}); err == nil {
		_, _ = c.SonyExtInfo(ctx, 4)
	}
	// ... then sends state 5 (end), as libgphoto2 does on exit.
	return c.SonySDIO(ctx, 4, 5, 0, 0, nil)
}
