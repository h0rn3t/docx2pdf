// Package cab extracts files from Microsoft Cabinet archives - the format of
// the self-extracting installers Microsoft shipped its core fonts in. It reads
// single-cabinet archives, stored or compressed with MSZIP or LZX, also when
// the cabinet is embedded in a larger file such as a self-extracting .exe.
package cab

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

// ErrFormat reports a cabinet this package cannot read: malformed, spanning
// several cabinet files, or compressed with a method other than MSZIP or LZX.
var ErrFormat = errors.New("cab: malformed or unsupported cabinet")

// maxFolder caps a folder's uncompressed size, which the archive declares.
const maxFolder = 64 << 20

// Extract returns the contents of the file called name (compared without
// regard to case) in the cabinet within data. It returns an error matching
// fs.ErrNotExist when the cabinet has no such file, and one matching ErrFormat
// when it cannot read the cabinet.
func Extract(data []byte, name string) ([]byte, error) {
	cab, err := locate(data)
	if err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	filesAt := int(le.Uint32(cab[16:]))
	nFolders, nFiles, flags := int(le.Uint16(cab[26:])), int(le.Uint16(cab[28:])), le.Uint16(cab[30:])
	if flags&3 != 0 {
		return nil, fmt.Errorf("%w: spans several cabinet files", ErrFormat)
	}
	at, folderReserve, dataReserve := 36, 0, 0
	if flags&4 != 0 {
		if len(cab) < at+4 {
			return nil, ErrFormat
		}
		folderReserve, dataReserve = int(cab[at+2]), int(cab[at+3])
		at += 4 + int(le.Uint16(cab[at:]))
	}
	folderSize := 8 + folderReserve
	if len(cab) < at+nFolders*folderSize {
		return nil, ErrFormat
	}

	for range nFiles {
		if len(cab) < filesAt+16 {
			return nil, ErrFormat
		}
		size, offset, folder := int(le.Uint32(cab[filesAt:])), int(le.Uint32(cab[filesAt+4:])), int(le.Uint16(cab[filesAt+8:]))
		end := bytes.IndexByte(cab[filesAt+16:], 0)
		if end < 0 {
			return nil, ErrFormat
		}
		fileName := string(cab[filesAt+16 : filesAt+16+end])
		filesAt += 16 + end + 1
		if !strings.EqualFold(fileName, name) {
			continue
		}
		if folder >= nFolders {
			return nil, fmt.Errorf("%w: %s continues in another cabinet", ErrFormat, fileName)
		}
		f := cab[at+folder*folderSize:]
		out, err := decodeFolder(cab, int(le.Uint32(f)), int(le.Uint16(f[4:])), le.Uint16(f[6:]), dataReserve)
		if err != nil {
			return nil, err
		}
		if offset+size > len(out) {
			return nil, fmt.Errorf("%w: %s lies past the end of its folder", ErrFormat, fileName)
		}
		return out[offset : offset+size], nil
	}
	return nil, fmt.Errorf("cab: %s: %w", name, fs.ErrNotExist)
}

// locate finds the cabinet in data: the first "MSCF" signature followed by a
// plausible header, so one in a self-extractor's own code is passed over.
func locate(data []byte) ([]byte, error) {
	for from := 0; ; {
		i := bytes.Index(data[from:], []byte("MSCF"))
		if i < 0 {
			return nil, fmt.Errorf("%w: no cabinet found", ErrFormat)
		}
		c := data[from+i:]
		if len(c) >= 36 && binary.LittleEndian.Uint32(c[4:]) == 0 && c[24] == 3 && c[25] == 1 {
			if size := int(binary.LittleEndian.Uint32(c[8:])); size >= 36 && size <= len(c) {
				return c[:size], nil
			}
		}
		from += i + 1
	}
}

// decodeFolder returns a folder's uncompressed contents: its blocks of data,
// which start at start, decompressed with method.
func decodeFolder(cab []byte, start, nBlocks int, method uint16, reserve int) ([]byte, error) {
	var blocks [][]byte
	var sizes []int
	total := 0
	for range nBlocks {
		if start < 0 || len(cab) < start+8+reserve {
			return nil, ErrFormat
		}
		n, size := int(binary.LittleEndian.Uint16(cab[start+4:])), int(binary.LittleEndian.Uint16(cab[start+6:]))
		start += 8 + reserve
		if len(cab) < start+n {
			return nil, ErrFormat
		}
		blocks = append(blocks, cab[start:start+n])
		sizes = append(sizes, size)
		total += size
		start += n
	}
	if total > maxFolder {
		return nil, fmt.Errorf("%w: folder of %d bytes", ErrFormat, total)
	}

	switch method & 0xf {
	case 0: // stored
		out := bytes.Join(blocks, nil)
		if len(out) != total {
			return nil, ErrFormat
		}
		return out, nil
	case 1: // MSZIP: a deflate stream per block, each with the history the blocks before it left
		out := make([]byte, 0, total)
		for i, b := range blocks {
			if !bytes.HasPrefix(b, []byte("CK")) {
				return nil, fmt.Errorf("%w: MSZIP block without its CK signature", ErrFormat)
			}
			r := flate.NewReaderDict(bytes.NewReader(b[2:]), out[max(0, len(out)-32768):])
			n := len(out)
			out = out[:n+sizes[i]]
			if _, err := io.ReadFull(r, out[n:]); err != nil {
				return nil, fmt.Errorf("%w: MSZIP block %d: %v", ErrFormat, i, err)
			}
		}
		return out, nil
	case 3: // LZX: one bitstream across the blocks
		return decodeLZX(bytes.Join(blocks, nil), total, int(method>>8&0x1f))
	default:
		return nil, fmt.Errorf("%w: compression method %d", ErrFormat, method&0xf)
	}
}
