package storage

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"sort"
	"time"

	"fyreq/internal/catalog"
	"fyreq/internal/pathpolicy"
)

// The manifest is the catalog's single commit point: a small file naming the
// generation, coverage, and live segments, replaced atomically on publication.
// Layout: magic "FYRQMAN\x00", uint16 major version, tagged fields, and a
// trailing uint32 CRC-32C of everything before it.
const (
	manifestVersion = 1
	manifestName    = "manifest"
	legacyName      = "catalog.json"
)

var manifestMagic = []byte("FYRQMAN\x00")

// segmentRef is a manifest entry for one live segment.
type segmentRef struct {
	name       string
	id         [16]byte
	records    uint64
	tombstones uint64
	blocks     int
	bytes      int64
	minPath    string
	maxPath    string
}

type manifest struct {
	platform   string
	generation uint64
	updatedAt  time.Time
	entries    uint64
	coverage   map[string]catalog.Coverage
	// base segments hold disjoint, ascending path ranges. Deltas are ordered
	// oldest first and override base segments and older deltas.
	base   []segmentRef
	deltas []segmentRef
}

func emptyManifest() manifest {
	return manifest{platform: runtime.GOOS, coverage: make(map[string]catalog.Coverage)}
}

func (m manifest) header() catalog.Header {
	coverage := make(map[string]catalog.Coverage, len(m.coverage))
	for root, c := range m.coverage {
		coverage[root] = c
	}
	return catalog.Header{FormatVersion: manifestVersion, Platform: m.platform, Generation: m.generation, UpdatedAt: m.updatedAt, Entries: m.entries, Coverage: coverage}
}

func (m manifest) encode() []byte {
	var e encoder
	e.buf = append(e.buf, manifestMagic...)
	e.buf = binary.BigEndian.AppendUint16(e.buf, manifestVersion)
	e.stringField(1, m.platform)
	e.uintField(2, m.generation)
	e.timeField(3, m.updatedAt)
	e.uintField(4, m.entries)
	for _, c := range sortedCoverage(m.coverage) {
		e.field(5, func(e *encoder) {
			e.stringField(1, c.Root)
			e.timeField(2, c.StartedAt)
			e.timeField(3, c.CompletedAt)
			if c.Complete {
				e.uintField(4, 1)
			}
			e.uintField(5, uint64(c.ErrorCount))
			for _, message := range c.Errors {
				e.stringField(6, message)
			}
		})
	}
	encodeRefs(&e, 6, m.base)
	encodeRefs(&e, 7, m.deltas)
	return binary.BigEndian.AppendUint32(e.buf, checksum(e.buf))
}

func encodeRefs(e *encoder, tag uint64, refs []segmentRef) {
	for _, ref := range refs {
		e.field(tag, func(e *encoder) {
			e.stringField(1, ref.name)
			e.field(2, func(e *encoder) { e.buf = append(e.buf, ref.id[:]...) })
			e.uintField(3, ref.records)
			e.uintField(4, ref.tombstones)
			e.uintField(5, uint64(ref.blocks))
			e.uintField(6, uint64(ref.bytes))
			e.stringField(7, ref.minPath)
			e.stringField(8, ref.maxPath)
		})
	}
}

func sortedCoverage(coverage map[string]catalog.Coverage) []catalog.Coverage {
	roots := make([]string, 0, len(coverage))
	for root := range coverage {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	result := make([]catalog.Coverage, len(roots))
	for i, root := range roots {
		result[i] = coverage[root]
	}
	return result
}

// decodeManifest validates the checksum and version, then the platform before
// any paths, because path rules are platform-specific.
func decodeManifest(data []byte) (manifest, error) {
	if len(data) < len(manifestMagic)+2+4 || !bytes.Equal(data[:len(manifestMagic)], manifestMagic) {
		return manifest{}, fmt.Errorf("decode catalog manifest: %w", errCorrupt)
	}
	body, sum := data[:len(data)-4], binary.BigEndian.Uint32(data[len(data)-4:])
	if checksum(body) != sum {
		return manifest{}, fmt.Errorf("decode catalog manifest: checksum mismatch: %w", errCorrupt)
	}
	if version := binary.BigEndian.Uint16(body[len(manifestMagic):]); version != manifestVersion {
		return manifest{}, fmt.Errorf("unsupported catalog format version %d (expected %d)", version, manifestVersion)
	}
	m := manifest{coverage: make(map[string]catalog.Coverage)}
	var coverage []catalog.Coverage
	d := decoder{buf: body[len(manifestMagic)+2:]}
	err := d.fields(func(tag uint64, v *decoder) {
		switch tag {
		case 1:
			m.platform = string(v.rest())
		case 2:
			m.generation = v.uvarint()
		case 3:
			m.updatedAt = v.time()
		case 4:
			m.entries = v.uvarint()
		case 5:
			var c catalog.Coverage
			v.fields(func(tag uint64, v *decoder) {
				switch tag {
				case 1:
					c.Root = string(v.rest())
				case 2:
					c.StartedAt = v.time()
				case 3:
					c.CompletedAt = v.time()
				case 4:
					c.Complete = v.uvarint() == 1
				case 5:
					c.ErrorCount = v.int()
				case 6:
					c.Errors = append(c.Errors, string(v.rest()))
				}
			})
			coverage = append(coverage, c)
		case 6, 7:
			var ref segmentRef
			v.fields(func(tag uint64, v *decoder) {
				switch tag {
				case 1:
					ref.name = string(v.rest())
				case 2:
					if id := v.rest(); len(id) == len(ref.id) {
						copy(ref.id[:], id)
					} else {
						v.fail()
					}
				case 3:
					ref.records = v.uvarint()
				case 4:
					ref.tombstones = v.uvarint()
				case 5:
					ref.blocks = v.int()
				case 6:
					ref.bytes = int64(v.int())
				case 7:
					ref.minPath = string(v.rest())
				case 8:
					ref.maxPath = string(v.rest())
				}
			})
			if tag == 6 {
				m.base = append(m.base, ref)
			} else {
				m.deltas = append(m.deltas, ref)
			}
		}
	})
	if err != nil {
		return manifest{}, fmt.Errorf("decode catalog manifest: %w", err)
	}
	if m.platform != runtime.GOOS {
		return manifest{}, fmt.Errorf("catalog platform %q is incompatible with runtime %q", m.platform, runtime.GOOS)
	}
	for _, c := range coverage {
		if !pathpolicy.Valid(c.Root) {
			return manifest{}, fmt.Errorf("invalid coverage root %q", c.Root)
		}
		if _, duplicate := m.coverage[c.Root]; duplicate {
			return manifest{}, fmt.Errorf("duplicate coverage root %q", c.Root)
		}
		m.coverage[c.Root] = c
	}
	return m, m.validateSegments()
}

func (m manifest) validateSegments() error {
	names := make(map[string]bool)
	for i, ref := range append(append([]segmentRef(nil), m.base...), m.deltas...) {
		if !validSegmentName(ref.name) || names[ref.name] {
			return fmt.Errorf("invalid catalog segment name %q", ref.name)
		}
		names[ref.name] = true
		if i < len(m.base) && (ref.tombstones != 0 || (i > 0 && m.base[i-1].maxPath >= ref.minPath)) {
			return fmt.Errorf("base segment %s overlaps its predecessor or has tombstones: %w", ref.name, errCorrupt)
		}
	}
	return nil
}
