package grpc

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseFrameHeader(t *testing.T) {
	t.Run("too_short", func(t *testing.T) {
		_, ok := parseFrameHeader([]byte{0, 0, 0})
		assert.False(t, ok)
	})

	t.Run("data_frame", func(t *testing.T) {
		b := make([]byte, frameHeaderLen)
		b[0] = 0
		b[1] = 0
		b[2] = 100 // 24-bit length = 100
		b[3] = FrameData
		b[4] = FlagEndStream
		binary.BigEndian.PutUint32(b[5:9], 1) // stream ID 1

		hdr, ok := parseFrameHeader(b)
		assert.True(t, ok)
		assert.Equal(t, uint32(100), hdr.Length)
		assert.Equal(t, byte(FrameData), hdr.Type)
		assert.Equal(t, byte(FlagEndStream), hdr.Flags)
		assert.Equal(t, uint32(1), hdr.StreamID)
		assert.Equal(t, frameHeaderLen+100, hdr.FrameSize())
	})

	t.Run("stream_id_31bit", func(t *testing.T) {
		b := make([]byte, frameHeaderLen)
		b[5] = 0xff
		b[6] = 0xff
		b[7] = 0xff
		b[8] = 0xff
		hdr, ok := parseFrameHeader(b)
		assert.True(t, ok)
		assert.Equal(t, uint32(0x7fffffff), hdr.StreamID)
	})
}
