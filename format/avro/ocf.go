package avro

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"

	"github.com/golang/snappy"
	"github.com/wader/fq/format"
	"github.com/wader/fq/format/avro/decoders"
	"github.com/wader/fq/format/avro/schema"
	"github.com/wader/fq/format/registry"
	"github.com/wader/fq/pkg/bitio"
	"github.com/wader/fq/pkg/decode"
	"github.com/wader/fq/pkg/scalar"
)

func init() {
	registry.MustRegister(decode.Format{
		Name:        format.AVRO_OCF,
		Description: "Avro object container file",
		Groups:      []string{format.PROBE},
		DecodeFn:    decodeAvroOCF,
	})
}

type HeaderData struct {
	Schema schema.SimplifiedSchema
	Codec  string
	Sync   []byte
}

const headerSchemaSpec = `
{
  "type": "record",
  "name": "org.apache.avro.file.Header",
  "fields": [
   {"name": "meta", "type": {"type": "map", "values": "string"}},
   {"name": "sync", "type": {"type": "fixed", "name": "Sync", "size": 16}}
  ]
}`

func decodeHeader(d *decode.D) HeaderData {
	d.FieldRawLen("magic", 4*8, d.AssertBitBuf([]byte{'O', 'b', 'j', 1}))

	var headerData HeaderData

	headerSchema, err := schema.FromSchemaString(headerSchemaSpec)
	if err != nil {
		d.Fatalf("Failed to parse header schema: %v", err)
	}
	decodeHeaderFn, err := decoders.DecodeFnForSchema(headerSchema)
	if err != nil {
		d.Fatalf("failed to parse header: %v", err)
	}

	header := decodeHeaderFn("header", d)
	headerRecord, ok := header.(map[string]interface{})
	if !ok {
		d.Fatalf("header is not a map")
	}
	meta, ok := headerRecord["meta"].(map[string]interface{})
	if !ok {
		d.Fatalf("header.meta is not a map")
	}

	headerData.Schema, err = schema.FromSchemaString(meta["avro.schema"].(string))
	if err != nil {
		d.Fatalf("failed to parse schema: %v", err)
	}
	if codec, ok := meta["avro.codec"]; ok {
		headerData.Codec, ok = codec.(string)
		if !ok {
			d.Fatalf("avro.codec is not a string")
		}
	} else {
		headerData.Codec = "null"
	}

	headerData.Sync, ok = headerRecord["sync"].([]byte)
	if !ok {
		d.Fatalf("header.sync is not a byte array")
	}
	return headerData
}

func decodeAvroOCF(d *decode.D, in interface{}) interface{} {
	header := decodeHeader(d)

	decodeFn, err := decoders.DecodeFnForSchema(header.Schema)
	if err != nil {
		d.Fatalf("unable to create codec: %v", err)
	}

	d.FieldStructArrayLoop("blocks", "block", func() bool { return d.NotEnd() }, func(d *decode.D) {
		count := d.FieldSFn("count", decoders.VarZigZag)
		if count <= 0 {
			return
		}
		size := d.FieldSFn("size", decoders.VarZigZag)
		i := int64(0)

		if header.Codec == "deflate" {
			br := d.FieldRawLen("compressed", size*8)
			bb := &bytes.Buffer{}
			d.MustCopy(bb, flate.NewReader(bitio.NewIOReader(br)))
			d.FieldArrayRootBitBufFn("data", bitio.NewBitReader(bb.Bytes(), -1), func(d *decode.D) {
				for ; i < count; i++ {
					decodeFn("data", d)
				}
			})
		} else if header.Codec == "snappy" {
			// Everything but last 4 bytes which are the checksum
			n := (size - 4) * 8
			br := d.FieldRawLen("compressed", n)
			data := make([]byte, size-4)
			if _, err := br.ReadBits(data, n); err != nil {
				d.Fatalf("failed to read snappy compressed data: %v", err)
			}
			decoded, err := snappy.Decode(nil, data)
			if err != nil {
				d.Fatalf("failed to decode snappy compressed data: %v", err)
			}

			crc := crc32.ChecksumIEEE(decoded)
			crcB := make([]byte, 4)
			if d.Endian == decode.BigEndian {
				binary.BigEndian.PutUint32(crcB, crc)
			} else {
				binary.LittleEndian.PutUint32(crcB, crc)
			}
			d.FieldRawLen("crc32", 4*8, d.AssertBitBuf(crcB))
			d.FieldArrayRootBitBufFn("data", bitio.NewBitReader(decoded, -1), func(d *decode.D) {
				for ; i < count; i++ {
					decodeFn("data", d)
				}
			})
		} else if header.Codec == "null" {
			d.FieldArrayLoop("data", func() bool { return i < count }, func(d *decode.D) {
				decodeFn("datum", d)
				i++
			})
		} else {
			// Unknown codec, just dump the compressed data.
			d.FieldRawLen("data", size*8, scalar.Description(header.Codec+" encoded"))
		}
		d.FieldRawLen("sync", 16*8, d.AssertBitBuf(header.Sync))
	})

	return nil
}
