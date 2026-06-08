package grpc

import (
	"kyanos/agent/protocol"
	"kyanos/bpf"
	"kyanos/common"
	"regexp"
	"slices"
	"strings"
)

var _ protocol.ProtocolFilter = GrpcFilter{}

type GrpcFilter struct {
	TargetPath       string
	TargetPathReg    *regexp.Regexp
	TargetPathPrefix string
	TargetHostName   string
	TargetMethods    []string
	TargetHeaders    map[string]string
	TargetHeaderRegs map[string]*regexp.Regexp
}

func (f GrpcFilter) FilterByProtocol(p bpf.AgentTrafficProtocolT) bool {
	return p == bpf.AgentTrafficProtocolTKProtocolHTTP2
}

func (f GrpcFilter) FilterByRequest() bool {
	return len(f.TargetPath) > 0 ||
		f.TargetPathReg != nil ||
		len(f.TargetPathPrefix) > 0 ||
		len(f.TargetMethods) > 0 ||
		len(f.TargetHostName) > 0 ||
		len(f.TargetHeaders) > 0 ||
		len(f.TargetHeaderRegs) > 0
}

func (GrpcFilter) FilterByResponse() bool {
	return false
}

func (f GrpcFilter) Filter(parsedReq protocol.ParsedMessage, _ protocol.ParsedMessage) bool {
	req, ok := parsedReq.(*ParsedGrpcRequest)
	if !ok {
		common.ProtocolParserLog.Warnf("[GrpcFilter] cast to gRPC request failed")
		return false
	}
	common.ProtocolParserLog.Debugf("[GrpcFilter] filtering request: %v", req)

	if len(f.TargetPath) > 0 && f.TargetPath != req.Path {
		return false
	}
	if len(f.TargetPathPrefix) > 0 && !strings.HasPrefix(req.Path, f.TargetPathPrefix) {
		return false
	}
	if f.TargetPathReg != nil && !f.TargetPathReg.MatchString(req.Path) {
		return false
	}
	if len(f.TargetMethods) > 0 && !slices.Contains(f.TargetMethods, req.Method) {
		return false
	}
	if f.TargetHostName != "" && f.TargetHostName != req.Host {
		return false
	}
	if req.Headers != nil {
		for wantKey, wantVal := range f.TargetHeaders {
			gotVal, ok := req.Headers[wantKey]
			if !ok || gotVal != wantVal {
				return false
			}
		}
		for wantKey, wantReg := range f.TargetHeaderRegs {
			gotVal, ok := req.Headers[wantKey]
			if !ok || !wantReg.MatchString(gotVal) {
				return false
			}
		}
	} else if len(f.TargetHeaders) > 0 || len(f.TargetHeaderRegs) > 0 {
		return false
	}
	return true
}

func (GrpcFilter) Protocol() bpf.AgentTrafficProtocolT {
	return bpf.AgentTrafficProtocolTKProtocolHTTP2
}
