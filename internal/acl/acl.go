// Package acl restricts which cameras the program talks to.
//
// The list is written one entry per line (or separated by commas):
//
//	192.168.1.50            one camera by IP address
//	192.168.1.0/24          every address of a network
//	serial:3012345          a camera by the serial number it reports
//	model:ILCA-77M2         a camera model
//
// Entries of one kind are alternatives; every kind that is present must match
// (so "192.168.1.0/24" plus "serial:A" and "serial:B" means: on that network AND
// one of those two cameras). IP rules are checked before connecting, model and
// serial rules right after connecting, before any file list is read.
//
// An empty list allows any camera, but a manually entered address must still be
// on a local network (private, link-local or loopback), so that the program
// cannot be pointed at arbitrary hosts on the internet.
package acl

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/aspidovss/spmag-sony-pm-alt-gui/internal/i18n"
)

// ErrNotAllowed is wrapped by every refusal.
var ErrNotAllowed = errors.New("camera not allowed")

type denied struct{ msg string }

func (d denied) Error() string        { return d.msg }
func (d denied) Is(target error) bool { return target == ErrNotAllowed }

func deny(key string, args ...any) error { return denied{i18n.T(key, args...)} }

// Rules is a parsed list. The zero value allows everything local.
type Rules struct {
	prefixes []netip.Prefix
	serials  map[string]bool
	models   map[string]bool
}

// Split breaks the text of a settings field into entries: separated by new
// lines, commas, semicolons or spaces; "#" starts a comment; duplicates are
// dropped.
func Split(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, f := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' }) {
			if !seen[strings.ToLower(f)] {
				seen[strings.ToLower(f)] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// Parse checks and compiles the entries. An invalid entry is an error, so a
// typo can never silently widen the list.
func Parse(entries []string) (*Rules, error) {
	r := &Rules{serials: map[string]bool{}, models: map[string]bool{}}
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		low := strings.ToLower(e)
		switch {
		case strings.HasPrefix(low, "serial:"):
			v := normSerial(e[len("serial:"):])
			if v == "" {
				return nil, errors.New(i18n.T("acl.badEntry", e))
			}
			r.serials[v] = true
		case strings.HasPrefix(low, "model:"):
			v := normModel(e[len("model:"):])
			if v == "" {
				return nil, errors.New(i18n.T("acl.badEntry", e))
			}
			r.models[v] = true
		case strings.Contains(e, "/"):
			p, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, errors.New(i18n.T("acl.badEntry", e))
			}
			r.prefixes = append(r.prefixes, p.Masked())
		default:
			a, err := netip.ParseAddr(e)
			if err != nil {
				return nil, errors.New(i18n.T("acl.badEntry", e))
			}
			a = a.Unmap().WithZone("")
			r.prefixes = append(r.prefixes, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return r, nil
}

// Empty reports whether the list has no entries at all.
func (r *Rules) Empty() bool {
	return r == nil || len(r.prefixes)+len(r.serials)+len(r.models) == 0
}

// hostAddr extracts the IP address from "ip", "ip:port" or "[v6]:port".
func hostAddr(s string) (netip.Addr, error) {
	s = strings.TrimSpace(s)
	host := s
	if h, _, err := net.SplitHostPort(s); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, errors.New(i18n.T("acl.needIP"))
	}
	return a.Unmap().WithZone(""), nil
}

// IsLocal reports whether a is a loopback, private, link-local or unique-local address.
func IsLocal(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast()
}

// AllowAddr checks a camera address (before connecting). manual is true for an
// address somebody typed in; an address that arrived in an SSDP announcement on
// the local segment is not forced to be a private one.
func (r *Rules) AllowAddr(addr string, manual bool) error {
	a, err := hostAddr(addr)
	if err != nil {
		return err
	}
	if r != nil && len(r.prefixes) > 0 {
		for _, p := range r.prefixes {
			if p.Contains(a) {
				return nil
			}
		}
		return deny("acl.notListed", a.String())
	}
	if manual && !IsLocal(a) {
		return deny("acl.notLocal", a.String())
	}
	return nil
}

// AllowDevice checks what the camera reported about itself (right after connecting).
func (r *Rules) AllowDevice(model, serial string) error {
	if r == nil {
		return nil
	}
	if len(r.models) > 0 && !r.models[normModel(model)] {
		return deny("acl.model", model)
	}
	if len(r.serials) > 0 && !r.serials[normSerial(serial)] {
		return deny("acl.serial", serial)
	}
	return nil
}

func normModel(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// normSerial ignores case, spaces and leading zeros ("0000012345" == "12345").
func normSerial(s string) string {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	return strings.TrimLeft(s, "0")
}

// Check is a convenience for callers that hold the raw settings list.
func Check(entries []string, addr string, manual bool) error {
	r, err := Parse(entries)
	if err != nil {
		return err
	}
	return r.AllowAddr(addr, manual)
}

// String lists the entries for logs.
func (r *Rules) String() string {
	if r.Empty() {
		return "any local camera"
	}
	return fmt.Sprintf("%d address rule(s), %d model(s), %d serial(s)", len(r.prefixes), len(r.models), len(r.serials))
}
