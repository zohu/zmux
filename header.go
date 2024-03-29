package zmux

import (
	"encoding/binary"
	"fmt"
)

const (
	headVersion uint8 = 0

	headSizeVersion = 1
	headSizeType    = 1
	headSizeFlags   = 2
	headSizeStream  = 4
	headSizeLength  = 4
	headerSize      = headSizeVersion + headSizeType + headSizeFlags + headSizeStream + headSizeLength
)

type msgType uint8

const (
	msgTypeData msgType = iota
	msgTypeUpdate
	msgTypePing
	msgTypeGoAway
)

type header []byte

func newHeader() header {
	return make([]byte, headerSize)
}
func (h header) Version() uint8 {
	return h[0]
}
func (h header) MsgType() msgType {
	return msgType(h[1])
}
func (h header) Flags() uint16 {
	return binary.BigEndian.Uint16(h[2:4])
}
func (h header) StreamID() uint32 {
	return binary.BigEndian.Uint32(h[4:8])
}
func (h header) Length() uint32 {
	return binary.BigEndian.Uint32(h[8:12])
}
func (h header) String() string {
	return fmt.Sprintf("Vsn:%d Type:%d Flags:%d StreamID:%d Length:%d",
		h.Version(), h.MsgType(), h.Flags(), h.StreamID(), h.Length())
}

func (h header) encode(msgType msgType, flags uint16, streamID uint32, length uint32) {
	h[0] = headVersion
	h[1] = uint8(msgType)
	binary.BigEndian.PutUint16(h[2:4], flags)
	binary.BigEndian.PutUint32(h[4:8], streamID)
	binary.BigEndian.PutUint32(h[8:12], length)
}
