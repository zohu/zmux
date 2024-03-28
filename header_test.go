package zmux

import "testing"

func TestEncodeDecode(t *testing.T) {
	hdr := newHeader()
	hdr.encode(msgTypeUpdate, flagACK|flagRST, 1234, 4321)

	if hdr.Version() != headVersion {
		t.Fatalf("bad: %v", hdr)
	}
	if hdr.MsgType() != msgTypeUpdate {
		t.Fatalf("bad: %v", hdr)
	}
	if hdr.Flags() != flagACK|flagRST {
		t.Fatalf("bad: %v", hdr)
	}
	if hdr.StreamID() != 1234 {
		t.Fatalf("bad: %v", hdr)
	}
	if hdr.Length() != 4321 {
		t.Fatalf("bad: %v", hdr)
	}
	t.Log(hdr.String())
}
