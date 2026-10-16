package pairing

import (
	"bytes"
	"net"
	"testing"
)

func TestGUIDFormats(t *testing.T) {
	g := NewGUID()
	if g[8] != 0xff || g[9] != 0xff || g[10]&0x02 == 0 {
		t.Fatalf("shape %s", g)
	}
	p, err := ParseGUID(g.String())
	if err != nil || p != g {
		t.Fatal(err)
	}
	mac, _ := net.ParseMAC("0a:1b:2c:3d:4e:5f")
	// PlayMemories Home style: 8 zero bytes, ff ff, adapter MAC
	want := "00:00:00:00:00:00:00:00:ff:ff:0a:1b:2c:3d:4e:5f"
	if FromMAC(mac).String() != want {
		t.Fatal(FromMAC(mac))
	}
}

// Layout of the 8 KiB "set GUID" block.
func TestUSBPayloadLayout(t *testing.T) {
	g, _ := ParseGUID("00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff")
	b := USBPayload(g, "MYPC")
	if len(b) != 8192 {
		t.Fatal(len(b))
	}
	// magic1[5] zeros1[12] name[16] zeros2[144] magic2[4] guid1[8] guid2[8] zeros3[7995]
	if !bytes.Equal(b[0:5], []byte{0x25, 1, 0, 0, 8}) ||
		!bytes.Equal(b[17:25], []byte{'M', 0, 'Y', 0, 'P', 0, 'C', 0}) ||
		!bytes.Equal(b[177:181], []byte{0x10, 0, 0, 0}) ||
		!bytes.Equal(b[181:197], g[:]) {
		t.Fatal("layout mismatch")
	}
	for i, v := range b[197:] {
		if v != 0 {
			t.Fatalf("non-zero tail at %d", 197+i)
		}
	}
	if 5+12+16+144+4+8+8+7995 != 8192 {
		t.Fatal("struct size")
	}
}
