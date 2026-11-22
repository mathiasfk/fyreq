package storage

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyreq/internal/catalog"
)

// A segment is an immutable file of path-sorted rows split into blocks. Each
// block stores every column as a separate checksummed chunk, so readers fetch
// only the columns a query needs. The footer indexes chunks and holds per-chunk
// statistics used to skip blocks. Layout:
//
//	header:  magic "FYRQSEG\x00", uint16 major version, 16-byte segment ID
//	chunks:  column data
//	footer:  tagged fields (see segmentFooter tags)
//	trailer: uint32 footer length, uint32 footer CRC-32C, magic "FYRQEND\x00"
const (
	segmentVersion   = 1
	segmentExtension = ".fqs"
	headerSize       = 8 + 2 + 16
	trailerSize      = 4 + 4 + 8
)

var (
	segmentMagic = []byte("FYRQSEG\x00")
	trailerMagic = []byte("FYRQEND\x00")
	// errSegmentChanged reports a segment removed or replaced after its manifest
	// was read; readers retry with the current manifest.
	errSegmentChanged = errors.New("catalog segment changed while opening")
)

type columnType uint64

const (
	columnString columnType = 1 // prefix-compressed, nullable
	columnInt    columnType = 2 // signed varints
	columnTime   columnType = 3 // varint second deltas and nanoseconds
	columnBool   columnType = 4 // one byte per row
)

// Column IDs are permanent. New columns take new IDs; readers ignore unknown
// IDs and treat columns missing from older segments as null or zero.
type column struct {
	id   uint64
	name string
	typ  columnType
}

var (
	columnPath    = column{1, "path", columnString}
	columnDeleted = column{11, "deleted", columnBool}
	columns       = []column{columnPath, {2, "id", columnString}, {3, "name", columnString}, {4, "kind", columnString}, {5, "size", columnInt}, {6, "modified_at", columnTime}, {7, "indexed_at", columnTime}, {8, "identity", columnString}, {9, "owner_id", columnString}, {10, "owner", columnString}, columnDeleted}
	columnsByName = make(map[string]column)
	recordColumns []string
)

func init() {
	for _, c := range columns {
		columnsByName[c.name] = c
		if c != columnDeleted {
			recordColumns = append(recordColumns, c.name)
		}
	}
}

// row is one record or, in delta segments, a tombstone for its path.
type row struct {
	file    catalog.File
	deleted bool
}

func stringValue(file *catalog.File, name string) *string {
	switch name {
	case "path":
		return &file.Path
	case "id":
		return &file.ID
	case "name":
		return &file.Name
	case "kind":
		return &file.Kind
	case "identity":
		return &file.Identity
	case "owner_id":
		return file.OwnerID
	case "owner":
		return file.Owner
	}
	panic("unknown string column " + name)
}

func setString(file *catalog.File, name string, v *string) {
	plain := ""
	if v != nil {
		plain = *v
	}
	switch name {
	case "path":
		file.Path = plain
	case "id":
		file.ID = plain
	case "name":
		file.Name = plain
	case "kind":
		file.Kind = plain
	case "identity":
		file.Identity = plain
	case "owner_id":
		file.OwnerID = v
	case "owner":
		file.Owner = v
	}
}

func timeValue(file *catalog.File, name string) *time.Time {
	if name == "modified_at" {
		return &file.ModifiedAt
	}
	return &file.IndexedAt
}

// columnStats summarizes one chunk. Min and Max are nil without non-null values.
type columnStats struct {
	nulls int
	min   any
	max   any
}

func (s *columnStats) observe(v any) {
	if s.min == nil || less(v, s.min) {
		s.min = v
	}
	if s.max == nil || less(s.max, v) {
		s.max = v
	}
}

func (s *columnStats) merge(o columnStats) {
	s.nulls += o.nulls
	if o.min != nil {
		s.observe(o.min)
		s.observe(o.max)
	}
}

func less(a, b any) bool {
	switch a := a.(type) {
	case string:
		return a < b.(string)
	case int64:
		return a < b.(int64)
	case time.Time:
		return a.Before(b.(time.Time))
	}
	return false
}

func encodeColumn(c column, rows []row) ([]byte, columnStats) {
	var e encoder
	var s columnStats
	switch c.typ {
	case columnString:
		previous := ""
		for i := range rows {
			v := stringValue(&rows[i].file, c.name)
			if v == nil || (rows[i].deleted && c != columnPath) {
				s.nulls++
				e.uvarint(0)
				continue
			}
			shared := 0
			for shared < len(previous) && shared < len(*v) && previous[shared] == (*v)[shared] {
				shared++
			}
			e.uvarint(uint64(shared) + 1)
			e.bytes([]byte((*v)[shared:]))
			previous = *v
			s.observe(*v)
		}
	case columnInt:
		for i := range rows {
			e.varint(rows[i].file.Size)
			if !rows[i].deleted {
				s.observe(rows[i].file.Size)
			}
		}
	case columnTime:
		var previous int64
		for i := range rows {
			t := timeValue(&rows[i].file, c.name)
			e.varint(t.Unix() - previous)
			e.uvarint(uint64(t.Nanosecond()))
			previous = t.Unix()
			if !rows[i].deleted {
				s.observe(t.UTC())
			}
		}
	case columnBool:
		for i := range rows {
			if rows[i].deleted {
				e.buf = append(e.buf, 1)
			} else {
				e.buf = append(e.buf, 0)
			}
		}
	}
	return e.buf, s
}

// values holds one decoded chunk.
type values struct {
	strings []*string
	ints    []int64
	times   []time.Time
	bools   []bool
}

func decodeColumn(c column, data []byte, n int) (values, error) {
	d := decoder{buf: data}
	var v values
	switch c.typ {
	case columnString:
		v.strings = make([]*string, n)
		var previous []byte
		for i := 0; i < n; i++ {
			state := d.uvarint()
			if state == 0 {
				continue
			}
			shared := state - 1
			suffix := d.bytes()
			if shared > uint64(len(previous)) {
				d.fail()
				break
			}
			current := make([]byte, 0, int(shared)+len(suffix))
			current = append(append(current, previous[:shared]...), suffix...)
			s := string(current)
			v.strings[i] = &s
			previous = current
		}
	case columnInt:
		v.ints = make([]int64, n)
		for i := range v.ints {
			v.ints[i] = d.varint()
		}
	case columnTime:
		v.times = make([]time.Time, n)
		var seconds int64
		for i := range v.times {
			seconds += d.varint()
			nanos := d.uvarint()
			if nanos >= uint64(time.Second) {
				d.fail()
			}
			v.times[i] = time.Unix(seconds, int64(nanos)).UTC()
		}
	case columnBool:
		raw := d.take(uint64(n))
		v.bools = make([]bool, n)
		for i, b := range raw {
			v.bools[i] = b != 0
		}
	}
	if d.err == nil && len(d.buf) != 0 {
		d.fail()
	}
	return v, d.err
}

func (v values) apply(c column, file *catalog.File, i int) {
	switch c.typ {
	case columnString:
		setString(file, c.name, v.strings[i])
	case columnInt:
		file.Size = v.ints[i]
	case columnTime:
		*timeValue(file, c.name) = v.times[i]
	}
}

// chunk locates one column of one block.
type chunk struct {
	typ    columnType
	offset int64
	length int64
	crc    uint32
	stats  columnStats
}

type block struct {
	rows   int
	chunks map[uint64]chunk
}

type segment struct {
	file       *os.File
	ref        segmentRef
	rows       int
	tombstones int
	blocks     []block
	stats      map[uint64]columnStats
	present    map[uint64]bool
}

func encodeValue(e *encoder, v any) {
	switch v := v.(type) {
	case string:
		e.buf = append(e.buf, v...)
	case int64:
		e.varint(v)
	case time.Time:
		e.time(v)
	}
}

func decodeValue(d *decoder, typ columnType) any {
	switch typ {
	case columnString:
		return string(d.rest())
	case columnInt:
		return d.varint()
	case columnTime:
		return d.time()
	}
	return nil
}

// writeSegment writes rows, already sorted by path, to a new durable segment
// file. The file is only reachable once a manifest references it.
func writeSegment(dir string, rows []row, blockRows int) (segmentRef, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return segmentRef{}, err
	}
	ref := segmentRef{name: hex.EncodeToString(id[:]) + segmentExtension, id: id}
	f, err := os.OpenFile(filepath.Join(dir, ref.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return segmentRef{}, err
	}
	keep := false
	defer func() {
		f.Close()
		if !keep {
			os.Remove(filepath.Join(dir, ref.name))
		}
	}()
	var out bytes.Buffer
	out.Write(segmentMagic)
	out.Write(binary.BigEndian.AppendUint16(nil, segmentVersion))
	out.Write(id[:])
	var footer encoder
	footer.field(1, func(e *encoder) { e.buf = append(e.buf, id[:]...) })
	for start := 0; start < len(rows); start += blockRows {
		part := rows[start:min(start+blockRows, len(rows))]
		footer.field(5, func(e *encoder) {
			e.uintField(1, uint64(len(part)))
			for _, c := range columns {
				data, s := encodeColumn(c, part)
				offset := out.Len()
				out.Write(data)
				e.field(2, func(e *encoder) {
					e.uintField(1, c.id)
					e.uintField(2, uint64(c.typ))
					e.uintField(3, uint64(offset))
					e.uintField(4, uint64(len(data)))
					e.uintField(5, uint64(checksum(data)))
					e.uintField(6, uint64(s.nulls))
					if s.min != nil {
						e.field(7, func(e *encoder) { encodeValue(e, s.min) })
						e.field(8, func(e *encoder) { encodeValue(e, s.max) })
					}
				})
			}
		})
		ref.blocks++
	}
	for _, r := range rows {
		if r.deleted {
			ref.tombstones++
		} else {
			ref.records++
		}
	}
	footer.uintField(3, ref.records)
	footer.uintField(4, ref.tombstones)
	out.Write(footer.buf)
	out.Write(binary.BigEndian.AppendUint32(nil, uint32(len(footer.buf))))
	out.Write(binary.BigEndian.AppendUint32(nil, checksum(footer.buf)))
	out.Write(trailerMagic)
	if len(rows) > 0 {
		ref.minPath, ref.maxPath = rows[0].file.Path, rows[len(rows)-1].file.Path
	}
	ref.bytes = int64(out.Len())
	if _, err := f.Write(out.Bytes()); err != nil {
		return segmentRef{}, err
	}
	if err := f.Sync(); err != nil {
		return segmentRef{}, err
	}
	if err := f.Close(); err != nil {
		return segmentRef{}, err
	}
	keep = true
	return ref, nil
}

// openSegment opens and validates a manifest-referenced segment. A missing file
// or different segment ID means a writer replaced it after the manifest was read.
func openSegment(dir string, ref segmentRef) (*segment, error) {
	f, err := openCatalogFile(filepath.Join(dir, ref.name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errSegmentChanged
	}
	if err != nil {
		return nil, err
	}
	s, err := readSegment(f, ref)
	if err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func readSegment(f *os.File, ref segmentRef) (*segment, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	header := make([]byte, headerSize)
	if size < headerSize+trailerSize {
		return nil, fmt.Errorf("segment %s: %w", ref.name, errCorrupt)
	}
	if _, err := f.ReadAt(header, 0); err != nil {
		return nil, err
	}
	if !bytes.Equal(header[:8], segmentMagic) {
		return nil, fmt.Errorf("segment %s: %w", ref.name, errCorrupt)
	}
	if version := binary.BigEndian.Uint16(header[8:10]); version != segmentVersion {
		return nil, fmt.Errorf("segment %s has unsupported format version %d (expected %d)", ref.name, version, segmentVersion)
	}
	if !bytes.Equal(header[10:26], ref.id[:]) {
		return nil, errSegmentChanged
	}
	trailer := make([]byte, trailerSize)
	if _, err := f.ReadAt(trailer, size-trailerSize); err != nil {
		return nil, err
	}
	length := int64(binary.BigEndian.Uint32(trailer[0:4]))
	if !bytes.Equal(trailer[8:], trailerMagic) || length > size-headerSize-trailerSize {
		return nil, fmt.Errorf("segment %s: %w", ref.name, errCorrupt)
	}
	footer := make([]byte, length)
	if _, err := f.ReadAt(footer, size-trailerSize-length); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if checksum(footer) != binary.BigEndian.Uint32(trailer[4:8]) {
		return nil, fmt.Errorf("segment %s: footer checksum mismatch: %w", ref.name, errCorrupt)
	}
	dataEnd := size - trailerSize - length
	s := &segment{file: f, ref: ref, stats: make(map[uint64]columnStats), present: make(map[uint64]bool)}
	d := decoder{buf: footer}
	var id []byte
	err = d.fields(func(tag uint64, v *decoder) {
		switch tag {
		case 1:
			id = v.rest()
		case 3:
			s.rows = v.int()
		case 4:
			s.tombstones = v.int()
		case 5:
			b := block{chunks: make(map[uint64]chunk)}
			v.fields(func(tag uint64, v *decoder) {
				switch tag {
				case 1:
					b.rows = v.int()
				case 2:
					var id uint64
					var c chunk
					var low, high *decoder
					v.fields(func(tag uint64, v *decoder) {
						switch tag {
						case 1:
							id = v.uvarint()
						case 2:
							c.typ = columnType(v.uvarint())
						case 3:
							c.offset = int64(v.int())
						case 4:
							c.length = int64(v.int())
						case 5:
							c.crc = uint32(v.uvarint())
						case 6:
							c.stats.nulls = v.int()
						case 7:
							low = v
						case 8:
							high = v
						}
					})
					if c.offset < headerSize || c.offset+c.length > dataEnd {
						v.fail()
						return
					}
					if known, ok := columnByID(id); ok {
						if known.typ != c.typ {
							v.fail()
							return
						}
						if low != nil && high != nil {
							c.stats.min, c.stats.max = decodeValue(low, c.typ), decodeValue(high, c.typ)
							if low.err != nil || high.err != nil {
								v.fail()
								return
							}
						}
						b.chunks[id] = c
					}
				}
			})
			s.blocks = append(s.blocks, b)
		}
	})
	if err != nil || !bytes.Equal(id, ref.id[:]) {
		return nil, fmt.Errorf("segment %s: %w", ref.name, errCorrupt)
	}
	total := 0
	for i, b := range s.blocks {
		total += b.rows
		for id, c := range b.chunks {
			if i == 0 {
				s.present[id] = true
			}
			aggregate := s.stats[id]
			aggregate.merge(c.stats)
			s.stats[id] = aggregate
		}
		for id := range s.present {
			if _, ok := b.chunks[id]; !ok {
				s.present[id] = false
			}
		}
	}
	if total != s.rows+s.tombstones || len(s.blocks) != ref.blocks || uint64(s.rows) != ref.records || uint64(s.tombstones) != ref.tombstones {
		return nil, fmt.Errorf("segment %s: row counts disagree with manifest: %w", ref.name, errCorrupt)
	}
	return s, nil
}

func columnByID(id uint64) (column, bool) {
	for _, c := range columns {
		if c.id == id {
			return c, true
		}
	}
	return column{}, false
}

// read decodes one column of one block. Columns absent from an older segment
// decode as null or zero values.
func (s *segment) read(b block, c column) (values, int64, error) {
	ch, ok := b.chunks[c.id]
	if !ok {
		var v values
		switch c.typ {
		case columnString:
			v.strings = make([]*string, b.rows)
		case columnInt:
			v.ints = make([]int64, b.rows)
		case columnTime:
			v.times = make([]time.Time, b.rows)
		case columnBool:
			v.bools = make([]bool, b.rows)
		}
		return v, 0, nil
	}
	data := make([]byte, ch.length)
	if _, err := s.file.ReadAt(data, ch.offset); err != nil {
		return values{}, 0, err
	}
	if checksum(data) != ch.crc {
		return values{}, ch.length, fmt.Errorf("segment %s: chunk checksum mismatch: %w", s.ref.name, errCorrupt)
	}
	v, err := decodeColumn(c, data, b.rows)
	if err != nil {
		return values{}, ch.length, fmt.Errorf("segment %s: %w", s.ref.name, err)
	}
	return v, ch.length, nil
}

func validSegmentName(name string) bool {
	if !strings.HasSuffix(name, segmentExtension) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(name, segmentExtension))
	return err == nil && len(name) == 32+len(segmentExtension)
}
