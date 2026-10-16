//go:build linux

package pairing

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// Drive is a SCSI generic device that may be a Sony camera in Mass Storage mode.
type Drive struct {
	Path, Vendor, Product string
	BusType               uint32
}

func (d Drive) String() string {
	return fmt.Sprintf("%s (%s %s)", d.Path, strings.TrimSpace(d.Vendor), strings.TrimSpace(d.Product))
}

// IsSony reports whether the drive identifies itself as a Sony device.
func (d Drive) IsSony() bool { return strings.EqualFold(strings.TrimSpace(d.Vendor), "sony") }

// ErrNeedsAdmin is returned when the device cannot be opened for writing.
var ErrNeedsAdmin = errors.New("permission denied: run as root (sudo) or add your user to the 'disk' group; in Docker pass the device with --device /dev/sgN")

func readSys(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ListUSBDrives lists the USB-attached SCSI generic devices (/dev/sg*).
func ListUSBDrives() ([]Drive, error) {
	return listDrives("/sys/class/scsi_generic", "/dev")
}

func listDrives(sysDir, devDir string) ([]Drive, error) {
	entries, err := os.ReadDir(sysDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Drive
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "sg") {
			continue
		}
		dev := filepath.Join(sysDir, e.Name(), "device")
		real, err := filepath.EvalSymlinks(dev)
		if err != nil || !strings.Contains(real, "/usb") {
			continue // only USB devices
		}
		out = append(out, Drive{
			Path:    filepath.Join(devDir, e.Name()),
			Vendor:  readSys(filepath.Join(dev, "vendor")),
			Product: readSys(filepath.Join(dev, "model")),
		})
	}
	return out, nil
}

// sgIoHdr mirrors struct sg_io_hdr from <scsi/sg.h> (same layout on 32 and 64 bit).
type sgIoHdr struct {
	InterfaceID    int32
	DxferDirection int32
	CmdLen         uint8
	MxSbLen        uint8
	IovecCount     uint16
	DxferLen       uint32
	Dxferp         uintptr
	Cmdp           uintptr
	Sbp            uintptr
	Timeout        uint32
	Flags          uint32
	PackID         int32
	UsrPtr         uintptr
	Status         uint8
	MaskedStatus   uint8
	MsgStatus      uint8
	SbLenWr        uint8
	HostStatus     uint16
	DriverStatus   uint16
	Resid          int32
	Duration       uint32
	Info           uint32
}

const (
	sgIO         = 0x2285
	sgDxferToDev = -2
	sgTimeoutMs  = 10000
)

// WriteGUID sends the Sony "set GUID" command to the SCSI generic device at path.
func WriteGUID(path string, g GUID, hostName string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return ErrNeedsAdmin
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data := USBPayload(g, hostName)
	cdb := append([]byte(nil), USBCDB...)
	sense := make([]byte, 32)
	h := sgIoHdr{
		InterfaceID:    'S',
		DxferDirection: sgDxferToDev,
		CmdLen:         uint8(len(cdb)),
		MxSbLen:        uint8(len(sense)),
		DxferLen:       uint32(len(data)),
		Dxferp:         uintptr(unsafe.Pointer(&data[0])),
		Cmdp:           uintptr(unsafe.Pointer(&cdb[0])),
		Sbp:            uintptr(unsafe.Pointer(&sense[0])),
		Timeout:        sgTimeoutMs,
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), sgIO, uintptr(unsafe.Pointer(&h)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(cdb)
	runtime.KeepAlive(sense)
	if errno != 0 {
		if errno == syscall.EACCES || errno == syscall.EPERM {
			return ErrNeedsAdmin
		}
		return fmt.Errorf("pairing command failed: %w", errno)
	}
	if h.Status != 0 || h.HostStatus != 0 || h.DriverStatus&0x0f != 0 {
		return fmt.Errorf("camera rejected the pairing command (SCSI status 0x%02X, host 0x%X, driver 0x%X, sense %02X/%02X/%02X)",
			h.Status, h.HostStatus, h.DriverStatus, sense[2]&0x0F, sense[12], sense[13])
	}
	return nil
}

// USBSupported reports whether USB pairing is implemented on this OS.
const USBSupported = true
