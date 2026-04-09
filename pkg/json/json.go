package json

import (
	stdjson "encoding/json"
	"io"

	"github.com/bytedance/sonic"
)

type RawMessage = stdjson.RawMessage
type Number = stdjson.Number
type Decoder = sonic.Decoder
type Encoder = sonic.Encoder

var std = sonic.ConfigStd

func Marshal(v interface{}) ([]byte, error) {
	return std.Marshal(v)
}

func MarshalIndent(v interface{}, prefix, indent string) ([]byte, error) {
	return std.MarshalIndent(v, prefix, indent)
}

func Unmarshal(data []byte, v interface{}) error {
	return std.Unmarshal(data, v)
}

func NewEncoder(w io.Writer) Encoder {
	return std.NewEncoder(w)
}

func NewDecoder(r io.Reader) Decoder {
	return std.NewDecoder(r)
}
