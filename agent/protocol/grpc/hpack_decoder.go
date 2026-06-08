package grpc

import (
	"bytes"
	"io"
	"kyanos/common"

	"golang.org/x/net/http2/hpack"
)

type hpackDecoder struct {
	dec *hpack.Decoder
	// Shadow dynamic table for best-effort decoding across frames.
	// Entries are prepended (newest first) to mirror HPACK's FIFO eviction.
	shadowDynTable []HeaderField
	// Whether we're in best-effort mode (standard decoder failed).
	bestEffort bool
}

func newHpackDecoder() *hpackDecoder {
	d := &hpackDecoder{}
	d.dec = hpack.NewDecoder(4096, func(f hpack.HeaderField) {})
	return d
}

// Decode decodes the header block fragment and returns decoded headers.
// If the standard HPACK decoder fails (e.g. stale dynamic table from a
// mid-stream join), it switches permanently to best-effort mode for this
// connection direction, maintaining its own shadow dynamic table.
func (d *hpackDecoder) Decode(p []byte) ([]HeaderField, error) {
	if d.bestEffort {
		return d.bestEffortDecode(p), nil
	}

	var out []HeaderField
	d.dec.SetEmitFunc(func(f hpack.HeaderField) {
		out = append(out, HeaderField{Name: f.Name, Value: f.Value})
	})
	defer d.dec.SetEmitFunc(func(hpack.HeaderField) {})
	_, err := d.dec.Write(p)
	if err != nil {
		// Standard decode failed — switch to best-effort mode permanently.
		d.bestEffort = true
		d.dec = nil
		result := d.bestEffortDecode(p)
		common.ProtocolParserLog.Debugf("[hpack] standard decode failed (%v), best-effort recovered %d headers (shadow table: %d)", err, len(result), len(d.shadowDynTable))
		for _, h := range result {
			common.ProtocolParserLog.Debugf("[hpack]   %s: %s", h.Name, h.Value)
		}
		return result, nil
	}
	return out, nil
}

// bestEffortDecode parses raw HPACK bytes and extracts header fields.
// It maintains a shadow dynamic table that builds up over successive calls,
// allowing it to resolve indexed references for headers it has seen before.
func (d *hpackDecoder) bestEffortDecode(p []byte) []HeaderField {
	var out []HeaderField
	buf := p
	for len(buf) > 0 {
		b := buf[0]
		switch {
		case b&128 != 0:
			// 6.1 Indexed Header Field — fully indexed name+value.
			idx, rest := readVarIntBestEffort(7, buf)
			if rest == nil {
				return out
			}
			if hf, ok := d.lookupIndex(idx); ok {
				out = append(out, hf)
			}
			buf = rest

		case b&192 == 64:
			// 6.2.1 Literal with Incremental Indexing — adds to dynamic table.
			hf, rest := d.parseLiteralField(6, buf)
			if rest == nil {
				return out
			}
			if hf.Name != "" {
				out = append(out, hf)
				// Add to shadow dynamic table (prepend = newest first).
				d.shadowDynTable = append([]HeaderField{hf}, d.shadowDynTable...)
				// Cap at ~128 entries to bound memory.
				if len(d.shadowDynTable) > 128 {
					d.shadowDynTable = d.shadowDynTable[:128]
				}
			}
			buf = rest

		case b&240 == 0:
			// 6.2.2 Literal without Indexing
			hf, rest := d.parseLiteralField(4, buf)
			if rest == nil {
				return out
			}
			if hf.Name != "" {
				out = append(out, hf)
			}
			buf = rest

		case b&240 == 16:
			// 6.2.3 Literal never Indexed
			hf, rest := d.parseLiteralField(4, buf)
			if rest == nil {
				return out
			}
			if hf.Name != "" {
				out = append(out, hf)
			}
			buf = rest

		case b&224 == 32:
			// 6.3 Dynamic Table Size Update
			_, rest := skipVarInt(5, buf)
			if rest == nil {
				return out
			}
			buf = rest

		default:
			return out
		}
	}
	return out
}

// lookupIndex resolves an HPACK index to a header field.
// Static table: indices 1-61. Dynamic table: indices 62+.
func (d *hpackDecoder) lookupIndex(idx uint64) (HeaderField, bool) {
	if idx < 1 {
		return HeaderField{}, false
	}
	if idx <= uint64(len(staticTableEntries)) {
		return staticTableEntries[idx-1], true
	}
	// Dynamic table: index 62 maps to shadowDynTable[0], 63 to [1], etc.
	dynIdx := int(idx) - len(staticTableEntries) - 1
	if dynIdx >= 0 && dynIdx < len(d.shadowDynTable) {
		return d.shadowDynTable[dynIdx], true
	}
	return HeaderField{}, false
}

// parseLiteralField parses a literal header field representation.
// Resolves names from static table, shadow dynamic table, or inline literal.
func (d *hpackDecoder) parseLiteralField(n byte, buf []byte) (HeaderField, []byte) {
	nameIdx, rest := readVarIntBestEffort(n, buf)
	if rest == nil {
		return HeaderField{}, nil
	}

	var name string
	if nameIdx > 0 {
		if hf, ok := d.lookupIndex(nameIdx); ok {
			name = hf.Name
		} else {
			// Can't resolve name — skip the value bytes to maintain position.
			_, rest = skipString(rest)
			return HeaderField{}, rest
		}
	} else {
		// Literal name (nameIdx == 0).
		name, rest = readStringBestEffort(rest)
		if rest == nil {
			return HeaderField{}, nil
		}
	}

	value, rest := readStringBestEffort(rest)
	if rest == nil {
		return HeaderField{}, nil
	}

	return HeaderField{Name: name, Value: value}, rest
}

// readVarIntBestEffort reads an HPACK variable-length integer with n-bit prefix.
func readVarIntBestEffort(n byte, p []byte) (uint64, []byte) {
	if len(p) == 0 {
		return 0, nil
	}
	mask := uint64((1 << n) - 1)
	i := uint64(p[0]) & mask
	p = p[1:]
	if i < mask {
		return i, p
	}
	var m uint64
	for len(p) > 0 {
		b := p[0]
		p = p[1:]
		i += uint64(b&127) << m
		if b&128 == 0 {
			return i, p
		}
		m += 7
		if m >= 63 {
			return 0, nil
		}
	}
	return 0, nil
}

// skipVarInt reads past a varint without returning its value.
func skipVarInt(n byte, p []byte) (uint64, []byte) {
	return readVarIntBestEffort(n, p)
}

// readStringBestEffort reads an HPACK string (Huffman or raw).
func readStringBestEffort(p []byte) (string, []byte) {
	if len(p) == 0 {
		return "", nil
	}
	isHuff := p[0]&128 != 0
	strLen, rest := readVarIntBestEffort(7, p)
	if rest == nil || uint64(len(rest)) < strLen {
		return "", nil
	}
	data := rest[:strLen]
	rest = rest[strLen:]
	if isHuff {
		decoded, err := huffmanDecodeString(data)
		if err != nil {
			return "", rest
		}
		return decoded, rest
	}
	return string(data), rest
}

// skipString reads past an HPACK string without decoding it.
func skipString(p []byte) (string, []byte) {
	if len(p) == 0 {
		return "", nil
	}
	strLen, rest := readVarIntBestEffort(7, p)
	if rest == nil || uint64(len(rest)) < strLen {
		return "", nil
	}
	return "", rest[strLen:]
}

// huffmanDecodeString decodes a Huffman-encoded HPACK string.
func huffmanDecodeString(data []byte) (string, error) {
	var buf bytes.Buffer
	if _, err := hpack.HuffmanDecode(&buf, data); err != nil {
		if err == io.ErrUnexpectedEOF {
			return buf.String(), nil
		}
		return "", err
	}
	return buf.String(), nil
}

// staticTableEntries contains the HPACK static table (RFC 7541 Appendix A).
var staticTableEntries = [...]HeaderField{
	{Name: ":authority"},                               // 1
	{Name: ":method", Value: "GET"},                    // 2
	{Name: ":method", Value: "POST"},                   // 3
	{Name: ":path", Value: "/"},                        // 4
	{Name: ":path", Value: "/index.html"},              // 5
	{Name: ":scheme", Value: "http"},                   // 6
	{Name: ":scheme", Value: "https"},                  // 7
	{Name: ":status", Value: "200"},                    // 8
	{Name: ":status", Value: "204"},                    // 9
	{Name: ":status", Value: "206"},                    // 10
	{Name: ":status", Value: "304"},                    // 11
	{Name: ":status", Value: "400"},                    // 12
	{Name: ":status", Value: "404"},                    // 13
	{Name: ":status", Value: "500"},                    // 14
	{Name: "accept-charset"},                           // 15
	{Name: "accept-encoding", Value: "gzip, deflate"},  // 16
	{Name: "accept-language"},                          // 17
	{Name: "accept-ranges"},                            // 18
	{Name: "accept"},                                   // 19
	{Name: "access-control-allow-origin"},              // 20
	{Name: "age"},                                      // 21
	{Name: "allow"},                                    // 22
	{Name: "authorization"},                            // 23
	{Name: "cache-control"},                            // 24
	{Name: "content-disposition"},                      // 25
	{Name: "content-encoding"},                         // 26
	{Name: "content-language"},                         // 27
	{Name: "content-length"},                           // 28
	{Name: "content-location"},                         // 29
	{Name: "content-range"},                            // 30
	{Name: "content-type"},                             // 31
	{Name: "cookie"},                                   // 32
	{Name: "date"},                                     // 33
	{Name: "etag"},                                     // 34
	{Name: "expect"},                                   // 35
	{Name: "expires"},                                  // 36
	{Name: "from"},                                     // 37
	{Name: "host"},                                     // 38
	{Name: "if-match"},                                 // 39
	{Name: "if-modified-since"},                        // 40
	{Name: "if-none-match"},                            // 41
	{Name: "if-range"},                                 // 42
	{Name: "if-unmodified-since"},                      // 43
	{Name: "last-modified"},                            // 44
	{Name: "link"},                                     // 45
	{Name: "location"},                                 // 46
	{Name: "max-forwards"},                             // 47
	{Name: "proxy-authenticate"},                       // 48
	{Name: "proxy-authorization"},                      // 49
	{Name: "range"},                                    // 50
	{Name: "referer"},                                  // 51
	{Name: "refresh"},                                  // 52
	{Name: "retry-after"},                              // 53
	{Name: "server"},                                   // 54
	{Name: "set-cookie"},                               // 55
	{Name: "strict-transport-security"},                // 56
	{Name: "transfer-encoding"},                        // 57
	{Name: "user-agent"},                               // 58
	{Name: "vary"},                                     // 59
	{Name: "via"},                                      // 60
	{Name: "www-authenticate"},                         // 61
}
