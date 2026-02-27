package grpc

import (
	"golang.org/x/net/http2/hpack"
)

type hpackDecoder struct {
	dec *hpack.Decoder
}

func newHpackDecoder() *hpackDecoder {
	d := &hpackDecoder{}
	d.dec = hpack.NewDecoder(4096, func(f hpack.HeaderField) {
		// emit callback - we use DecodeFull instead
	})
	return d
}

// Decode decodes the header block fragment and returns decoded headers.
// DecodeFull is used so we get all headers from this block (decoder state is updated).
func (d *hpackDecoder) Decode(p []byte) ([]HeaderField, error) {
	var out []HeaderField
	d.dec.SetEmitFunc(func(f hpack.HeaderField) {
		out = append(out, HeaderField{Name: f.Name, Value: f.Value})
	})
	defer d.dec.SetEmitFunc(func(hpack.HeaderField) {})
	_, err := d.dec.Write(p)
	if err != nil {
		return nil, err
	}
	return out, nil
}
