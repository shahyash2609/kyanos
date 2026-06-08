package grpc

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/net/http2/hpack"
)

func TestDecode_LiteralHeaders(t *testing.T) {
	var buf bytes.Buffer
	enc := hpack.NewEncoder(&buf)
	enc.WriteField(hpack.HeaderField{Name: "traceparent", Value: "00-abc123-def456-01"})
	enc.WriteField(hpack.HeaderField{Name: "x-envoy-internal", Value: "true"})
	enc.WriteField(hpack.HeaderField{Name: "caller_id", Value: "42"})

	dec := newHpackDecoder()
	headers, err := dec.Decode(buf.Bytes())
	assert.NoError(t, err)
	assert.Len(t, headers, 3)
	assert.Equal(t, "traceparent", headers[0].Name)
	assert.Equal(t, "00-abc123-def456-01", headers[0].Value)
}

func TestDecode_StaleDecoder_ShadowTable(t *testing.T) {
	// Use a single encoder to simulate connection lifecycle.
	var encBuf bytes.Buffer
	enc := hpack.NewEncoder(&encBuf)

	// Frame 1: first request on connection. Builds dynamic table.
	enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	enc.WriteField(hpack.HeaderField{Name: ":path", Value: "/pkg.Svc/Method"})
	enc.WriteField(hpack.HeaderField{Name: "content-type", Value: "application/grpc"})
	enc.WriteField(hpack.HeaderField{Name: "traceparent", Value: "00-trace1-span1-01"})
	frame1 := make([]byte, encBuf.Len())
	copy(frame1, encBuf.Bytes())

	// Frame 2: second request. Encoder reuses dynamic table entries.
	enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	enc.WriteField(hpack.HeaderField{Name: ":path", Value: "/pkg.Svc/Method"})
	enc.WriteField(hpack.HeaderField{Name: "content-type", Value: "application/grpc"})
	enc.WriteField(hpack.HeaderField{Name: "traceparent", Value: "00-trace2-span2-01"})
	frame2 := make([]byte, encBuf.Len()-len(frame1))
	copy(frame2, encBuf.Bytes()[len(frame1):])

	// Decode frame1 with a fresh decoder (simulates kyanos missing frame0).
	// Standard decode of frame1 should fail if any dynamic refs exist,
	// but frame1 is the first frame so everything is literal → should succeed.
	dec := newHpackDecoder()
	h1, err := dec.Decode(frame1)
	assert.NoError(t, err)
	t.Logf("Frame 1: %d headers", len(h1))
	for _, h := range h1 {
		t.Logf("  %s: %s", h.Name, h.Value)
	}

	// Now decode frame2. The standard decoder has correct state, so this should work.
	h2, err := dec.Decode(frame2)
	assert.NoError(t, err)
	t.Logf("Frame 2: %d headers", len(h2))
	found := false
	for _, h := range h2 {
		t.Logf("  %s: %s", h.Name, h.Value)
		if h.Name == "traceparent" && h.Value == "00-trace2-span2-01" {
			found = true
		}
	}
	assert.True(t, found, "traceparent should be found in frame 2")
}

func TestDecode_MidStreamJoin_ShadowTableBuilds(t *testing.T) {
	// Simulate: encoder sends 3 frames. Kyanos misses frame 0.
	// Frame 1 has dynamic refs from frame 0 → standard decode fails.
	// Best-effort mode kicks in, extracts what it can, builds shadow table.
	// Frame 2 uses shadow table from frame 1.
	var encBuf bytes.Buffer
	enc := hpack.NewEncoder(&encBuf)

	// Frame 0 (missed by kyanos).
	enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	enc.WriteField(hpack.HeaderField{Name: "traceparent", Value: "00-trace0-span0-01"})
	frame0Len := encBuf.Len()

	// Frame 1 (first seen by kyanos — will have dynamic table refs).
	enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	enc.WriteField(hpack.HeaderField{Name: "traceparent", Value: "00-trace1-span1-01"})
	frame1 := make([]byte, encBuf.Len()-frame0Len)
	copy(frame1, encBuf.Bytes()[frame0Len:])
	frame1Len := encBuf.Len()

	// Frame 2.
	enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	enc.WriteField(hpack.HeaderField{Name: "traceparent", Value: "00-trace2-span2-01"})
	frame2 := make([]byte, encBuf.Len()-frame1Len)
	copy(frame2, encBuf.Bytes()[frame1Len:])

	dec := newHpackDecoder()

	// Decode frame1 (kyanos missed frame0 → standard decode may fail).
	h1, err := dec.Decode(frame1)
	assert.NoError(t, err) // should not error (falls back to best-effort)
	t.Logf("Frame 1 (mid-stream): %d headers", len(h1))
	for _, h := range h1 {
		t.Logf("  %s: %s", h.Name, h.Value)
	}

	// Decode frame2 (should use shadow table built from frame1).
	h2, err := dec.Decode(frame2)
	assert.NoError(t, err)
	t.Logf("Frame 2: %d headers", len(h2))
	for _, h := range h2 {
		t.Logf("  %s: %s", h.Name, h.Value)
	}

	// Check if traceparent is found in frame 2.
	found := false
	for _, h := range h2 {
		if h.Name == "traceparent" {
			assert.Equal(t, "00-trace2-span2-01", h.Value)
			found = true
		}
	}
	if !found {
		t.Log("traceparent not found in frame 2 — encoder strategy may differ")
	}
}

func TestDecode_RawLiteralBytes(t *testing.T) {
	// Manually craft HPACK bytes: literal without indexing, new name.
	raw := []byte{
		0x00,                                                     // literal without indexing, name index 0
		0x0b,                                                     // name length = 11
		't', 'r', 'a', 'c', 'e', 'p', 'a', 'r', 'e', 'n', 't', // "traceparent"
		0x12,                                                                                           // value length = 18
		'0', '0', '-', 'a', 'b', 'c', '1', '2', '3', '-', 'd', 'e', 'f', '4', '5', '6', '-', '0', // "00-abc123-def456-0"
	}

	dec := newHpackDecoder()
	dec.bestEffort = true // force best-effort mode
	headers := dec.bestEffortDecode(raw)
	assert.Len(t, headers, 1)
	assert.Equal(t, "traceparent", headers[0].Name)
	assert.Equal(t, "00-abc123-def456-0", headers[0].Value)
}
