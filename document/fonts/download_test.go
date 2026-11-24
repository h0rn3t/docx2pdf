package fonts

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"golang.org/x/image/font/sfnt"
)

func sum(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:])
}

func zipOf(t *testing.T, members map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// serve answers each path with its body, 404 for any other, and counts the
// requests per path.
func serve(t *testing.T, bodies map[string]string) (*http.Client, string, map[string]*atomic.Int32) {
	t.Helper()
	hits := make(map[string]*atomic.Int32)
	for path := range bodies {
		hits[path] = new(atomic.Int32)
	}
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		hits[r.URL.Path].Add(1)
		_, _ = w.Write([]byte(body)) // the test reads what arrives
	}))
	client := srv.Client()
	return client, srv.URL, hits
}

func readDir(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(data)
}

func TestDownload_FetchesPlainFilesAndArchiveMembers(t *testing.T) {
	pack := zipOf(t, map[string]string{"pack/ttf/b.ttf": "font b", "pack/ttf/c.ttf": "font c"})
	client, base, hits := serve(t, map[string]string{"/a.ttf": "font a", "/pack.zip": pack})
	files := []remoteFile{
		{name: "a.ttf", url: base + "/a.ttf", sha256: sum("font a")},
		{name: "b.ttf", url: base + "/pack.zip", member: "pack/ttf/b.ttf", sha256: sum("font b")},
		{name: "c.ttf", url: base + "/pack.zip", member: "pack/ttf/c.ttf", sha256: sum("font c")},
	}
	dir := filepath.Join(t.TempDir(), "new", "fonts") // created by download

	if err := download(t.Context(), client, dir, files, map[string]string{base + "/pack.zip": sum(pack)}); err != nil {
		t.Fatalf("download() error = %v, want nil", err)
	}
	for name, want := range map[string]string{"a.ttf": "font a", "b.ttf": "font b", "c.ttf": "font c"} {
		if got := readDir(t, dir, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if n := hits["/pack.zip"].Load(); n != 1 {
		t.Errorf("archive fetched %d times, want once for both members", n)
	}
}

func TestDownload_KeepsVerifiedFilesAndReplacesOthers(t *testing.T) {
	client, base, hits := serve(t, map[string]string{"/a.ttf": "font a", "/b.ttf": "font b"})
	files := []remoteFile{
		{name: "a.ttf", url: base + "/a.ttf", sha256: sum("font a")},
		{name: "b.ttf", url: base + "/b.ttf", sha256: sum("font b")},
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.ttf"), []byte("font a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.ttf"), []byte("truncated"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := download(t.Context(), client, dir, files, nil); err != nil {
		t.Fatalf("download() error = %v, want nil", err)
	}
	if n := hits["/a.ttf"].Load(); n != 0 {
		t.Errorf("a.ttf fetched %d times, want 0: it is already there with the right checksum", n)
	}
	if got := readDir(t, dir, "b.ttf"); got != "font b" {
		t.Errorf("b.ttf = %q, want the damaged copy replaced with %q", got, "font b")
	}
}

func TestDownload_FailuresDoNotStopTheRestOrLeaveFiles(t *testing.T) {
	pack := zipOf(t, map[string]string{"other.ttf": "x"})
	client, base, _ := serve(t, map[string]string{
		"/good.ttf":     "font good",
		"/tampered.ttf": "something else",
		"/pack.zip":     pack,
		"/swapped.zip":  zipOf(t, map[string]string{"font.ttf": "font swapped"}),
	})
	files := []remoteFile{
		{name: "missing.ttf", url: base + "/missing.ttf", sha256: sum("font missing")},
		{name: "tampered.ttf", url: base + "/tampered.ttf", sha256: sum("font tampered")},
		{name: "member.ttf", url: base + "/pack.zip", member: "absent.ttf", sha256: sum("font member")},
		// The font inside is the right one, but the archive is not: it is never opened.
		{name: "swapped.ttf", url: base + "/swapped.zip", member: "font.ttf", sha256: sum("font swapped")},
		{name: "good.ttf", url: base + "/good.ttf", sha256: sum("font good")},
	}
	dir := t.TempDir()
	archiveSums := map[string]string{base + "/pack.zip": sum(pack), base + "/swapped.zip": sum("the archive published")}

	if err := download(t.Context(), client, dir, files, archiveSums); err == nil {
		t.Errorf("download() error = nil, want the four failures reported")
	}
	if got := readDir(t, dir, "good.ttf"); got != "font good" {
		t.Errorf("good.ttf = %q, want %q despite the failures before it", got, "font good")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir holds %q, want only good.ttf: a failed font leaves no file, temporary or not", names)
	}
}

func TestDownload_StopsWhenCancelled(t *testing.T) {
	client, base, hits := serve(t, map[string]string{"/a.ttf": "font a"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := download(ctx, client, t.TempDir(), []remoteFile{{name: "a.ttf", url: base + "/a.ttf", sha256: sum("font a")}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("download(cancelled) error = %v, want context.Canceled", err)
	}
	if n := hits["/a.ttf"].Load(); n != 0 {
		t.Errorf("a.ttf fetched %d times after cancel, want 0", n)
	}
}

func TestSubstitute(t *testing.T) {
	for name, want := range map[string]string{
		"Cambria":       "Caladea",
		" verdana ":     "DejaVu Sans",
		"TAHOMA":        "DejaVu Sans Condensed",
		"Georgia":       "Gelasio",
		"Comic Sans MS": "Comic Neue",
		"Arial":         "", // the embedded Liberation Sans already stands in
		"":              "",
	} {
		if got := Substitute(name); got != want {
			t.Errorf("Substitute(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestDownload_Network fetches the real catalogs and checks that every face
// is a font of the family the catalog promises - so a moved upstream file or
// a wrong checksum fails here rather than for a user. It needs the network,
// so it runs only with DOCX2PDF_NETWORK_TESTS=1 (CI sets it).
func TestDownload_Network(t *testing.T) {
	if os.Getenv("DOCX2PDF_NETWORK_TESTS") != "1" {
		t.Skip("set DOCX2PDF_NETWORK_TESTS=1 to download the font catalogs")
	}
	dir := t.TempDir()
	if err := Download(t.Context(), dir); err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if err := DownloadMicrosoftFonts(t.Context(), dir); err != nil {
		t.Fatalf("DownloadMicrosoftFonts() error = %v", err)
	}
	var buf sfnt.Buffer
	for _, f := range slices.Concat(catalog, microsoftCatalog) {
		data, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			t.Errorf("%s: %v", f.name, err)
			continue
		}
		font, err := sfnt.Parse(data)
		if err != nil {
			t.Errorf("%s: parse: %v", f.name, err)
			continue
		}
		if family, err := font.Name(&buf, sfnt.NameIDFamily); err != nil || family != f.family {
			t.Errorf("%s: family = %q (%v), want %q", f.name, family, err, f.family)
		}
	}
	for word, family := range substitutes {
		found := false
		for _, f := range catalog {
			found = found || f.family == family
		}
		if !found {
			t.Errorf("substitute for %q is %q, which the catalog does not download", word, family)
		}
	}
}
