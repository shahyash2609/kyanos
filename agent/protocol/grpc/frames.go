package grpc

import (
	"encoding/binary"
)

// HTTP/2 frame format (RFC 7540): 9-byte header + payload
const frameHeaderLen = 9

// Frame types
const (
	FrameData     = 0x0
	FrameHeaders  = 0x1
	FrameSettings = 0x4
	FrameGoAway   = 0x7
)

// Flags
const (
	FlagEndStream  = 0x1
	FlagEndHeaders = 0x4
	FlagPadded     = 0x8
)

type FrameHeader struct {
	Length   uint32 // 24-bit
	Type     byte
	Flags    byte
	StreamID uint32 // 31-bit
}

func parseFrameHeader(b []byte) (FrameHeader, bool) {
	if len(b) < frameHeaderLen {
		return FrameHeader{}, false
	}
	length := uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
	streamID := binary.BigEndian.Uint32(b[5:9]) & 0x7fffffff
	return FrameHeader{
		Length:   length,
		Type:     b[3],
		Flags:    b[4],
		StreamID: streamID,
	}, true
}

// FrameSize returns total bytes for this frame (header + payload).
func (f FrameHeader) FrameSize() int {
	return frameHeaderLen + int(f.Length)
}
