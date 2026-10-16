package ptpip

import (
	"fmt"
	"strings"
	"time"
)

// Operation codes used by this program.
const (
	OpGetDeviceInfo        uint16 = 0x1001
	OpOpenSession          uint16 = 0x1002
	OpCloseSession         uint16 = 0x1003
	OpGetStorageIDs        uint16 = 0x1004
	OpGetStorageInfo       uint16 = 0x1005
	OpGetNumObjects        uint16 = 0x1006
	OpGetObjectHandles     uint16 = 0x1007
	OpGetObjectInfo        uint16 = 0x1008
	OpGetObject            uint16 = 0x1009
	OpGetPartialObject     uint16 = 0x101B
	OpGetObjectPropValue   uint16 = 0x9803
	OpSonySDIOConnect      uint16 = 0x9280 // Sony "SDIO connect" / transfer status
	OpSonyGetExtDevInfo    uint16 = 0x9281 // Sony "get SDIO ext device info"
	PropObjectSize         uint16 = 0xDC04
	FormatAssociation      uint16 = 0x3001
	RespOK                 uint16 = 0x2001
	RespGeneralError       uint16 = 0x2002
	RespSessionNotOpen     uint16 = 0x2003
	RespOperationNotSupp   uint16 = 0x2005
	RespParamNotSupported  uint16 = 0x2006
	RespSessionAlreadyOpen uint16 = 0x201E
)

// RespName returns a readable name for common response codes.
func RespName(c uint16) string {
	switch c {
	case 0x2001:
		return "OK"
	case 0x2002:
		return "GeneralError"
	case 0x2003:
		return "SessionNotOpen"
	case 0x2005:
		return "OperationNotSupported"
	case 0x2006:
		return "ParameterNotSupported"
	case 0x2009:
		return "InvalidObjectHandle"
	case 0x2019:
		return "DeviceBusy"
	case 0x201E:
		return "SessionAlreadyOpen"
	case 0x201F:
		return "TransactionCancelled"
	}
	return fmt.Sprintf("0x%04X", c)
}

// DeviceInfo is the PTP DeviceInfo dataset.
type DeviceInfo struct {
	StandardVersion        uint16
	VendorExtensionID      uint32
	VendorExtensionVersion uint16
	VendorExtensionDesc    string
	FunctionalMode         uint16
	Operations             []uint16
	Events                 []uint16
	DeviceProps            []uint16
	CaptureFormats         []uint16
	ImageFormats           []uint16
	Manufacturer           string
	Model                  string
	DeviceVersion          string
	SerialNumber           string
}

// Supports reports whether the camera lists the operation code.
func (d *DeviceInfo) Supports(op uint16) bool {
	for _, o := range d.Operations {
		if o == op {
			return true
		}
	}
	return false
}

// ParseDeviceInfo decodes a DeviceInfo dataset.
func ParseDeviceInfo(b []byte) (*DeviceInfo, error) {
	r := &reader{b: b}
	d := &DeviceInfo{}
	d.StandardVersion = r.u16()
	d.VendorExtensionID = r.u32()
	d.VendorExtensionVersion = r.u16()
	d.VendorExtensionDesc = r.str()
	d.FunctionalMode = r.u16()
	d.Operations = r.u16array()
	d.Events = r.u16array()
	d.DeviceProps = r.u16array()
	d.CaptureFormats = r.u16array()
	d.ImageFormats = r.u16array()
	d.Manufacturer = r.str()
	d.Model = r.str()
	d.DeviceVersion = r.str()
	d.SerialNumber = r.str()
	if r.err != nil && d.Model == "" {
		return d, r.err
	}
	return d, nil
}

// Encode serialises the dataset (used by the fake camera).
func (d *DeviceInfo) Encode() []byte {
	w := &Builder{}
	w.U16(d.StandardVersion)
	w.U32(d.VendorExtensionID)
	w.U16(d.VendorExtensionVersion)
	w.Str(d.VendorExtensionDesc)
	w.U16(d.FunctionalMode)
	w.U16Array(d.Operations)
	w.U16Array(d.Events)
	w.U16Array(d.DeviceProps)
	w.U16Array(d.CaptureFormats)
	w.U16Array(d.ImageFormats)
	w.Str(d.Manufacturer)
	w.Str(d.Model)
	w.Str(d.DeviceVersion)
	w.Str(d.SerialNumber)
	return w.B
}

// StorageInfo is the PTP StorageInfo dataset (subset).
type StorageInfo struct {
	StorageType        uint16
	FilesystemType     uint16
	AccessCapability   uint16
	MaxCapacity        uint64
	FreeSpaceInBytes   uint64
	FreeSpaceInImages  uint32
	StorageDescription string
	VolumeLabel        string
}

// ParseStorageInfo decodes a StorageInfo dataset.
func ParseStorageInfo(b []byte) (*StorageInfo, error) {
	r := &reader{b: b}
	s := &StorageInfo{}
	s.StorageType = r.u16()
	s.FilesystemType = r.u16()
	s.AccessCapability = r.u16()
	s.MaxCapacity = r.u64()
	s.FreeSpaceInBytes = r.u64()
	s.FreeSpaceInImages = r.u32()
	s.StorageDescription = r.str()
	s.VolumeLabel = r.str()
	return s, r.err
}

// ObjectInfo is the PTP ObjectInfo dataset.
type ObjectInfo struct {
	StorageID           uint32
	ObjectFormat        uint16
	ProtectionStatus    uint16
	CompressedSize      uint32
	ThumbFormat         uint16
	ThumbCompressedSize uint32
	ThumbPixWidth       uint32
	ThumbPixHeight      uint32
	ImagePixWidth       uint32
	ImagePixHeight      uint32
	ImageBitDepth       uint32
	ParentObject        uint32
	AssociationType     uint16
	AssociationDesc     uint32
	SequenceNumber      uint32
	Filename            string
	CaptureDate         string
	ModificationDate    string
	Keywords            string
}

// IsFolder reports whether the object is an association (folder).
func (o *ObjectInfo) IsFolder() bool { return o.ObjectFormat == FormatAssociation }

// ParseObjectInfo decodes an ObjectInfo dataset.
func ParseObjectInfo(b []byte) (*ObjectInfo, error) {
	r := &reader{b: b}
	o := &ObjectInfo{}
	o.StorageID = r.u32()
	o.ObjectFormat = r.u16()
	o.ProtectionStatus = r.u16()
	o.CompressedSize = r.u32()
	o.ThumbFormat = r.u16()
	o.ThumbCompressedSize = r.u32()
	o.ThumbPixWidth = r.u32()
	o.ThumbPixHeight = r.u32()
	o.ImagePixWidth = r.u32()
	o.ImagePixHeight = r.u32()
	o.ImageBitDepth = r.u32()
	o.ParentObject = r.u32()
	o.AssociationType = r.u16()
	o.AssociationDesc = r.u32()
	o.SequenceNumber = r.u32()
	o.Filename = r.str()
	o.CaptureDate = r.str()
	o.ModificationDate = r.str()
	o.Keywords = r.str()
	if r.err != nil && o.Filename == "" {
		return o, r.err
	}
	return o, nil
}

// Encode serialises the dataset (used by the fake camera).
func (o *ObjectInfo) Encode() []byte {
	w := &Builder{}
	w.U32(o.StorageID)
	w.U16(o.ObjectFormat)
	w.U16(o.ProtectionStatus)
	w.U32(o.CompressedSize)
	w.U16(o.ThumbFormat)
	w.U32(o.ThumbCompressedSize)
	w.U32(o.ThumbPixWidth)
	w.U32(o.ThumbPixHeight)
	w.U32(o.ImagePixWidth)
	w.U32(o.ImagePixHeight)
	w.U32(o.ImageBitDepth)
	w.U32(o.ParentObject)
	w.U16(o.AssociationType)
	w.U32(o.AssociationDesc)
	w.U32(o.SequenceNumber)
	w.Str(o.Filename)
	w.Str(o.CaptureDate)
	w.Str(o.ModificationDate)
	w.Str(o.Keywords)
	return w.B
}

// ParsePTPTime parses PTP date strings such as "20150725T174331",
// "20150725T174331.0" or "20150725T174331+0300". Times without a zone are
// treated as local time (that is what the camera clock shows).
func ParsePTPTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 15 {
		return time.Time{}, false
	}
	base := s[:15]
	rest := s[15:]
	if strings.HasPrefix(rest, ".") {
		i := 1
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		rest = rest[i:]
	}
	if rest == "Z" {
		t, err := time.Parse("20060102T150405", base)
		return t, err == nil
	}
	if len(rest) == 5 && (rest[0] == '+' || rest[0] == '-') {
		t, err := time.Parse("20060102T150405-0700", base+rest)
		return t, err == nil
	}
	t, err := time.ParseInLocation("20060102T150405", base, time.Local)
	return t, err == nil
}
