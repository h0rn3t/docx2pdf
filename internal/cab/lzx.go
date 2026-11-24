package cab

import (
	"encoding/binary"
	"fmt"
)

// LZX as cabinets use it ([MS-PATCH] and libmspack's lzxd.c): 32 KiB output
// frames, each ending on a 16-bit boundary of the bitstream, made of blocks
// that are verbatim, aligned-offset, or stored.
const (
	frameSize     = 32768
	minMatch      = 2
	numChars      = 256
	pretreeSize   = 20
	alignedSize   = 8
	lengthSize    = 249
	maxMainSize   = numChars + 50*8
	lensSlack     = 64 // a run of lengths may overrun its table, as in libmspack
	blockVerbatim = 1
	blockAligned  = 2
	blockStored   = 3
)

// positionSlots is the number of match position slots for each window size,
// 2^15 to 2^21 bytes.
var positionSlots = map[int]int{15: 30, 16: 32, 17: 34, 18: 36, 19: 38, 20: 42, 21: 50}

// extraBits and positionBase give, for each position slot, how many bits
// follow it and the smallest offset it codes.
var extraBits, positionBase = func() (extra [51]int, base [51]int) {
	for i, j := 0, 0; i < 51; i += 2 {
		extra[i] = j
		if i+1 < 51 {
			extra[i+1] = j
		}
		if i != 0 && j < 17 {
			j++
		}
	}
	for i, j := 0, 0; i < 51; i++ {
		base[i] = j
		j += 1 << extra[i]
	}
	return extra, base
}()

// huffman decodes a canonical Huffman code of at most 16 bits by table
// lookup: entry is symbol<<5 | length, zero for a code no symbol has.
type huffman struct{ table []uint32 }

func (h *huffman) build(lens []int) error {
	if h.table == nil {
		h.table = make([]uint32, 1<<16)
	}
	clear(h.table)
	code := 0
	for l := 1; l <= 16; l++ {
		for sym, sl := range lens {
			if sl != l {
				continue
			}
			start := code << (16 - l)
			end := start + 1<<(16-l)
			if end > 1<<16 {
				return fmt.Errorf("%w: LZX Huffman code is over-subscribed", ErrFormat)
			}
			for i := start; i < end; i++ {
				h.table[i] = uint32(sym)<<5 | uint32(l)
			}
			code++
		}
		code <<= 1
	}
	return nil
}

type lzxDecoder struct {
	in  []byte
	pos int // in bits
	err error
	out []byte

	r                              [3]int // the three most recent match offsets
	mainSize                       int
	mainLens                       [maxMainSize + lensSlack]int
	lengthLens                     [lengthSize + lensSlack]int
	pretree, main, length, aligned huffman
	intelStarted                   bool
}

// bits reads n ≤ 17 bits. Reading past the input sets d.err and reads zeros.
func (d *lzxDecoder) bits(n int) int {
	v := d.peek()
	d.pos += n
	if d.pos > len(d.in)*8 && d.err == nil {
		d.err = fmt.Errorf("%w: LZX stream is truncated", ErrFormat)
	}
	return int(v >> (32 - n))
}

// peek returns the next 32 bits of the stream, most significant first.
func (d *lzxDecoder) peek() uint32 {
	var v uint64
	w := d.pos / 16
	for i := range 3 {
		v <<= 16
		if b := (w + i) * 2; b+1 < len(d.in) {
			v |= uint64(binary.LittleEndian.Uint16(d.in[b:]))
		}
	}
	return uint32(v >> (16 - d.pos%16)) //nolint:gosec // G115: v holds 48 bits, shifted right by at least 1 and truncated to the 32 wanted
}

func (d *lzxDecoder) symbol(h *huffman) int {
	e := h.table[d.peek()>>16]
	if e == 0 {
		if d.err == nil {
			d.err = fmt.Errorf("%w: LZX code with no symbol", ErrFormat)
		}
		return 0
	}
	d.bits(int(e & 31))
	return int(e >> 5)
}

// readLengths updates lens[first:last], the code lengths of a tree, from the
// pretree-coded deltas that follow in the stream.
func (d *lzxDecoder) readLengths(lens []int, first, last int) {
	var pre [pretreeSize]int
	for i := range pre {
		pre[i] = d.bits(4)
	}
	if err := d.pretree.build(pre[:]); err != nil {
		d.err = err
		return
	}
	fill := func(x, n, v int) int {
		if x+n > len(lens) {
			d.err = fmt.Errorf("%w: LZX code lengths overrun their table", ErrFormat)
			return len(lens)
		}
		for i := range n {
			lens[x+i] = v
		}
		return x + n
	}
	for x := first; x < last && d.err == nil; {
		switch z := d.symbol(&d.pretree); z {
		case 17:
			x = fill(x, d.bits(4)+4, 0)
		case 18:
			x = fill(x, d.bits(5)+20, 0)
		case 19:
			n := d.bits(1) + 4
			z = d.symbol(&d.pretree)
			if z > 16 {
				d.err = fmt.Errorf("%w: LZX length delta %d", ErrFormat, z)
				return
			}
			x = fill(x, n, (lens[x]-z+17)%17)
		default:
			lens[x] = (lens[x] - z + 17) % 17
			x++
		}
	}
}

// decodeLZX decompresses the LZX stream in to its total uncompressed bytes,
// with a window of 2^windowBits bytes.
func decodeLZX(in []byte, total, windowBits int) ([]byte, error) {
	slots, ok := positionSlots[windowBits]
	if !ok {
		return nil, fmt.Errorf("%w: LZX window of 2^%d bytes", ErrFormat, windowBits)
	}
	d := &lzxDecoder{in: in, out: make([]byte, 0, total), r: [3]int{1, 1, 1}, mainSize: numChars + slots*8}
	result := make([]byte, total)

	var translation int // E8 call translation size; 0 when off
	if d.bits(1) == 1 {
		translation = d.bits(16)<<16 | d.bits(16)
	}
	blockType, blockLen, remaining := 0, 0, 0
	for frameStart := 0; frameStart < total; frameStart += frameSize {
		frameEnd := min(frameStart+frameSize, total)
		for len(d.out) < frameEnd && d.err == nil {
			if remaining == 0 {
				if blockType == blockStored && blockLen&1 == 1 {
					d.pos += 8 // a stored block of odd size is padded
				}
				blockType, blockLen = d.blockHeader()
				remaining = blockLen
				continue
			}
			run := min(remaining, frameEnd-len(d.out))
			produced := 0
			if blockType == blockStored {
				produced = d.stored(run)
			} else {
				produced = d.matches(run, blockType == blockAligned)
			}
			if produced > remaining {
				return nil, fmt.Errorf("%w: LZX match runs past its block", ErrFormat)
			}
			remaining -= produced
		}
		if d.err != nil {
			return nil, d.err
		}
		if blockType != blockStored {
			d.pos = (d.pos + 15) &^ 15 // each frame ends on a 16-bit boundary
		}
		frame := result[frameStart:frameEnd]
		copy(frame, d.out[frameStart:frameEnd])
		if d.intelStarted && translation != 0 && len(frame) > 10 {
			translateE8(frame, frameStart, translation)
		}
	}
	if len(d.out) != total {
		return nil, fmt.Errorf("%w: LZX output is %d bytes, want %d", ErrFormat, len(d.out), total)
	}
	return result, nil
}

// blockHeader reads a block's type and size, and its trees or stored offsets.
func (d *lzxDecoder) blockHeader() (blockType, size int) {
	blockType = d.bits(3)
	size = d.bits(16)<<8 | d.bits(8)
	switch blockType {
	case blockAligned:
		var lens [alignedSize]int
		for i := range lens {
			lens[i] = d.bits(3)
		}
		if err := d.aligned.build(lens[:]); err != nil {
			d.err = err
		}
		fallthrough
	case blockVerbatim:
		d.readLengths(d.mainLens[:], 0, numChars)
		d.readLengths(d.mainLens[:], numChars, d.mainSize)
		d.readLengths(d.lengthLens[:], 0, lengthSize)
		if d.err != nil {
			return
		}
		if err := d.main.build(d.mainLens[:d.mainSize]); err != nil {
			d.err = err
		}
		if err := d.length.build(d.lengthLens[:lengthSize]); err != nil {
			d.err = err
		}
		if d.mainLens[0xE8] != 0 {
			d.intelStarted = true
		}
	case blockStored:
		d.intelStarted = true
		d.pos += 16 - d.pos%16 // 1 to 16 bits of padding
		at := d.pos / 8
		if at+12 > len(d.in) {
			d.err = fmt.Errorf("%w: LZX stream is truncated", ErrFormat)
			return
		}
		for i := range d.r {
			d.r[i] = int(binary.LittleEndian.Uint32(d.in[at+4*i:]))
		}
		d.pos += 96
	default:
		d.err = fmt.Errorf("%w: LZX block type %d", ErrFormat, blockType)
	}
	if size == 0 && d.err == nil {
		d.err = fmt.Errorf("%w: empty LZX block", ErrFormat)
	}
	return blockType, size
}

// stored copies n bytes of a stored block.
func (d *lzxDecoder) stored(n int) int {
	at := d.pos / 8
	if at+n > len(d.in) {
		d.err = fmt.Errorf("%w: LZX stream is truncated", ErrFormat)
		return 0
	}
	d.out = append(d.out, d.in[at:at+n]...)
	d.pos += n * 8
	return n
}

// matches decodes literals and matches until at least n bytes are output, and
// returns how many were: a match may run past n.
func (d *lzxDecoder) matches(n int, aligned bool) int {
	start := len(d.out)
	for len(d.out)-start < n && d.err == nil {
		sym := d.symbol(&d.main)
		if sym < numChars {
			d.out = append(d.out, byte(sym)) //nolint:gosec // G115: sym < numChars, a byte
			continue
		}
		sym -= numChars
		length := sym & 7
		if length == 7 {
			length += d.symbol(&d.length)
		}
		length += minMatch

		var offset int
		switch slot := sym >> 3; slot {
		case 0:
			offset = d.r[0]
		case 1:
			offset = d.r[1]
			d.r[1] = d.r[0]
		case 2:
			offset = d.r[2]
			d.r[2] = d.r[0]
		default:
			extra := extraBits[slot]
			offset = positionBase[slot] - 2
			switch {
			case aligned && extra > 3:
				offset += d.bits(extra-3)<<3 + d.symbol(&d.aligned)
			case aligned && extra == 3:
				offset += d.symbol(&d.aligned)
			case extra > 0:
				offset += d.bits(extra)
			default:
				offset = 1
			}
			d.r[2], d.r[1] = d.r[1], d.r[0]
		}
		d.r[0] = offset

		if offset <= 0 || offset > len(d.out) || len(d.out)+length > cap(d.out)+frameSize {
			if d.err == nil {
				d.err = fmt.Errorf("%w: LZX match out of range", ErrFormat)
			}
			break
		}
		for range length {
			d.out = append(d.out, d.out[len(d.out)-offset])
		}
	}
	return len(d.out) - start
}

// translateE8 undoes the encoder's x86 CALL translation on one output frame
// that starts at offset pos: each E8 byte's 32-bit operand, stored as an
// absolute address, becomes relative again.
func translateE8(frame []byte, pos, size int) {
	for i := 0; i < len(frame)-10; {
		if frame[i] != 0xE8 {
			i++
			continue
		}
		abs := int(int32(binary.LittleEndian.Uint32(frame[i+1:]))) //nolint:gosec // G115: the operand is a signed 32-bit address
		cur := pos + i
		if abs >= -cur && abs < size {
			rel := abs - cur
			if abs < 0 {
				rel = abs + size
			}
			binary.LittleEndian.PutUint32(frame[i+1:], uint32(int32(rel))) //nolint:gosec // G115: |rel| < size, a 32-bit value
		}
		i += 5
	}
}
