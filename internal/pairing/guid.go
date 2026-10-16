// Package pairing handles the PC identity (PTP/IP GUID) that a Sony camera
// must know before it accepts a Wi-Fi "Send to Computer" connection.
//
// Two ways to get the camera to know a GUID:
//
//  1. PlayMemories Home pairing: PMH uses 8 zero bytes + ff ff + the MAC
//     address of the PC network adapter that talks to the camera. If the
//     camera was ever paired with PMH on this PC, that GUID already works.
//  2. USB pairing (this program): the GUID is written to the camera over USB
//     with a Sony vendor SCSI command while the camera is connected in
//     "Mass Storage" mode. See usb_windows.go.
package pairing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// GUID is the 16-byte PTP/IP initiator GUID.
type GUID [16]byte

// String formats the GUID as colon-separated hex (same style as gphoto2).
func (g GUID) String() string {
	parts := make([]string, 16)
	for i, b := range g {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, ":")
}

// IsZero reports whether the GUID is all zeros.
func (g GUID) IsZero() bool { return g == GUID{} }

// ParseGUID parses 32 hex digits, optionally separated by ':' '-' or spaces.
func ParseGUID(s string) (GUID, error) {
	clean := strings.NewReplacer(":", "", "-", "", " ", "", "{", "", "}", "").Replace(s)
	var g GUID
	if len(clean) != 32 {
		return g, fmt.Errorf("GUID must have 32 hex digits, got %d", len(clean))
	}
	b, err := hex.DecodeString(clean)
	if err != nil {
		return g, fmt.Errorf("GUID: %w", err)
	}
	copy(g[:], b)
	return g, nil
}

// NewGUID creates a new GUID in the same shape PlayMemories Home uses
// (8 zero bytes, ff ff, 6 bytes), with a random locally-administered
// "MAC" so it cannot clash with a real adapter.
func NewGUID() GUID {
	var g GUID
	g[8], g[9] = 0xff, 0xff
	_, _ = rand.Read(g[10:])
	g[10] = (g[10] | 0x02) &^ 0x01
	return g
}

// FromMAC builds the PlayMemories-Home-style GUID for a MAC address.
func FromMAC(mac net.HardwareAddr) GUID {
	var g GUID
	g[8], g[9] = 0xff, 0xff
	copy(g[10:], mac)
	return g
}

// MACForLocalIP returns the MAC of the interface that owns ip.
func MACForLocalIP(ip net.IP) (net.HardwareAddr, string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, "", err
	}
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(ip) && len(ifc.HardwareAddr) == 6 {
				return ifc.HardwareAddr, ifc.Name, nil
			}
		}
	}
	return nil, "", fmt.Errorf("no adapter with address %s", ip)
}

// LocalIPToward returns the local IP the OS would use to reach dst.
func LocalIPToward(dst string) (net.IP, error) {
	c, err := net.Dial("udp4", net.JoinHostPort(dst, "15740"))
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP, nil
}

// PMHGuidsFor returns PlayMemories-style GUIDs for the adapter that routes to
// cameraIP first, followed by all other adapters (PMH may have paired over a
// different adapter).
func PMHGuidsFor(cameraIP string) []GUID {
	var out []GUID
	seen := map[GUID]bool{}
	add := func(mac net.HardwareAddr) {
		if len(mac) != 6 {
			return
		}
		g := FromMAC(mac)
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	if ip, err := LocalIPToward(cameraIP); err == nil {
		if mac, _, err := MACForLocalIP(ip); err == nil {
			add(mac)
		}
	}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagLoopback == 0 {
			add(ifc.HardwareAddr)
		}
	}
	return out
}

// USBPayload builds the 8192-byte data block of the Sony "set GUID" SCSI
// command (CDB 7a 04 00 ...). Layout reverse-engineered by Clemens Fruhwirth
// (sony-guid-setter, LGPL-2.1); re-implemented here from the description.
//
//	0x000  25 01 00 00 08        magic
//	0x011  host name, UTF-16LE, 8 characters max
//	0x0B1  10 00 00 00           magic
//	0x0B5  16-byte GUID
func USBPayload(g GUID, hostName string) []byte {
	b := make([]byte, 8192)
	copy(b[0:], []byte{0x25, 0x01, 0x00, 0x00, 0x08})
	r := []rune(hostName)
	if len(r) > 8 {
		r = r[:8]
	}
	for i, c := range r {
		if c > 0xFFFF {
			c = '?'
		}
		b[17+i*2] = byte(c)
		b[17+i*2+1] = byte(c >> 8)
	}
	copy(b[177:], []byte{0x10, 0x00, 0x00, 0x00})
	copy(b[181:], g[:])
	return b
}

// USBCDB is the 9-byte vendor command descriptor block.
var USBCDB = []byte{0x7a, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
