package protocol

import (
	"kyanos/bpf"
	"kyanos/common"
	"regexp"
)

// MultiProtocolHeaderFilter accepts both HTTP/1 and HTTP/2 (gRPC) traffic and
// filters requests by header name/value (exact or regex). It is used when the
// top-level "watch" command is invoked with --header or --header-regex so that
// both protocols can be captured in a single run.
type MultiProtocolHeaderFilter struct {
	TargetHeaders    map[string]string        // canonical header name -> exact value
	TargetHeaderRegs map[string]*regexp.Regexp // canonical header name -> value regex
}

var _ ProtocolFilter = MultiProtocolHeaderFilter{}

func (f MultiProtocolHeaderFilter) FilterByProtocol(p bpf.AgentTrafficProtocolT) bool {
	return p == bpf.AgentTrafficProtocolTKProtocolHTTP || p == bpf.AgentTrafficProtocolTKProtocolHTTP2
}

func (f MultiProtocolHeaderFilter) FilterByRequest() bool {
	return len(f.TargetHeaders) > 0 || len(f.TargetHeaderRegs) > 0
}

func (f MultiProtocolHeaderFilter) FilterByResponse() bool {
	return false
}

func (f MultiProtocolHeaderFilter) Filter(parsedReq ParsedMessage, _ ParsedMessage) bool {
	var headers map[string]string

	switch req := parsedReq.(type) {
	case *ParsedHttpRequest:
		headers = req.Headers
	default:
		// For gRPC (or any other protocol with a Headers field), use the
		// HeadersProvider interface.
		if hp, ok := parsedReq.(HeadersProvider); ok {
			headers = hp.GetHeaders()
		} else {
			common.ProtocolParserLog.Debugf("[MultiProtocolHeaderFilter] request type %T has no headers", parsedReq)
			return false
		}
	}

	if headers == nil {
		return false
	}

	for wantKey, wantVal := range f.TargetHeaders {
		gotVal, ok := headers[wantKey]
		if !ok || gotVal != wantVal {
			return false
		}
	}

	for wantKey, wantReg := range f.TargetHeaderRegs {
		gotVal, ok := headers[wantKey]
		if !ok || !wantReg.MatchString(gotVal) {
			return false
		}
	}

	return true
}

func (MultiProtocolHeaderFilter) Protocol() bpf.AgentTrafficProtocolT {
	return bpf.AgentTrafficProtocolTKProtocolUnset
}

// HeadersProvider is implemented by parsed request types that expose headers.
type HeadersProvider interface {
	GetHeaders() map[string]string
}
