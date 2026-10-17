package ptpip

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func pkt(n uint32, body []byte) io.Reader {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, n)
	_ = binary.Write(&b, binary.LittleEndian, uint32(7))
	b.Write(body)
	return &b
}

func TestReadPacketSizes(t *testing.T) {
	small := bytes.Repeat([]byte{1}, 100)
	p, err := ReadPacket(pkt(8+100, small))
	if err != nil || p.Type != 7 || !bytes.Equal(p.Payload, small) {
		t.Fatalf("small packet: %v", err)
	}
	big := bytes.Repeat([]byte{2}, 3<<20)
	p, err = ReadPacket(pkt(uint32(8+len(big)), big))
	if err != nil || !bytes.Equal(p.Payload, big) {
		t.Fatalf("big packet: %v", err)
	}
	// a peer that announces 60 MB and sends 10 bytes gets an error, not 60 MB of memory
	if _, err := ReadPacket(pkt(8+60<<20, make([]byte, 10))); err == nil {
		t.Fatal("truncated big packet must fail")
	}
	// impossible lengths
	for _, n := range []uint32{0, 7, maxPacket + 1, 0xFFFFFFFF} {
		if _, err := ReadPacket(pkt(n, nil)); err == nil {
			t.Errorf("length %d must be refused", n)
		}
	}
}
