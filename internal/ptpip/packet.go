// Package ptpip implements the client side of PTP/IP (ISO 15740 over TCP)
// as used by Sony cameras in "Send to Computer" mode.
package ptpip

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// DefaultPort is the TCP port of the camera's PTP/IP server.
const DefaultPort = 15740

// PTP/IP packet types.
const (
	PktInitCommandRequest uint32 = 1
	PktInitCommandAck     uint32 = 2
	PktInitEventRequest   uint32 = 3
	PktInitEventAck       uint32 = 4
	PktInitFail           uint32 = 5
	PktCmdRequest         uint32 = 6
	PktCmdResponse        uint32 = 7
	PktEvent              uint32 = 8
	PktStartData          uint32 = 9
	PktData               uint32 = 10
	PktCancel             uint32 = 11
	PktEndData            uint32 = 12
	PktProbeRequest       uint32 = 13
	PktProbeResponse      uint32 = 14
)

// Data phase values in an operation request.
const (
	DataPhaseNoneOrIn uint32 = 1
	DataPhaseOut      uint32 = 2
)

const maxPacket = 64 << 20

var le = binary.LittleEndian

// Packet is one raw PTP/IP packet.
type Packet struct {
	Type    uint32
	Payload []byte
}

// ReadPacket reads one packet from r.
func ReadPacket(r io.Reader) (Packet, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Packet{}, err
	}
	n := le.Uint32(hdr[0:4])
	t := le.Uint32(hdr[4:8])
	if n < 8 || n > maxPacket {
		return Packet{}, fmt.Errorf("ptpip: bad packet length %d", n)
	}
	size := int64(n - 8)
	if size <= 1<<20 {
		p := make([]byte, size)
		if _, err := io.ReadFull(r, p); err != nil {
			return Packet{}, err
		}
		return Packet{Type: t, Payload: p}, nil
	}
	// A big packet: do not reserve the announced size up front, the other
	// side could claim 64 MB and send nothing. Memory grows with real data.
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, r, size); err != nil {
		return Packet{}, err
	}
	return Packet{Type: t, Payload: buf.Bytes()}, nil
}

// WritePacket writes one packet to w in a single Write call.
func WritePacket(w io.Writer, t uint32, payload []byte) error {
	b := make([]byte, 8+len(payload))
	le.PutUint32(b[0:4], uint32(len(b)))
	le.PutUint32(b[4:8], t)
	copy(b[8:], payload)
	_, err := w.Write(b)
	return err
}

// EncodeUTF16Z encodes s as null-terminated UTF-16LE (used in the init packets).
func EncodeUTF16Z(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, (len(u)+1)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return append(b, 0, 0)
}

// DecodeUTF16Z decodes null-terminated UTF-16LE and returns the string and
// the number of bytes consumed (including the terminator, if present).
func DecodeUTF16Z(b []byte) (string, int) {
	var u []uint16
	i := 0
	for ; i+1 < len(b); i += 2 {
		c := le.Uint16(b[i:])
		if c == 0 {
			i += 2
			return string(utf16.Decode(u)), i
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u)), i
}

// ErrShort is returned when a dataset is truncated.
var ErrShort = errors.New("ptp: dataset truncated")

// reader is a little helper for decoding PTP datasets.
type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.off+n > len(r.b) {
		r.err = ErrShort
		return false
	}
	return true
}

func (r *reader) u8() uint8 {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := le.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := le.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *reader) u64() uint64 {
	if !r.need(8) {
		return 0
	}
	v := le.Uint64(r.b[r.off:])
	r.off += 8
	return v
}

// str reads a PTP string: u8 character count (including the null) + UTF-16LE.
func (r *reader) str() string {
	n := int(r.u8())
	if n == 0 || !r.need(n*2) {
		return ""
	}
	u := make([]uint16, 0, n)
	for i := 0; i < n; i++ {
		c := le.Uint16(r.b[r.off+i*2:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	r.off += n * 2
	return string(utf16.Decode(u))
}

func (r *reader) u16array() []uint16 {
	n := int(r.u32())
	if n < 0 || !r.need(n*2) {
		return nil
	}
	out := make([]uint16, n)
	for i := range out {
		out[i] = r.u16()
	}
	return out
}

func (r *reader) u32array() []uint32 {
	n := int(r.u32())
	if n < 0 || !r.need(n*4) {
		return nil
	}
	out := make([]uint32, n)
	for i := range out {
		out[i] = r.u32()
	}
	return out
}

// writer helpers for building datasets (used by the fake camera and tests).
type Builder struct{ B []byte }

func (w *Builder) U8(v uint8)   { w.B = append(w.B, v) }
func (w *Builder) U16(v uint16) { w.B = le.AppendUint16(w.B, v) }
func (w *Builder) U32(v uint32) { w.B = le.AppendUint32(w.B, v) }
func (w *Builder) U64(v uint64) { w.B = le.AppendUint64(w.B, v) }
func (w *Builder) Str(s string) {
	if s == "" {
		w.U8(0)
		return
	}
	u := utf16.Encode([]rune(s))
	w.U8(uint8(len(u) + 1))
	for _, c := range u {
		w.U16(c)
	}
	w.U16(0)
}
func (w *Builder) U16Array(a []uint16) {
	w.U32(uint32(len(a)))
	for _, v := range a {
		w.U16(v)
	}
}
func (w *Builder) U32Array(a []uint32) {
	w.U32(uint32(len(a)))
	for _, v := range a {
		w.U32(v)
	}
}
