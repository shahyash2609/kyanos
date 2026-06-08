package grpc

import (
	"kyanos/agent/protocol"
	"kyanos/bpf"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGrpcFilter_FilterByProtocol(t *testing.T) {
	f := GrpcFilter{}
	assert.True(t, f.FilterByProtocol(bpf.AgentTrafficProtocolTKProtocolHTTP2))
	assert.False(t, f.FilterByProtocol(bpf.AgentTrafficProtocolTKProtocolHTTP))
}

func TestGrpcFilter_FilterByRequest(t *testing.T) {
	assert.False(t, GrpcFilter{}.FilterByRequest())
	assert.True(t, GrpcFilter{TargetPath: "/foo"}.FilterByRequest())
	assert.True(t, GrpcFilter{TargetPathPrefix: "/"}.FilterByRequest())
	assert.True(t, GrpcFilter{TargetHostName: "x"}.FilterByRequest())
	assert.True(t, GrpcFilter{TargetMethods: []string{"POST"}}.FilterByRequest())
	assert.True(t, GrpcFilter{TargetHeaders: map[string]string{"x": "y"}}.FilterByRequest())
	assert.True(t, GrpcFilter{TargetPathReg: regexp.MustCompile(".*")}.FilterByRequest())
}

func TestGrpcFilter_Filter(t *testing.T) {
	fb := protocol.NewFrameBase(0, 0, 0)

	t.Run("not_grpc_request", func(t *testing.T) {
		f := GrpcFilter{}
		// ParsedGrpcResponse is ParsedMessage but not *ParsedGrpcRequest, so Filter returns false
		ok := f.Filter(&ParsedGrpcResponse{FrameBase: fb}, nil)
		assert.False(t, ok)
	})

	t.Run("filter_by_path", func(t *testing.T) {
		f := GrpcFilter{TargetPath: "/pkg.Svc/Method"}
		req := &ParsedGrpcRequest{FrameBase: fb, Path: "/pkg.Svc/Method"}
		assert.True(t, f.Filter(req, nil))
		req.Path = "/other"
		assert.False(t, f.Filter(req, nil))
	})

	t.Run("filter_by_path_prefix", func(t *testing.T) {
		f := GrpcFilter{TargetPathPrefix: "/pkg."}
		req := &ParsedGrpcRequest{FrameBase: fb, Path: "/pkg.Svc/Method"}
		assert.True(t, f.Filter(req, nil))
		assert.False(t, f.Filter(&ParsedGrpcRequest{FrameBase: fb, Path: "/other"}, nil))
	})

	t.Run("filter_by_header", func(t *testing.T) {
		f := GrpcFilter{TargetHeaders: map[string]string{"X-Request-Id": "abc"}}
		assert.True(t, f.Filter(&ParsedGrpcRequest{FrameBase: fb, Headers: map[string]string{"X-Request-Id": "abc"}}, nil))
		assert.False(t, f.Filter(&ParsedGrpcRequest{FrameBase: fb, Headers: map[string]string{"X-Request-Id": "wrong"}}, nil))
	})

	t.Run("filter_by_host", func(t *testing.T) {
		f := GrpcFilter{TargetHostName: "svc:50051"}
		req := &ParsedGrpcRequest{FrameBase: fb, Host: "svc:50051"}
		assert.True(t, f.Filter(req, nil))
		assert.False(t, f.Filter(&ParsedGrpcRequest{FrameBase: fb, Host: "other"}, nil))
	})
}
