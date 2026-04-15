package grpc

import (
	"bytes"
	"encoding/binary"
	"kyanos/agent/buffer"
	"kyanos/agent/protocol"
	"strings"
)

const frameContinuation = 0x9

// FindBoundary for HTTP/2: we parse from the start of the buffer (frame stream).
func (p *GrpcParser) FindBoundary(streamBuffer *buffer.StreamBuffer, _ protocol.MessageType, startPos int) int {
	if streamBuffer.IsEmpty() {
		return -1
	}
	// We always start parsing from 0; no need to skip to a "next" boundary like HTTP/1.
	return startPos
}

// Match pairs request and response by stream ID.
func (p *GrpcParser) Match(reqStreams map[protocol.StreamId]*protocol.ParsedMessageQueue, respStreams map[protocol.StreamId]*protocol.ParsedMessageQueue) []protocol.Record {
	records := make([]protocol.Record, 0)
	for streamID, reqQ := range reqStreams {
		if reqQ == nil || len(*reqQ) == 0 {
			continue
		}
		respQ := respStreams[streamID]
		if respQ == nil || len(*respQ) == 0 {
			continue
		}
		// Pair first request with first response for this stream
		record := protocol.Record{
			Req:            (*reqQ)[0],
			Resp:           (*respQ)[0],
			ResponseStatus: protocol.SuccessStatus,
		}
		records = append(records, record)
	}
	return records
}

func (p *GrpcParser) ParseStream(streamBuffer *buffer.StreamBuffer, messageType protocol.MessageType) protocol.ParseResult {
	p.initStreams()
	head := streamBuffer.Head()
	if head == nil {
		return protocol.ParseResult{ParseState: protocol.NeedsMoreData}
	}
	buf := head.Buffer()
	ts, ok := streamBuffer.FindTimestampBySeq(head.LeftBoundary())
	if !ok {
		return protocol.ParseResult{ParseState: protocol.Invalid}
	}
	seq := head.LeftBoundary()

	var consumed int
	var parsed []protocol.ParsedMessage

	for consumed < len(buf) {
		if len(buf)-consumed < frameHeaderLen {
			break
		}
		hdr, ok := parseFrameHeader(buf[consumed:])
		if !ok {
			break
		}
		frameLen := hdr.FrameSize()
		if len(buf)-consumed < frameLen {
			break
		}
		payload := buf[consumed+frameHeaderLen : consumed+frameLen]
		consumed += frameLen

		switch hdr.Type {
		case FrameHeaders, frameContinuation:
			ss := p.getOrCreateStream(hdr.StreamID)
			if ss == nil {
				continue
			}
			if hdr.Flags&FlagPadded != 0 && len(payload) > 0 {
				padLen := int(payload[0])
				if padLen < len(payload) {
					payload = payload[1 : len(payload)-padLen]
				}
			}
			if messageType == protocol.Response {
				ss.responseHeaderBlock = append(ss.responseHeaderBlock, payload...)
				if hdr.Flags&FlagEndHeaders != 0 {
					decoded, err := p.respHpackDecoder.Decode(ss.responseHeaderBlock)
					if err == nil {
						ss.responseHeaders = decoded
						for _, f := range decoded {
							if f.Name == "grpc-encoding" {
								ss.responseGrpcEncoding = strings.TrimSpace(strings.ToLower(f.Value))
								break
							}
						}
					}
					ss.responseHeaderBlock = nil
				}
			} else {
				ss.headerBlock = append(ss.headerBlock, payload...)
				if hdr.Flags&FlagEndHeaders != 0 {
					decoded, err := p.reqHpackDecoder.Decode(ss.headerBlock)
					if err == nil {
						ss.headers = decoded
						for _, f := range decoded {
							switch f.Name {
							case ":path":
								ss.path = f.Value
							case ":method":
								ss.method = f.Value
							case ":authority":
								ss.authority = f.Value
							case "content-type":
								ss.contentType = f.Value
							case "grpc-encoding":
								ss.grpcEncoding = strings.TrimSpace(strings.ToLower(f.Value))
							}
						}
					}
					ss.headerBlock = nil
				}
			}
			if hdr.Flags&FlagEndStream != 0 {
				ss.endStream = true
			}
		case FrameData:
			ss := p.getOrCreateStream(hdr.StreamID)
			if ss == nil {
				continue
			}
			if hdr.Flags&FlagPadded != 0 && len(payload) > 0 {
				padLen := int(payload[0])
				if padLen < len(payload) {
					payload = payload[1 : len(payload)-padLen]
				}
			}
			if messageType == protocol.Response {
				ss.responseBody = append(ss.responseBody, payload...)
			} else {
				ss.body = append(ss.body, payload...)
			}
			if hdr.Flags&FlagEndStream != 0 {
				ss.endStream = true
			}
		default:
			// Ignore SETTINGS, etc.
			continue
		}

		// Emit message when stream ended
		if ss := p.streams[hdr.StreamID]; ss != nil && ss.endStream {
			if msg := p.buildMessage(hdr.StreamID, ss, ts, seq, consumed, messageType); msg != nil {
				parsed = append(parsed, msg)
			}
			delete(p.streams, hdr.StreamID)
		}
	}

	if consumed == 0 {
		return protocol.ParseResult{ParseState: protocol.NeedsMoreData}
	}
	if len(parsed) == 0 {
		return protocol.ParseResult{
			ParseState:     protocol.Success,
			ReadBytes:      consumed,
			ParsedMessages: []protocol.ParsedMessage{},
		}
	}
	return protocol.ParseResult{
		ParseState:     protocol.Success,
		ReadBytes:      consumed,
		ParsedMessages: parsed,
	}
}

func (p *GrpcParser) getOrCreateStream(streamID uint32) *streamState {
	if p.streams == nil {
		p.streams = make(map[uint32]*streamState)
	}
	s, ok := p.streams[streamID]
	if !ok {
		s = &streamState{
			isRequest: streamID%2 == 1,
			headers:   nil,
			body:      nil,
		}
		p.streams[streamID] = s
	}
	return s
}

func (p *GrpcParser) buildMessage(streamID uint32, ss *streamState, ts, seq uint64, readBytes int, messageType protocol.MessageType) protocol.ParsedMessage {
	fb := protocol.NewFrameBase(ts, readBytes, seq)
	path := ss.path
	method := ss.method
	authority := ss.authority
	if path == "" {
		path = "/"
	}
	if method == "" {
		method = "POST"
	}

	body := ss.body
	// gRPC length-prefixed messages; decompress if grpc-encoding: gzip
	if ss.contentType == "application/grpc" && len(body) > 0 {
		body = decodeGrpcBody(body, ss.grpcEncoding)
	}

	var buf []byte
	// Request: only when we're parsing request buffer and this stream is client-initiated
	if ss.isRequest && messageType == protocol.Request {
		// Cache path for response-side reflection decoding
		if path != "/" {
			p.streamPaths[streamID] = path
		}
		displayBody := body
		if p.Reflection != nil && len(body) > 0 {
			if decoded, ok := p.Reflection.DecodeRequest(path, body); ok {
				displayBody = []byte(decoded)
			}
		}
		buf = buildRequestDisplay(path, method, authority, ss.headers, displayBody)
		return &ParsedGrpcRequest{
			FrameBase: fb,
			Path:      path,
			Host:      authority,
			Method:    method,
			Headers:   copyFirstHeaderValuesFromSlice(ss.headers),
			buf:       buf,
			streamID:  streamID,
		}
	}
	// Response: when we're parsing response buffer (same stream ID, server sends body)
	if messageType == protocol.Response {
		respBody := ss.responseBody
		if len(respBody) > 0 {
			respBody = decodeGrpcBody(respBody, ss.responseGrpcEncoding)
		}
		displayBody := respBody
		if p.Reflection != nil && len(respBody) > 0 {
			// Use cached path from request parsing for reflection lookup
			reflectPath := path
			if reflectPath == "/" {
				if cached, ok := p.streamPaths[streamID]; ok {
					reflectPath = cached
				}
			}
			if decoded, ok := p.Reflection.DecodeResponse(reflectPath, respBody); ok {
				displayBody = []byte(decoded)
			}
			delete(p.streamPaths, streamID)
		}
		buf = buildResponseDisplay(ss.responseHeaders, displayBody)
		return &ParsedGrpcResponse{
			FrameBase: fb,
			buf:       buf,
			streamID:  streamID,
		}
	}
	return nil
}

func buildRequestDisplay(path, method, authority string, headers []HeaderField, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString(method + " " + path + " HTTP/2\r\n")
	for _, f := range headers {
		b.WriteString(f.Name + ": " + f.Value + "\r\n")
	}
	b.WriteString("\r\n")
	b.Write(body)
	return b.Bytes()
}

func buildResponseDisplay(headers []HeaderField, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("HTTP/2 200\r\n")
	for _, f := range headers {
		b.WriteString(f.Name + ": " + f.Value + "\r\n")
	}
	b.WriteString("\r\n")
	b.Write(body)
	return b.Bytes()
}

// decodeGrpcBody parses gRPC length-prefixed messages and decompresses if encoding is gzip.
func decodeGrpcBody(body []byte, grpcEncoding string) []byte {
	var out []byte
	pos := 0
	for pos+5 <= len(body) {
		compressed := body[pos] != 0
		msgLen := binary.BigEndian.Uint32(body[pos+1 : pos+5])
		pos += 5
		if int(msgLen) > len(body)-pos {
			break
		}
		msg := body[pos : pos+int(msgLen)]
		pos += int(msgLen)
		if compressed && (grpcEncoding == "gzip" || grpcEncoding == "x-gzip") {
			dec, ok := decompressHTTPBody(msg, "gzip")
			if ok {
				out = append(out, dec...)
			} else {
				out = append(out, msg...)
			}
		} else {
			out = append(out, msg...)
		}
	}
	if len(out) == 0 {
		return body
	}
	return out
}

// decompressHTTPBody mirrors protocol.decompressHTTPBody for gzip (used for grpc-encoding).
func decompressHTTPBody(body []byte, contentEncoding string) ([]byte, bool) {
	return commonDecompress(body, contentEncoding)
}
