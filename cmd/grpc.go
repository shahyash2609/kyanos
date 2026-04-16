package cmd

import (
	"fmt"
	"kyanos/agent/metadata"
	"kyanos/agent/protocol/grpc"
	"kyanos/common"
	"regexp"
	"sync"

	"github.com/spf13/cobra"
)

// initAutoReflect discovers pod endpoints via CRI and probes each for gRPC server
// reflection. Falls back to host-level port scan if CRI is unavailable.
// Successful resolvers are registered in grpc.DefaultRegistry keyed by ":port".
func initAutoReflect() {
	// Try CRI-based pod endpoint discovery first.
	endpoints := metadata.DiscoverPodEndpoints(options.CriRuntimeEndpoint, options.ContainerdEndpoint)
	if len(endpoints) == 0 {
		// Fallback: host network namespace ports (works for host-network pods).
		ports := metadata.DiscoverListeningPorts()
		if len(ports) == 0 {
			common.AgentLog.Infoln("auto-reflect: no listening ports found")
			return
		}
		for _, p := range ports {
			endpoints = append(endpoints, metadata.PodEndpoint{IP: "localhost", Port: p})
		}
	}

	common.AgentLog.Infof("auto-reflect: probing %d endpoint(s) for gRPC reflection", len(endpoints))
	registry := grpc.NewReflectionRegistry()
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, ep := range endpoints {
		ep := ep
		wg.Add(1)
		go func() {
			defer wg.Done()
			target := fmt.Sprintf("%s:%d", ep.IP, ep.Port)
			resolver := grpc.NewReflectionResolver(target)
			if err := resolver.Resolve(); err != nil {
				common.AgentLog.Debugf("auto-reflect: %s not gRPC or reflection unavailable: %v", target, err)
				return
			}
			key := fmt.Sprintf(":%d", ep.Port)
			common.AgentLog.Infof("auto-reflect: registered reflection for %s (key=%s)", target, key)
			mu.Lock()
			registry.Register(key, resolver)
			mu.Unlock()
		}()
	}
	wg.Wait()
	grpc.DefaultRegistry = registry
}

func initGrpcReflectionForTarget(target string) {
	resolver := grpc.NewReflectionResolver(target)
	if err := resolver.Resolve(); err != nil {
		logger.Warnf("gRPC reflection failed for %s: %v (protobuf bodies will be raw bytes)", target, err)
		return
	}
	grpc.DefaultReflection = resolver
}

var grpcCmd = &cobra.Command{
	Use:   "grpc [--method METHODS|--path PATH|--path-regex REGEX|--path-prefix PREFIX|--host HOSTNAME|--header HEADER:VALUE]",
	Short: "watch gRPC (HTTP/2) messages",
	Long:  `Filter gRPC messages based on method, path (strict, regex, prefix), host, or request headers. Supports grpc-encoding: gzip decompression. Filter flags are combined with AND(&&).`,
	Run: func(cmd *cobra.Command, args []string) {
		methods, err := cmd.Flags().GetStringSlice("method")
		if err != nil {
			logger.Fatalf("invalid method: %v\n", err)
		}
		path, err := cmd.Flags().GetString("path")
		if err != nil {
			logger.Fatalf("invalid path: %v\n", err)
		}
		host, err := cmd.Flags().GetString("host")
		if err != nil {
			logger.Fatalf("invalid host: %v\n", err)
		}
		var pathReg *regexp.Regexp
		if pathRegStr, err := cmd.Flags().GetString("path-regex"); err != nil {
			logger.Fatalf("invalid path-regex: %v\n", err)
		} else if len(pathRegStr) > 0 {
			if pathReg, err = regexp.Compile(pathRegStr); err != nil {
				logger.Fatalf("invalid path-regex: %v\n", err)
			}
		}
		pathPrefix, err := cmd.Flags().GetString("path-prefix")
		if err != nil {
			logger.Fatalf("invalid path-prefix: %v\n", err)
		}
		headerPairs, err := cmd.Flags().GetStringSlice("header")
		if err != nil {
			logger.Fatalf("invalid header: %v\n", err)
		}
		targetHeaders := parseHeaderFilter(headerPairs)

		options.MessageFilter = grpc.GrpcFilter{
			TargetPath:       path,
			TargetPathReg:    pathReg,
			TargetPathPrefix: pathPrefix,
			TargetHostName:   host,
			TargetMethods:    methods,
			TargetHeaders:    targetHeaders,
		}
		options.LatencyFilter = initLatencyFilter(cmd)
		options.SizeFilter = initSizeFilter(cmd)
		startAgent()
	},
}

func init() {
	autoReflectFunc = initAutoReflect
	reflectTargetFunc = initGrpcReflectionForTarget

	grpcCmd.Flags().StringSlice("method", []string{}, "Specify the HTTP method to monitor (e.g. POST for gRPC)")
	grpcCmd.Flags().String("host", "", "Specify the :authority to monitor, like: 'localhost:50051'")
	grpcCmd.Flags().String("path", "", "Specify the gRPC path (e.g. /package.Service/Method)")
	grpcCmd.Flags().String("path-regex", "", "Specify the regex for gRPC path")
	grpcCmd.Flags().String("path-prefix", "", "Specify the prefix of gRPC path to monitor")
	grpcCmd.Flags().StringSlice("header", []string{}, "Filter by request header (key:value). Can be repeated. Example: --header 'Authorization: Bearer x'")

	grpcCmd.Flags().SortFlags = false
	grpcCmd.PersistentFlags().SortFlags = false
	copy := *grpcCmd
	watchCmd.AddCommand(&copy)
	copy2 := *grpcCmd
	statCmd.AddCommand(&copy2)
}
