package storage

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"time"
)

// Persistent structures are sequences of tagged fields: a uvarint tag, a uvarint
// length, and the payload. Readers skip tags they do not know, so later format
// revisions can add fields without breaking older readers; incompatible changes
// increase the file's major version instead.

var errCorrupt = errors.New("corrupt catalog data")

var checksumTable = crc32.MakeTable(crc32.Castagnoli)

func checksum(b []byte) uint32 { return crc32.Checksum(b, checksumTable) }

type encoder struct{ buf []byte }

func (e *encoder) uvarint(v uint64) { e.buf = binary.AppendUvarint(e.buf, v) }
func (e *encoder) varint(v int64)   { e.buf = binary.AppendVarint(e.buf, v) }

func (e *encoder) bytes(b []byte) {
	e.uvarint(uint64(len(b)))
	e.buf = append(e.buf, b...)
}

func (e *encoder) time(t time.Time) {
	e.varint(t.Unix())
	e.uvarint(uint64(t.Nanosecond()))
}

func (e *encoder) field(tag uint64, write func(*encoder)) {
	var inner encoder
	write(&inner)
	e.uvarint(tag)
	e.bytes(inner.buf)
}

func (e *encoder) uintField(tag, v uint64) { e.field(tag, func(e *encoder) { e.uvarint(v) }) }
func (e *encoder) stringField(tag uint64, s string) {
	e.field(tag, func(e *encoder) { e.buf = append(e.buf, s...) })
}
func (e *encoder) timeField(tag uint64, t time.Time) { e.field(tag, func(e *encoder) { e.time(t) }) }

// decoder records the first error; later reads return zero values.
type decoder struct {
	buf []byte
	err error
}

func (d *decoder) fail() {
	if d.err == nil {
		d.err = errCorrupt
	}
	d.buf = nil
}

func (d *decoder) uvarint() uint64 {
	v, n := binary.Uvarint(d.buf)
	if n <= 0 {
		d.fail()
		return 0
	}
	d.buf = d.buf[n:]
	return v
}

func (d *decoder) varint() int64 {
	v, n := binary.Varint(d.buf)
	if n <= 0 {
		d.fail()
		return 0
	}
	d.buf = d.buf[n:]
	return v
}

func (d *decoder) take(n uint64) []byte {
	if n > uint64(len(d.buf)) {
		d.fail()
		return nil
	}
	b := d.buf[:n]
	d.buf = d.buf[n:]
	return b
}

func (d *decoder) bytes() []byte { return d.take(d.uvarint()) }

func (d *decoder) time() time.Time {
	seconds := d.varint()
	nanos := d.uvarint()
	if nanos >= uint64(time.Second) {
		d.fail()
		return time.Time{}
	}
	return time.Unix(seconds, int64(nanos)).UTC()
}

// int narrows a count, rejecting values that cannot describe in-memory data.
func (d *decoder) int() int {
	v := d.uvarint()
	if v > 1<<40 {
		d.fail()
		return 0
	}
	return int(v)
}

func (d *decoder) rest() []byte {
	b := d.buf
	d.buf = nil
	return b
}

// fields visits every tagged field. Unknown tags are ignored by the callback.
func (d *decoder) fields(visit func(tag uint64, value *decoder)) error {
	for len(d.buf) > 0 && d.err == nil {
		tag := d.uvarint()
		payload := d.bytes()
		if d.err != nil {
			break
		}
		value := &decoder{buf: payload}
		visit(tag, value)
		if value.err != nil {
			d.err = value.err
		}
	}
	return d.err
}
