package grpc

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"kyanos/agent/buffer"
	"kyanos/agent/protocol"
	"testing"

	"github.com/stretchr/testify/assert"
)

// buildGrpcMessage builds a single gRPC length-prefixed message: 1 byte compressed + 4 byte length (BE) + body.
func buildGrpcMessage(compressed bool, body []byte) []byte {
	out := make([]byte, 5+len(body))
	if compressed {
		out[0] = 1
	} else {
		out[0] = 0
	}
	binary.BigEndian.PutUint32(out[1:5], uint32(len(body)))
	copy(out[5:], body)
	return out
}

func TestDecodeGrpcBody(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out := decodeGrpcBody(nil, "")
		assert.Nil(t, out)
	})

	t.Run("single_uncompressed", func(t *testing.T) {
		body := buildGrpcMessage(false, []byte("hello"))
		out := decodeGrpcBody(body, "")
		assert.Equal(t, []byte("hello"), out)
	})

	t.Run("single_gzip", func(t *testing.T) {
		plain := []byte("hello gzip")
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write(plain)
		w.Close()
		body := buildGrpcMessage(true, buf.Bytes())
		out := decodeGrpcBody(body, "gzip")
		assert.Equal(t, plain, out)
	})

	t.Run("multiple_messages", func(t *testing.T) {
		msg1 := buildGrpcMessage(false, []byte("a"))
		msg2 := buildGrpcMessage(false, []byte("b"))
		body := append(msg1, msg2...)
		out := decodeGrpcBody(body, "")
		assert.Equal(t, []byte("ab"), out)
	})

	t.Run("invalid_short_body_returns_unchanged", func(t *testing.T) {
		short := []byte{0, 0, 0}
		out := decodeGrpcBody(short, "")
		assert.Equal(t, short, out)
	})
}

func TestGrpcParser_FindBoundary(t *testing.T) {
	p := &GrpcParser{}
	sb := buffer.New(1024)
	sb.Add(0, []byte("x"), 0)
	assert.Equal(t, 0, p.FindBoundary(sb, protocol.Request, 0))
	assert.Equal(t, -1, p.FindBoundary(buffer.New(1024), protocol.Request, 0))
}

func TestGrpcParser_Match(t *testing.T) {
	p := &GrpcParser{}
	fb := protocol.NewFrameBase(0, 0, 0)
	req1 := &ParsedGrpcRequest{FrameBase: fb, streamID: 1}
	req3 := &ParsedGrpcRequest{FrameBase: fb, streamID: 3}
	resp1 := &ParsedGrpcResponse{FrameBase: fb, streamID: 1}
	resp3 := &ParsedGrpcResponse{FrameBase: fb, streamID: 3}

	reqStreams := map[protocol.StreamId]*protocol.ParsedMessageQueue{
		1: ptr(protocol.ParsedMessageQueue{req1}),
		3: ptr(protocol.ParsedMessageQueue{req3}),
	}
	respStreams := map[protocol.StreamId]*protocol.ParsedMessageQueue{
		1: ptr(protocol.ParsedMessageQueue{resp1}),
		3: ptr(protocol.ParsedMessageQueue{resp3}),
	}

	records := p.Match(reqStreams, respStreams)
	assert.Len(t, records, 2)
	for _, r := range records {
		assert.NotNil(t, r.Req)
		assert.NotNil(t, r.Resp)
		assert.Equal(t, r.Req.StreamId(), r.Resp.StreamId())
	}
}

func ptr(q protocol.ParsedMessageQueue) *protocol.ParsedMessageQueue {
	return &q
}
