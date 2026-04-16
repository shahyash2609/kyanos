package grpc

import (
	"context"
	"fmt"
	"kyanos/common"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	rpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// ReflectionResolver fetches proto descriptors via gRPC server reflection
// and caches them for decoding captured protobuf messages.
type ReflectionResolver struct {
	mu       sync.RWMutex
	target   string // host:port of the gRPC server
	files    *protoregistry.Files
	methods  map[string]*methodDescriptor // "/package.Service/Method" → descriptor
	resolved bool
}

type methodDescriptor struct {
	InputType  protoreflect.MessageDescriptor
	OutputType protoreflect.MessageDescriptor
}

// NewReflectionResolver creates a new resolver for the given target address.
func NewReflectionResolver(target string) *ReflectionResolver {
	return &ReflectionResolver{
		target:  target,
		methods: make(map[string]*methodDescriptor),
	}
}

// Resolve connects to the gRPC server, fetches all service descriptors via
// reflection, and caches the method input/output types.
func (r *ReflectionResolver) Resolve() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.resolved {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, r.target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("reflection: dial %s: %w", r.target, err)
	}
	defer conn.Close()

	client := rpb.NewServerReflectionClient(conn)
	stream, err := client.ServerReflectionInfo(ctx)
	if err != nil {
		return fmt.Errorf("reflection: open stream: %w", err)
	}

	// Step 1: list all services
	if err := stream.Send(&rpb.ServerReflectionRequest{
		MessageRequest: &rpb.ServerReflectionRequest_ListServices{ListServices: ""},
	}); err != nil {
		return fmt.Errorf("reflection: list services: %w", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("reflection: recv list: %w", err)
	}
	listResp := resp.GetListServicesResponse()
	if listResp == nil {
		return fmt.Errorf("reflection: unexpected response type")
	}

	// Step 2: fetch file descriptors for each service
	fdMap := make(map[string]*descriptorpb.FileDescriptorProto)
	for _, svc := range listResp.GetService() {
		if err := r.fetchFileDescriptor(stream, svc.GetName(), fdMap); err != nil {
			common.ProtocolParserLog.Warnf("reflection: skip service %s: %v", svc.GetName(), err)
			continue
		}
	}

	// Step 3: build a file registry from all collected descriptors
	files, err := buildFileRegistry(fdMap)
	if err != nil {
		return fmt.Errorf("reflection: build registry: %w", err)
	}
	r.files = files

	// Step 4: index all methods
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			sd := fd.Services().Get(i)
			for j := 0; j < sd.Methods().Len(); j++ {
				md := sd.Methods().Get(j)
				fullPath := fmt.Sprintf("/%s/%s", sd.FullName(), md.Name())
				r.methods[fullPath] = &methodDescriptor{
					InputType:  md.Input(),
					OutputType: md.Output(),
				}
			}
		}
		return true
	})

	r.resolved = true
	common.ProtocolParserLog.Infof("reflection: resolved %d methods from %s", len(r.methods), r.target)
	return nil
}

func (r *ReflectionResolver) fetchFileDescriptor(stream rpb.ServerReflection_ServerReflectionInfoClient, symbol string, fdMap map[string]*descriptorpb.FileDescriptorProto) error {
	if err := stream.Send(&rpb.ServerReflectionRequest{
		MessageRequest: &rpb.ServerReflectionRequest_FileContainingSymbol{
			FileContainingSymbol: symbol,
		},
	}); err != nil {
		return err
	}
	resp, err := stream.Recv()
	if err != nil {
		return err
	}
	fdResp := resp.GetFileDescriptorResponse()
	if fdResp == nil {
		return fmt.Errorf("no file descriptor for %s", symbol)
	}
	for _, raw := range fdResp.GetFileDescriptorProto() {
		fd := &descriptorpb.FileDescriptorProto{}
		if err := proto.Unmarshal(raw, fd); err != nil {
			continue
		}
		name := fd.GetName()
		if _, exists := fdMap[name]; exists {
			continue
		}
		fdMap[name] = fd
		// Recursively fetch dependencies
		for _, dep := range fd.GetDependency() {
			if _, exists := fdMap[dep]; !exists {
				_ = r.fetchFileDependency(stream, dep, fdMap)
			}
		}
	}
	return nil
}

func (r *ReflectionResolver) fetchFileDependency(stream rpb.ServerReflection_ServerReflectionInfoClient, filename string, fdMap map[string]*descriptorpb.FileDescriptorProto) error {
	if err := stream.Send(&rpb.ServerReflectionRequest{
		MessageRequest: &rpb.ServerReflectionRequest_FileByFilename{
			FileByFilename: filename,
		},
	}); err != nil {
		return err
	}
	resp, err := stream.Recv()
	if err != nil {
		return err
	}
	fdResp := resp.GetFileDescriptorResponse()
	if fdResp == nil {
		return fmt.Errorf("no descriptor for file %s", filename)
	}
	for _, raw := range fdResp.GetFileDescriptorProto() {
		fd := &descriptorpb.FileDescriptorProto{}
		if err := proto.Unmarshal(raw, fd); err != nil {
			continue
		}
		name := fd.GetName()
		if _, exists := fdMap[name]; exists {
			continue
		}
		fdMap[name] = fd
		for _, dep := range fd.GetDependency() {
			if _, exists := fdMap[dep]; !exists {
				_ = r.fetchFileDependency(stream, dep, fdMap)
			}
		}
	}
	return nil
}

// DecodeRequest decodes a protobuf request body for the given gRPC path.
// If the path is unknown (e.g. "/" on mid-stream capture), it tries all methods.
func (r *ReflectionResolver) DecodeRequest(path string, body []byte) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if md, ok := r.methods[path]; ok {
		return decodeDynamic(md.InputType, body)
	}
	// Path unknown — try all methods (best-effort for mid-stream captures)
	return r.tryAllMethods(body, true)
}

// DecodeResponse decodes a protobuf response body for the given gRPC path.
// If the path is unknown, it tries all methods.
func (r *ReflectionResolver) DecodeResponse(path string, body []byte) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if md, ok := r.methods[path]; ok {
		return decodeDynamic(md.OutputType, body)
	}
	// Path unknown — try all methods
	return r.tryAllMethods(body, false)
}

// tryAllMethods attempts to decode the body against all known method descriptors.
// It picks the decode that produces the most populated fields (best match heuristic).
func (r *ReflectionResolver) tryAllMethods(body []byte, isRequest bool) (string, bool) {
	var bestResult string
	var bestScore int
	for path, md := range r.methods {
		var msgDesc protoreflect.MessageDescriptor
		if isRequest {
			msgDesc = md.InputType
		} else {
			msgDesc = md.OutputType
		}
		msg := dynamicpb.NewMessage(msgDesc)
		if err := proto.Unmarshal(body, msg); err != nil {
			continue
		}
		// Score by number of fields that are set
		score := countSetFields(msg)
		if score > bestScore {
			bestScore = score
			bestResult = formatMessage(msg, msgDesc, 0)
			// Prefix with method path for context
			bestResult = fmt.Sprintf("# %s\n%s", path, bestResult)
		}
	}
	if bestScore > 0 {
		return bestResult, true
	}
	return "", false
}

// countSetFields returns how many fields are populated in the message.
func countSetFields(msg *dynamicpb.Message) int {
	count := 0
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		count++
		return true
	})
	return count
}

func decodeDynamic(msgDesc protoreflect.MessageDescriptor, data []byte) (string, bool) {
	msg := dynamicpb.NewMessage(msgDesc)
	if err := proto.Unmarshal(data, msg); err != nil {
		return "", false
	}
	// Use prototext for human-readable output
	return formatMessage(msg, msgDesc, 0), true
}

// ReflectionRegistry maps "host:port" or ":port" keys to per-target resolvers.
// It is populated at startup by the node scanner when --auto-reflect is used.
type ReflectionRegistry struct {
	mu        sync.RWMutex
	resolvers map[string]*ReflectionResolver
}

// DefaultRegistry is set from node_scanner when --auto-reflect is provided.
var DefaultRegistry *ReflectionRegistry

// NewReflectionRegistry creates an empty registry.
func NewReflectionRegistry() *ReflectionRegistry {
	return &ReflectionRegistry{resolvers: make(map[string]*ReflectionResolver)}
}

// Register adds or replaces the resolver for the given key (":port" or "host:port").
func (r *ReflectionRegistry) Register(key string, res *ReflectionResolver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolvers[key] = res
}

// Get returns the resolver for the given authority ("host:port").
// Falls back to a ":port"-only match if no exact entry exists.
func (r *ReflectionRegistry) Get(authority string) *ReflectionResolver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if res, ok := r.resolvers[authority]; ok {
		return res
	}
	_, port, err := net.SplitHostPort(authority)
	if err == nil {
		if res, ok := r.resolvers[":"+port]; ok {
			return res
		}
	}
	return nil
}

// buildFileRegistry creates a *protoregistry.Files from a map of FileDescriptorProtos.
func buildFileRegistry(fdMap map[string]*descriptorpb.FileDescriptorProto) (*protoregistry.Files, error) {
	fds := &descriptorpb.FileDescriptorSet{}
	for _, fd := range fdMap {
		fds.File = append(fds.File, fd)
	}
	return protodesc.NewFiles(fds)
}
