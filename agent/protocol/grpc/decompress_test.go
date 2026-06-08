package grpc

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCommonDecompress(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		_, ok := commonDecompress(nil, "gzip")
		assert.False(t, ok)
		_, ok = commonDecompress([]byte{}, "gzip")
		assert.False(t, ok)
	})

	t.Run("gzip", func(t *testing.T) {
		plain := []byte("hello gzip")
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write(plain)
		w.Close()
		compressed := buf.Bytes()

		out, ok := commonDecompress(compressed, "gzip")
		assert.True(t, ok)
		assert.Equal(t, plain, out)
	})

	t.Run("gzip_case_insensitive", func(t *testing.T) {
		plain := []byte("data")
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write(plain)
		w.Close()
		out, ok := commonDecompress(buf.Bytes(), "GZIP")
		assert.True(t, ok)
		assert.Equal(t, plain, out)
	})

	t.Run("unknown_encoding", func(t *testing.T) {
		_, ok := commonDecompress([]byte("raw"), "identity")
		assert.False(t, ok)
	})
}
