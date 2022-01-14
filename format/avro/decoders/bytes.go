package decoders

import (
	"github.com/wader/fq/format/avro/schema"
	"github.com/wader/fq/pkg/decode"
	"github.com/wader/fq/pkg/scalar"
)

type BytesCodec struct{}

func decodeBytesFn(schema schema.SimplifiedSchema, sms ...scalar.Mapper) (DecodeFn, error) {
	// Bytes are encoded as a long followed by that many bytes of data.
	return func(name string, d *decode.D) interface{} {
		var val []byte
		var err error

		d.FieldStruct(name, func(d *decode.D) {
			length := d.FieldSFn("length", VarZigZag)
			bb := d.FieldRawLen("data", length*8, sms...)

			val, err = bb.Bytes()
			if err != nil {
				d.Fatalf("failed to read %s bytes: %v", name, err)
			}
		})

		return val
	}, nil
}
