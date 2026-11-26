// Codec for the WMPF private debug bridge wire protocol.
// It implements the protobuf wire format with the Go standard library; the
// field-number layout is an interface/protocol fact, not copied source code.
package bridge

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const MaxFrame = 64 << 20

type Frame struct {
	Sequence  uint64
	Category  string
	Payload   string
	JsContext string
	Data      []byte
	Fields    []string
	Outer     []string
}
type field struct {
	number int
	wire   int
	value  uint64
	data   []byte
}

func fields(raw []byte) ([]field, error) {
	result := []field{}
	for len(raw) > 0 {
		tag, n := binary.Uvarint(raw)
		if n <= 0 || tag>>3 == 0 {
			return nil, errors.New("invalid protobuf tag")
		}
		raw = raw[n:]
		f := field{number: int(tag >> 3), wire: int(tag & 7)}
		switch tag & 7 {
		case 0:
			v, n := binary.Uvarint(raw)
			if n <= 0 {
				return nil, io.ErrUnexpectedEOF
			}
			raw = raw[n:]
			f.value = v
		case 1:
			if len(raw) < 8 {
				return nil, io.ErrUnexpectedEOF
			}
			raw = raw[8:]
		case 2:
			length, n := binary.Uvarint(raw)
			if n <= 0 || length > MaxFrame || length > uint64(len(raw)-n) {
				return nil, io.ErrUnexpectedEOF
			}
			raw = raw[n:]
			f.data = raw[:int(length)]
			raw = raw[int(length):]
		case 5:
			if len(raw) < 4 {
				return nil, io.ErrUnexpectedEOF
			}
			raw = raw[4:]
		default:
			return nil, errors.New("unsupported protobuf wire type")
		}
		result = append(result, f)
	}
	return result, nil
}
func varint(dst []byte, field int, value uint64) []byte {
	dst = binary.AppendUvarint(dst, uint64(field<<3))
	return binary.AppendUvarint(dst, value)
}
func blob(dst []byte, field int, value []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(field<<3|2))
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}
func wrap(seq uint64, category string, inner []byte) ([]byte, error) {
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(inner); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	out := varint(nil, 1, seq)
	out = blob(out, 3, []byte(category))
	out = blob(out, 4, compressed.Bytes())
	out = varint(out, 5, 1)
	out = varint(out, 6, uint64(len(inner)))
	return out, nil
}
func Encode(seq uint64, category, payload string) ([]byte, error) {
	inner := varint(nil, 1, seq)
	inner = blob(inner, 2, []byte(payload))
	inner = blob(inner, 3, []byte{})
	return wrap(seq, category, inner)
}

// EncodeConnectJsContext asks the runtime to attach a JavaScript context.
// WARemoteDebug_ConnectJsContext has only field 1 (jscontextId), unlike the
// chromeDevtools wrapper (opId/payload/jscontextId).
func EncodeConnectJsContext(seq uint64, jscontextID string) ([]byte, error) {
	return wrap(seq, "connectJsContext", blob(nil, 1, []byte(jscontextID)))
}
func Decode(raw []byte) (Frame, error) {
	f := Frame{}
	if len(raw) > MaxFrame {
		return f, errors.New("frame too large")
	}
	values, err := fields(raw)
	if err != nil {
		return f, err
	}
	var compression, original uint64
	for _, v := range values {
		f.Outer = append(f.Outer, fmt.Sprintf("%d:%d:%d", v.number, v.wire, len(v.data)))
		switch v.number {
		case 1:
			f.Sequence = v.value
		case 3:
			f.Category = string(v.data)
		case 4:
			f.Data = v.data
		case 5:
			compression = v.value
		case 6:
			original = v.value
		}
	}
	if compression&1 != 0 {
		zr, err := zlib.NewReader(bytes.NewReader(f.Data))
		if err != nil {
			return f, err
		}
		decoded, err := io.ReadAll(io.LimitReader(zr, MaxFrame+1))
		zr.Close()
		if err != nil {
			return f, err
		}
		if len(decoded) > MaxFrame || original > MaxFrame {
			return f, errors.New("inflated frame too large")
		}
		if original != 0 && original != uint64(len(decoded)) {
			return f, errors.New("inflated size mismatch")
		}
		f.Data = decoded
	}
	switch f.Category {
	case "chromeDevtools", "chromeDevtoolsResult":
		inner, err := fields(f.Data)
		if err != nil {
			return f, err
		}
		for _, v := range inner {
			f.Fields = append(f.Fields, fmt.Sprintf("%d:%d:%d", v.number, v.wire, len(v.data)))
			if v.number == 2 {
				f.Payload = string(v.data)
			}
			if v.number == 3 {
				f.JsContext = string(v.data)
			}
		}
	case "connectJsContext", "addJsContext", "removeJsContext":
		inner, err := fields(f.Data)
		if err != nil {
			return f, err
		}
		for _, v := range inner {
			f.Fields = append(f.Fields, fmt.Sprintf("%d:%d:%d", v.number, v.wire, len(v.data)))
			if v.number == 1 {
				f.JsContext = string(v.data)
			}
		}
	}
	return f, nil
}
