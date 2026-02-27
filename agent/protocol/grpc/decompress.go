package grpc

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"kyanos/common"
	"strings"
)

func commonDecompress(body []byte, contentEncoding string) ([]byte, bool) {
	if len(body) == 0 {
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "gzip", "x-gzip":
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			common.ProtocolParserLog.Debugf("[gRPC] failed to create gzip reader: %v", err)
			return nil, false
		}
		defer gr.Close()
		decompressed, err := io.ReadAll(gr)
		if err != nil {
			common.ProtocolParserLog.Debugf("[gRPC] failed to decompress gzip body: %v", err)
			return nil, false
		}
		return decompressed, true
	case "deflate":
		fr := flate.NewReader(bytes.NewReader(body))
		defer fr.Close()
		decompressed, err := io.ReadAll(fr)
		if err != nil {
			common.ProtocolParserLog.Debugf("[gRPC] failed to decompress deflate body: %v", err)
			return nil, false
		}
		return decompressed, true
	default:
		return nil, false
	}
}
