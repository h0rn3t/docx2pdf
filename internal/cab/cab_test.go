package cab

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// cabinet builds a single-folder cabinet holding files (in order), compressed
// with comp, whose data blocks are blocks (each with its uncompressed size).
func cabinet(comp uint16, files []string, sizes []int, blocks [][]byte, usizes []int) []byte {
	var names bytes.Buffer
	off := 0
	for i, name := range files {
		var e [16]byte
		binary.LittleEndian.PutUint32(e[0:], uint32(sizes[i])) //nolint:gosec // G115: fixture sizes are small
		binary.LittleEndian.PutUint32(e[4:], uint32(off))
		names.Write(e[:])
		names.WriteString(name)
		names.WriteByte(0)
		off += sizes[i]
	}
	const header, folder = 36, 8
	filesAt := header + folder
	dataAt := filesAt + names.Len()
	var data bytes.Buffer
	for i, b := range blocks {
		var e [8]byte
		binary.LittleEndian.PutUint16(e[4:], uint16(len(b)))    //nolint:gosec // G115: fixture sizes are small
		binary.LittleEndian.PutUint16(e[6:], uint16(usizes[i])) //nolint:gosec // G115: fixture sizes are small
		data.Write(e[:])
		data.Write(b)
	}
	out := make([]byte, dataAt, dataAt+data.Len())
	copy(out, "MSCF")
	binary.LittleEndian.PutUint32(out[8:], uint32(dataAt+data.Len())) //nolint:gosec // G115: fixture sizes are small
	binary.LittleEndian.PutUint32(out[16:], uint32(filesAt))
	out[24], out[25] = 3, 1
	binary.LittleEndian.PutUint16(out[26:], 1)
	binary.LittleEndian.PutUint16(out[28:], uint16(len(files)))        //nolint:gosec // G115: fixture sizes are small
	binary.LittleEndian.PutUint32(out[header:], uint32(dataAt))        //nolint:gosec // G115: fixture sizes are small
	binary.LittleEndian.PutUint16(out[header+4:], uint16(len(blocks))) //nolint:gosec // G115: fixture sizes are small
	binary.LittleEndian.PutUint16(out[header+6:], comp)
	copy(out[filesAt:], names.Bytes())
	return append(out, data.Bytes()...)
}

func TestExtract_StoredCabinetInsideAnExecutable(t *testing.T) {
	cab := cabinet(0, []string{"readme.txt", "Font.TTF"}, []int{5, 4}, [][]byte{[]byte("helloglyf")}, []int{9})
	// A self-extractor's code comes first, and may contain the signature itself.
	exe := append([]byte("MZ...MSCF but not a cabinet header..."), cab...)

	got, err := Extract(exe, "font.ttf")
	if err != nil {
		t.Fatalf("Extract(font.ttf) error = %v", err)
	}
	if string(got) != "glyf" {
		t.Errorf("Extract(font.ttf) = %q, want %q", got, "glyf")
	}
	if _, err := Extract(exe, "other.ttf"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Extract(other.ttf) error = %v, want fs.ErrNotExist", err)
	}
}

func TestExtract_MSZIPBlocksShareHistory(t *testing.T) {
	// Two 32 KiB blocks; the second repeats the first, so it compresses only
	// against the history the first block leaves.
	first := []byte(strings.Repeat("Шрифт Arial, ", 2700)[:32768])
	second := bytes.Clone(first)
	var blocks [][]byte
	for i, chunk := range [][]byte{first, second} {
		var buf bytes.Buffer
		buf.WriteString("CK")
		var dict []byte
		if i == 1 {
			dict = first
		}
		w, err := flate.NewWriterDict(&buf, flate.BestCompression, dict)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(chunk) // writes to a bytes.Buffer
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, buf.Bytes())
	}
	if len(blocks[1]) > 1000 {
		t.Fatalf("second block is %d bytes: the fixture does not depend on the first block's history", len(blocks[1]))
	}
	cab := cabinet(1, []string{"a.ttf"}, []int{65536}, blocks, []int{32768, 32768})

	got, err := Extract(cab, "a.ttf")
	if err != nil {
		t.Fatalf("Extract(a.ttf) error = %v", err)
	}
	if want := append(bytes.Clone(first), second...); !bytes.Equal(got, want) {
		t.Errorf("Extract(a.ttf) = %d bytes, want the %d bytes of both blocks", len(got), len(want))
	}
}

// bitWriter writes an LZX bitstream: bits most significant first, packed
// into 16-bit little-endian words.
type bitWriter struct {
	out  []byte
	acc  uint32
	nacc int
}

func (w *bitWriter) bits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.acc = w.acc<<1 | (v>>i)&1
		w.nacc++
		if w.nacc == 16 {
			w.out = binary.LittleEndian.AppendUint16(w.out, uint16(w.acc)) //nolint:gosec // G115: fixture sizes are small
			w.acc, w.nacc = 0, 0
		}
	}
}

// align pads with zero bits to the next 16-bit boundary.
func (w *bitWriter) align() {
	for w.nacc != 0 {
		w.bits(0, 1)
	}
}

func lzxCabinet(stream []byte, size int) []byte {
	return cabinet(0x1503, []string{"f.ttf"}, []int{size}, [][]byte{stream}, []int{size})
}

func TestExtract_LZXVerbatimBlock(t *testing.T) {
	var w bitWriter
	w.bits(0, 1)  // no E8 translation
	w.bits(1, 3)  // verbatim block
	w.bits(0, 16) // block size, high 16 bits
	w.bits(4, 8)  // ... low 8 bits: 4 bytes
	// The pretree: every symbol 5 bits long, so symbol s is coded as s.
	for range 20 {
		w.bits(5, 4)
	}
	// Main tree, first 256 lengths: every literal 9 bits long. A length is
	// coded as (previous - new) mod 17, and previous is 0: (0-9) mod 17 = 8.
	for range 256 {
		w.bits(8, 5)
	}
	// Main tree, the 400 match symbols of a 2 MiB window: symbol 256 (repeat
	// the last offset, length 2) 1 bit long, the rest unused - runs of zeros.
	for range 20 {
		w.bits(5, 4)
	}
	w.bits(16, 5) // (0-1) mod 17 = 16: symbol 256 gets length 1
	w.bits(18, 5) // a run of 20+n zeros ...
	w.bits(31, 5) // ... n = 31: 51 zeros
	for range 6 { // 6 × 51 = 306
		w.bits(18, 5)
		w.bits(31, 5)
	}
	w.bits(18, 5) // 399 - 357 = 42 more
	w.bits(22, 5)
	// Length tree: unused, all 249 lengths zero.
	for range 20 {
		w.bits(5, 4)
	}
	for range 4 { // 4 × 51 = 204
		w.bits(18, 5)
		w.bits(31, 5)
	}
	w.bits(18, 5) // 45 more
	w.bits(25, 5)
	// Canonical codes: symbol 256 is "0", literal b is "1" followed by b.
	w.bits(0x100|'a', 9)
	w.bits(0x100|'b', 9)
	w.bits(0, 1) // repeat offset 1 (the initial R0), length 2: "bb"
	w.align()

	got, err := Extract(lzxCabinet(w.out, 4), "f.ttf")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if string(got) != "abbb" {
		t.Errorf("Extract() = %q, want %q", got, "abbb")
	}
}

func TestExtract_LZXUncompressedBlock(t *testing.T) {
	var w bitWriter
	w.bits(0, 1)
	w.bits(3, 3) // uncompressed block
	w.bits(0, 16)
	w.bits(5, 8) // 5 bytes
	w.align()    // the header ends mid-word: pad to the boundary
	out := w.out
	for range 3 {
		out = binary.LittleEndian.AppendUint32(out, 1) // R0, R1, R2
	}
	out = append(out, "glyph"...)
	out = append(out, 0) // an odd-sized block is padded to a whole word

	got, err := Extract(lzxCabinet(out, 5), "f.ttf")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if string(got) != "glyph" {
		t.Errorf("Extract() = %q, want %q", got, "glyph")
	}
}

func TestExtract_RejectsMalformedCabinets(t *testing.T) {
	good := cabinet(0, []string{"a.ttf"}, []int{4}, [][]byte{[]byte("glyf")}, []int{4})
	for name, data := range map[string][]byte{
		"no cabinet":       []byte("MZ, no signature at all"),
		"truncated":        good[:len(good)-2],
		"file past folder": cabinet(0, []string{"a.ttf"}, []int{8}, [][]byte{[]byte("glyf")}, []int{4}),
		"unknown method":   cabinet(2, []string{"a.ttf"}, []int{4}, [][]byte{[]byte("glyf")}, []int{4}),
		"mszip without CK": cabinet(1, []string{"a.ttf"}, []int{4}, [][]byte{[]byte("glyf")}, []int{4}),
		"lzx garbage":      cabinet(0x1503, []string{"a.ttf"}, []int{4}, [][]byte{bytes.Repeat([]byte{0xff}, 64)}, []int{4}),
	} {
		if _, err := Extract(data, "a.ttf"); !errors.Is(err, ErrFormat) {
			t.Errorf("Extract(%s) error = %v, want ErrFormat", name, err)
		}
	}
}

func FuzzExtract(f *testing.F) {
	f.Add(cabinet(0, []string{"f.ttf"}, []int{4}, [][]byte{[]byte("glyf")}, []int{4}))
	f.Add(lzxCabinet(bytes.Repeat([]byte{0x5a, 0x01}, 40), 64))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = Extract(data, "f.ttf") // must return, without panicking
	})
}
