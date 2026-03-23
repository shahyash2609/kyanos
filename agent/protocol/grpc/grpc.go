package grpc

import (
	"fmt"
	"kyanos/agent/protocol"
	"kyanos/bpf"
	"net/http"
	"strings"
)

func init() {
	protocol.ParsersMap[bpf.AgentTrafficProtocolTKProtocolHTTP2] = func() protocol.ProtocolStreamParser {
		return &GrpcParser{}
	}
}

// ParsedGrpcRequest represents a parsed gRPC request (HTTP/2 request with application/grpc).
type ParsedGrpcRequest struct {
	protocol.FrameBase
	Path     string
	Host     string // :authority
	Method   string
	Headers  map[string]string
	buf      []byte
	streamID uint32
}

func (r *ParsedGrpcRequest) FormatToSummaryString() string {
	return fmt.Sprintf("[gRPC] %s %s%s", r.Method, r.Host, r.Path)
}

func (r *ParsedGrpcRequest) FormatToString() string {
	return string(r.buf)
}

func (r *ParsedGrpcRequest) IsReq() bool { return true }

func (r *ParsedGrpcRequest) StreamId() protocol.StreamId { return protocol.StreamId(r.streamID) }

// ParsedGrpcResponse represents a parsed gRPC response.
type ParsedGrpcResponse struct {
	protocol.FrameBase
	buf      []byte
	streamID uint32
}

func (r *ParsedGrpcResponse) FormatToSummaryString() string {
	return fmt.Sprintf("[gRPC] Response len: %d", r.ByteSize())
}

func (r *ParsedGrpcResponse) FormatToString() string {
	return string(r.buf)
}

func (r *ParsedGrpcResponse) IsReq() bool { return false }

func (r *ParsedGrpcResponse) StreamId() protocol.StreamId { return protocol.StreamId(r.streamID) }

func (r *ParsedGrpcResponse) Status() protocol.ResponseStatus { return protocol.SuccessStatus }

var _ protocol.ParsedMessage = (*ParsedGrpcRequest)(nil)
var _ protocol.ParsedMessage = (*ParsedGrpcResponse)(nil)
var _ protocol.StatusfulMessage = (*ParsedGrpcResponse)(nil)

// copyFirstHeaderValues returns a map of canonical header name to first value.
func copyFirstHeaderValuesFromSlice(h []HeaderField) map[string]string {
	out := make(map[string]string, len(h))
	for _, f := range h {
		key := http.CanonicalHeaderKey(f.Name)
		if _, ok := out[key]; !ok {
			out[key] = strings.TrimSpace(f.Value)
		}
	}
	return out
}

type HeaderField struct {
	Name  string
	Value string
}

// GrpcParser parses HTTP/2 frames and gRPC messages.
type GrpcParser struct {
	// HTTP/2 has separate HPACK compression contexts per direction (RFC 7541 §2.2).
	// reqHpackDecoder decodes client→server HEADERS; respHpackDecoder decodes server→client HEADERS.
	reqHpackDecoder  *hpackDecoder
	respHpackDecoder *hpackDecoder
	// partial state per stream until END_STREAM
	streams map[uint32]*streamState
}

type streamState struct {
	isRequest bool
	// Request side (client → server)
	headers      []HeaderField
	headerBlock  []byte
	body         []byte
	path         string
	method       string
	authority    string
	contentType  string
	grpcEncoding string
	// Response side (server → client)
	responseHeaderBlock  []byte
	responseHeaders      []HeaderField
	responseBody         []byte
	responseGrpcEncoding string
	endStream            bool
}

func (p *GrpcParser) initStreams() {
	if p.streams == nil {
		p.streams = make(map[uint32]*streamState)
	}
	if p.reqHpackDecoder == nil {
		p.reqHpackDecoder = newHpackDecoder()
	}
	if p.respHpackDecoder == nil {
		p.respHpackDecoder = newHpackDecoder()
	}
}
