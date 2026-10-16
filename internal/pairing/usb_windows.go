//go:build windows

package pairing

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Drive is a USB disk that may be a Sony camera in Mass Storage mode.
type Drive struct {
	Path    string // \\.\PhysicalDriveN
	Vendor  string
	Product string
	BusType uint32
}

func (d Drive) String() string {
	return fmt.Sprintf("%s (%s %s)", d.Path, d.Vendor, d.Product)
}

// IsSony reports whether the drive identifies itself as a Sony device.
func (d Drive) IsSony() bool { return strings.EqualFold(strings.TrimSpace(d.Vendor), "sony") }

const (
	ioctlStorageQueryProperty  = 0x002D1400
	ioctlScsiPassThroughDirect = 0x0004D014
	busTypeUsb                 = 7
	scsiIoctlDataOut           = 0
)

type storagePropertyQuery struct {
	PropertyID uint32
	QueryType  uint32
	Additional [1]byte
}

func openDrive(path string, write bool) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	access := uint32(0)
	if write {
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	}
	return windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
}

func cstr(b []byte, off uint32) string {
	if off == 0 || int(off) >= len(b) {
		return ""
	}
	end := int(off)
	for end < len(b) && b[end] != 0 {
		end++
	}
	return strings.TrimSpace(string(b[off:end]))
}

// ListUSBDrives enumerates physical drives on the USB bus.
func ListUSBDrives() ([]Drive, error) {
	var out []Drive
	for i := 0; i < 32; i++ {
		path := fmt.Sprintf(`\\.\PhysicalDrive%d`, i)
		h, err := openDrive(path, false)
		if err != nil {
			continue
		}
		q := storagePropertyQuery{}
		buf := make([]byte, 1024)
		var ret uint32
		err = windows.DeviceIoControl(h, ioctlStorageQueryProperty, (*byte)(unsafe.Pointer(&q)), uint32(unsafe.Sizeof(q)), &buf[0], uint32(len(buf)), &ret, nil)
		windows.CloseHandle(h)
		if err != nil || ret < 36 {
			continue
		}
		// STORAGE_DEVICE_DESCRIPTOR offsets
		vendorOff := le32(buf[12:])
		productOff := le32(buf[16:])
		bus := le32(buf[28:])
		d := Drive{Path: path, Vendor: cstr(buf, vendorOff), Product: cstr(buf, productOff), BusType: bus}
		if bus == busTypeUsb {
			out = append(out, d)
		}
	}
	return out, nil
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// scsiPassThroughDirect mirrors SCSI_PASS_THROUGH_DIRECT (x64 / x86 safe:
// Go uses the same natural alignment as MSVC for these field types).
type scsiPassThroughDirect struct {
	Length             uint16
	ScsiStatus         uint8
	PathID             uint8
	TargetID           uint8
	Lun                uint8
	CdbLength          uint8
	SenseInfoLength    uint8
	DataIn             uint8
	DataTransferLength uint32
	TimeOutValue       uint32
	DataBuffer         uintptr
	SenseInfoOffset    uint32
	Cdb                [16]byte
}

type sptdWithSense struct {
	sptd  scsiPassThroughDirect
	_     uint32
	sense [32]byte
}

// ErrNeedsAdmin is returned when raw disk access is denied.
var ErrNeedsAdmin = errors.New("administrator rights are required to send the pairing command")

// WriteGUID sends the Sony "set GUID" command to the drive at path.
func WriteGUID(path string, g GUID, hostName string) error {
	h, err := openDrive(path, true)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return ErrNeedsAdmin
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer windows.CloseHandle(h)

	payload := USBPayload(g, hostName)
	// Page-aligned buffer: satisfies any adapter alignment requirement.
	raw := make([]byte, len(payload)+4096)
	off := int((4096 - uintptr(unsafe.Pointer(&raw[0]))%4096) % 4096)
	data := raw[off : off+len(payload)]
	copy(data, payload)

	var s sptdWithSense
	s.sptd.Length = uint16(unsafe.Sizeof(s.sptd))
	s.sptd.CdbLength = uint8(len(USBCDB))
	s.sptd.SenseInfoLength = uint8(len(s.sense))
	s.sptd.DataIn = scsiIoctlDataOut
	s.sptd.DataTransferLength = uint32(len(data))
	s.sptd.TimeOutValue = 10
	s.sptd.DataBuffer = uintptr(unsafe.Pointer(&data[0]))
	s.sptd.SenseInfoOffset = uint32(unsafe.Offsetof(s.sense))
	copy(s.sptd.Cdb[:], USBCDB)

	var ret uint32
	err = windows.DeviceIoControl(h, ioctlScsiPassThroughDirect,
		(*byte)(unsafe.Pointer(&s)), uint32(unsafe.Sizeof(s)),
		(*byte)(unsafe.Pointer(&s)), uint32(unsafe.Sizeof(s)), &ret, nil)
	// keep data alive until after the call
	_ = data[0]
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return ErrNeedsAdmin
		}
		return fmt.Errorf("pairing command failed: %w", err)
	}
	if s.sptd.ScsiStatus != 0 {
		return fmt.Errorf("camera rejected the pairing command (SCSI status 0x%02X, sense %02X/%02X/%02X)",
			s.sptd.ScsiStatus, s.sense[2]&0x0F, s.sense[12], s.sense[13])
	}
	return nil
}

// USBSupported reports whether USB pairing is implemented on this OS.
const USBSupported = true
