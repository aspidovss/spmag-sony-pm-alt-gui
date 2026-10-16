//go:build !windows

package pairing

import "errors"

// Drive is a USB disk that may be a Sony camera in Mass Storage mode.
type Drive struct {
	Path, Vendor, Product string
	BusType               uint32
}

func (d Drive) String() string { return d.Path }

// IsSony reports whether the drive identifies itself as a Sony device.
func (d Drive) IsSony() bool { return false }

// ErrNeedsAdmin is returned when raw disk access is denied.
var ErrNeedsAdmin = errors.New("administrator rights are required")

var errUnsupported = errors.New("USB pairing is only implemented on Windows")

// ListUSBDrives is not implemented on this OS.
func ListUSBDrives() ([]Drive, error) { return nil, errUnsupported }

// WriteGUID is not implemented on this OS.
func WriteGUID(path string, g GUID, hostName string) error { return errUnsupported }

// USBSupported reports whether USB pairing is implemented on this OS.
const USBSupported = false
