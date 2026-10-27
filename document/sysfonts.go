package document

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/image/font/sfnt"
)

// maxFontFileSize skips font files too large to be a plain TrueType face (a
// CJK collection, say), so indexing never reads hundreds of megabytes.
const maxFontFileSize = 32 << 20

// faceKey names one face of a font family; family is lower-cased.
type faceKey struct {
	family       string
	bold, italic bool
}

func newFaceKey(family string, bold, italic bool) faceKey {
	return faceKey{family: strings.ToLower(strings.TrimSpace(family)), bold: bold, italic: italic}
}

// fontIndex finds installed fonts by family name: the TrueType files in a set
// of directories, indexed by their family and style (the name table's family
// and subfamily) the first time a face is looked up.
type fontIndex struct {
	dirs  []string
	once  sync.Once
	files map[faceKey]string
}

func newFontIndex(dirs []string) *fontIndex {
	if len(dirs) == 0 {
		dirs = defaultFontDirs()
	}
	return &fontIndex{dirs: dirs}
}

// defaultFontDirs are the platform's usual font directories, the user's own
// included.
func defaultFontDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		dirs := []string{filepath.Join(os.Getenv("WINDIR"), "Fonts")}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			dirs = append(dirs, filepath.Join(local, "Microsoft", "Windows", "Fonts"))
		}
		return dirs
	case "darwin":
		return []string{"/Library/Fonts", "/System/Library/Fonts", filepath.Join(home, "Library", "Fonts")}
	default:
		return []string{"/usr/share/fonts", "/usr/local/share/fonts", filepath.Join(home, ".fonts"), filepath.Join(home, ".local", "share", "fonts")}
	}
}

// lookup returns the file bytes of the installed face k, or nil when there is
// none (or it cannot be read). A nil index finds nothing.
func (ix *fontIndex) lookup(k faceKey) []byte {
	if ix == nil {
		return nil
	}
	ix.once.Do(ix.build)
	path, ok := ix.files[k]
	if !ok {
		return nil
	}
	data, err := readFontFile(path)
	if err != nil {
		return nil
	}
	return data
}

// build indexes every .ttf under the directories. A directory or file that
// cannot be read, or a font that does not parse, just contributes nothing.
func (ix *fontIndex) build() {
	ix.files = make(map[faceKey]string)
	var buf sfnt.Buffer
	for _, dir := range ix.dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".ttf") {
				if k, ok := fontFace(path, &buf); ok && ix.files[k] == "" {
					ix.files[k] = path
				}
			}
			return nil
		})
	}
}

// fontFace reads which face of which family the font file at path is.
func fontFace(path string, buf *sfnt.Buffer) (faceKey, bool) {
	data, err := readFontFile(path)
	if err != nil {
		return faceKey{}, false
	}
	f, err := sfnt.Parse(data)
	if err != nil {
		return faceKey{}, false
	}
	family, err := f.Name(buf, sfnt.NameIDFamily)
	if err != nil {
		return faceKey{}, false
	}
	style, err := f.Name(buf, sfnt.NameIDSubfamily)
	if err != nil {
		return faceKey{}, false
	}
	bold, italic, ok := ribbi(style)
	return newFaceKey(family, bold, italic), ok
}

// ribbi reads a name table subfamily: one of the four styles a family's
// Regular/Bold/Italic/Bold Italic faces carry. Anything else (Light,
// Condensed, ...) is a family of its own under another name, and not ok here.
func ribbi(subfamily string) (bold, italic, ok bool) {
	switch strings.ToLower(strings.TrimSpace(subfamily)) {
	case "regular", "normal", "roman", "book":
		return false, false, true
	case "bold":
		return true, false, true
	case "italic", "oblique":
		return false, true, true
	case "bold italic", "bold oblique", "bolditalic":
		return true, true, true
	}
	return false, false, false
}

func readFontFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxFontFileSize))
}
