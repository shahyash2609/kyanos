package cmd

import (
	"kyanos/agent/protocol"
	"net/http"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var httpCmd = &cobra.Command{
	Use:   "http [--method METHODS|--path PATH|--path-regex REGEX|--path-prefix PREFIX|--host HOSTNAME|--header HEADER:VALUE]",
	Short: "watch HTTP message",
	Long:  `Filter HTTP messages based on method, path (strict, regex, prefix), host, or request headers. Filter flags are combined with AND(&&).`,
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
		var (
			pathReg *regexp.Regexp
		)
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

		headerRegexPairs, err := cmd.Flags().GetStringSlice("header-regex")
		if err != nil {
			logger.Fatalf("invalid header-regex: %v\n", err)
		}
		targetHeaderRegs := parseHeaderRegexFilter(headerRegexPairs)

		options.MessageFilter = protocol.HttpFilter{
			TargetPath:       path,
			TargetPathReg:    pathReg,
			TargetPathPrefix: pathPrefix,
			TargetHostName:   host,
			TargetMethods:    methods,
			TargetHeaders:    targetHeaders,
			TargetHeaderRegs: targetHeaderRegs,
		}
		options.LatencyFilter = initLatencyFilter(cmd)
		options.SizeFilter = initSizeFilter(cmd)
		startAgent()
	},
}

func init() {
	httpCmd.Flags().StringSlice("method", []string{}, "Specify the HTTP method to monitor(GET, POST), seperate by ','")
	httpCmd.Flags().String("host", "", "Specify the HTTP host to monitor, like: 'ubuntu.com'")
	httpCmd.Flags().String("path", "", "Specify the HTTP path to monitor, like: '/foo/bar'")
	httpCmd.Flags().String("path-regex", "", "Specify the regex for HTTP path to monitor, like: '\\/foo\\/bar\\/\\d+'")
	httpCmd.Flags().String("path-prefix", "", "Specify the prefix of HTTP path to monitor, like: '/foo'")
	httpCmd.Flags().StringSlice("header", []string{}, "Filter by request header (key:value). Can be repeated. Example: --header 'Authorization: Bearer x' --header 'X-Request-Id: abc'")
	httpCmd.Flags().StringSlice("header-regex", []string{}, "Filter by request header with regex (key:regex). Can be repeated. Example: --header-regex 'Traceparent:.*-01$'")

	httpCmd.Flags().SortFlags = false
	httpCmd.PersistentFlags().SortFlags = false
	copy := *httpCmd
	watchCmd.AddCommand(&copy)
	copy2 := *httpCmd
	statCmd.AddCommand(&copy2)
}

// parseHeaderRegexFilter parses "Name: regex" strings and returns compiled regexps keyed by canonical header name.
func parseHeaderRegexFilter(pairs []string) map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp)
	for _, s := range pairs {
		s = strings.TrimSpace(s)
		i := strings.Index(s, ":")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(s[:i])
		pattern := strings.TrimSpace(s[i+1:])
		if key == "" || pattern == "" {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			logger.Fatalf("invalid header-regex pattern for %s: %v\n", key, err)
		}
		out[http.CanonicalHeaderKey(key)] = re
	}
	return out
}

// parseHeaderFilter parses "Name: Value" strings and returns a map with canonical header keys.
// Malformed entries (no colon) are skipped.
func parseHeaderFilter(pairs []string) map[string]string {
	out := make(map[string]string)
	for _, s := range pairs {
		s = strings.TrimSpace(s)
		i := strings.Index(s, ":")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(s[:i])
		val := strings.TrimSpace(s[i+1:])
		if key == "" {
			continue
		}
		out[http.CanonicalHeaderKey(key)] = val
	}
	return out
}
