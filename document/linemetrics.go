package document

import "encoding/binary"

// fontLineMetrics reads how tall a line of a TrueType font is and where its
// baseline sits, in ems, the way Word lays lines out: the taller of the hhea
// line (ascender + descender + line gap) and the OS/2 win line (usWinAscent +
// usWinDescent), with the win descent below the baseline and the rest above
// it. ok is false for data without those tables or with nonsense in them.
func fontLineMetrics(data []byte) (ascent, height float64, ok bool) {
	table := func(tag string) []byte {
		if len(data) < 12 {
			return nil
		}
		for i := range int(binary.BigEndian.Uint16(data[4:])) {
			rec := 12 + 16*i
			if rec+16 > len(data) {
				return nil
			}
			if string(data[rec:rec+4]) == tag {
				off, n := uint64(binary.BigEndian.Uint32(data[rec+8:])), uint64(binary.BigEndian.Uint32(data[rec+12:]))
				if off+n > uint64(len(data)) {
					return nil
				}
				return data[off : off+n]
			}
		}
		return nil
	}
	head, hhea, os2 := table("head"), table("hhea"), table("OS/2")
	if len(head) < 20 || len(hhea) < 10 {
		return 0, 0, false
	}
	upm := float64(binary.BigEndian.Uint16(head[18:]))
	i16 := func(b []byte, off int) float64 { return float64(int16(binary.BigEndian.Uint16(b[off:]))) }
	descent := -i16(hhea, 6)
	height = i16(hhea, 4) + descent + max(i16(hhea, 8), 0)
	if len(os2) >= 78 {
		winAscent, winDescent := float64(binary.BigEndian.Uint16(os2[74:])), float64(binary.BigEndian.Uint16(os2[76:]))
		if winAscent+winDescent > 0 {
			height, descent = max(height, winAscent+winDescent), winDescent
		}
	}
	if upm == 0 || height <= 0 || descent < 0 || descent >= height {
		return 0, 0, false
	}
	return (height - descent) / upm, height / upm, true
}
