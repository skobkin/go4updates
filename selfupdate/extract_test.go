package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression: with WithMaxExtractedBytes(math.MaxInt64), the former
// io.LimitReader(max+1) bound overflowed to a negative N and truncated the
// member to zero bytes while every size check passed.
func TestExtractMaxInt64BoundDoesNotTruncate(t *testing.T) {
	dir := t.TempDir()

	archivePath := buildZip(t, dir, []zipEntry{{name: "app", content: "payload"}})

	dstPath := filepath.Join(dir, "staged")

	if err := extractBinary(archivePath, "app_1.0.0_linux_amd64.zip", "app", "linux", dstPath, math.MaxInt64); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}

	if got := readFileForTest(t, dstPath); got != "payload" {
		t.Errorf("extracted content = %q, want payload", got)
	}
}

// zipEntry describes one member of a built test archive.
type zipEntry struct {
	name    string
	content string
	mode    os.FileMode // zero: regular file default
	symlink string      // non-empty: member is a symlink pointing at this target
}

func buildZip(t *testing.T, dir string, entries []zipEntry) string {
	t.Helper()

	path := filepath.Join(dir, "archive.zip")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating zip: %v", err)
	}

	w := zip.NewWriter(f)

	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name}

		if e.symlink != "" {
			hdr.SetMode(os.ModeSymlink | 0o777)

			// The link target travels in the member body.
			e.content = e.symlink
		} else if e.mode != 0 {
			hdr.SetMode(e.mode)
		}

		member, err := w.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("creating zip member %q: %v", e.name, err)
		}

		if _, err := io.Copy(member, strings.NewReader(e.content)); err != nil {
			t.Fatalf("writing zip member %q: %v", e.name, err)
		}
	}

	if err := w.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("closing zip file: %v", err)
	}

	return path
}

// tarEntry describes one member of a built tar test archive.
type tarEntry struct {
	name    string
	content string
	dir     bool
	symlink string
}

func buildTarBytes(t *testing.T, entries []tarEntry, gzipWrap bool) []byte {
	t.Helper()

	var buf bytes.Buffer

	var dst io.Writer = &buf

	var gz *gzip.Writer

	if gzipWrap {
		gz = gzip.NewWriter(&buf)

		dst = gz
	}

	tw := tar.NewWriter(dst)

	for _, e := range entries {
		hdr := &tar.Header{
			Name:    e.name,
			Mode:    0o755,
			ModTime: time.Unix(1700000000, 0),
		}

		switch {
		case e.dir:
			hdr.Typeflag = tar.TypeDir

		case e.symlink != "":
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.symlink

		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.content))
		}

		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header %q: %v", e.name, err)
		}

		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(tw, e.content); err != nil {
				t.Fatalf("writing tar member %q: %v", e.name, err)
			}
		}
	}

	// Close in wrap order so every buffer is flushed before buf is read.
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}

	if gz != nil {
		if err := gz.Close(); err != nil {
			t.Fatalf("closing gzip: %v", err)
		}
	}

	return buf.Bytes()
}

// buildTar writes a tar.gz (or plain tar when plain is set) and returns its
// path and the asset name matching that format.
func buildTar(t *testing.T, dir string, plain bool, entries []tarEntry) (path, assetName string) {
	t.Helper()

	data := buildTarBytes(t, entries, !plain)

	if plain {
		path = filepath.Join(dir, "archive.tar")
		assetName = "app_1.0.0_linux_amd64.tar"
	} else {
		path = filepath.Join(dir, "archive.tar.gz")
		assetName = "app_1.0.0_linux_amd64.tar.gz"
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing archive: %v", err)
	}

	return path, assetName
}

func buildGzipFile(t *testing.T, dir, content string) (path, assetName string) {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)

	if _, err := io.WriteString(gz, content); err != nil {
		t.Fatalf("writing gzip payload: %v", err)
	}

	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip: %v", err)
	}

	path = filepath.Join(dir, "archive.gz")

	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("writing archive: %v", err)
	}

	return path, "app_1.0.0_linux_amd64.gz"
}

func buildRawFile(t *testing.T, dir, content string) (path, assetName string) {
	t.Helper()

	path = filepath.Join(dir, "archive.bin")

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing asset: %v", err)
	}

	return path, "app_1.0.0_linux_amd64"
}

// extractForTest runs extractBinary with dstPath inside dir and returns the
// extracted content.
func extractForTest(t *testing.T, archivePath, assetName, appName, goos string, maxExtracted int64) (string, error) {
	t.Helper()

	dstPath := filepath.Join(t.TempDir(), "staged")

	err := extractBinary(archivePath, assetName, appName, goos, dstPath, maxExtracted)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}

	return string(content), nil
}

func TestExtractBinaryFormats(t *testing.T) {
	payload := "executable payload"

	t.Run("zip root member", func(t *testing.T) {
		dir := t.TempDir()
		path := buildZip(t, dir, []zipEntry{
			{name: "app_1.0.0_linux_amd64.tar.gz.sha256", content: "notanarchive"},
			{name: "app", content: payload},
		})

		got, err := extractForTest(t, path, "app_1.0.0_linux_amd64.zip", "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("zip nested member", func(t *testing.T) {
		dir := t.TempDir()
		path := buildZip(t, dir, []zipEntry{
			{name: "bin/", mode: 0o755 | os.ModeDir},
			{name: "bin/app", content: payload},
			{name: "docs/readme.md", content: "words"},
		})

		got, err := extractForTest(t, path, "app_1.0.0_linux_amd64.zip", "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("tar.gz", func(t *testing.T) {
		dir := t.TempDir()
		path, assetName := buildTar(t, dir, false, []tarEntry{
			{name: "app", content: payload},
			{name: "readme.txt", content: "words"},
		})

		got, err := extractForTest(t, path, assetName, "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("tgz", func(t *testing.T) {
		dir := t.TempDir()
		path, _ := buildTar(t, dir, false, []tarEntry{{name: "app", content: payload}})

		got, err := extractForTest(t, path, "app_1.0.0_linux_amd64.tgz", "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("plain tar", func(t *testing.T) {
		dir := t.TempDir()
		path, assetName := buildTar(t, dir, true, []tarEntry{
			{name: "app", content: payload},
			{name: "license", content: "MIT"},
		})

		got, err := extractForTest(t, path, assetName, "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("single-member gz", func(t *testing.T) {
		dir := t.TempDir()
		path, assetName := buildGzipFile(t, dir, payload)

		got, err := extractForTest(t, path, assetName, "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("raw binary", func(t *testing.T) {
		dir := t.TempDir()
		path, assetName := buildRawFile(t, dir, payload)

		got, err := extractForTest(t, path, assetName, "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("windows exe inside zip", func(t *testing.T) {
		dir := t.TempDir()
		path := buildZip(t, dir, []zipEntry{{name: "app.exe", content: payload}})

		got, err := extractForTest(t, path, "app_1.0.0_windows_amd64.zip", "app", "windows", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("unsupported format fails before opening", func(t *testing.T) {
		_, err := extractForTest(t, "/nonexistent/archive", "app_1.0.0_linux_amd64.tar.xz", "app", "linux", DefaultMaxExtractedBytes)
		if !errors.Is(err, ErrUnsupportedArchive) {
			t.Fatalf("err = %v, want ErrUnsupportedArchive", err)
		}
	})
}

func TestExtractBinarySelection(t *testing.T) {
	payload := "chosen"

	t.Run("single-file fallback", func(t *testing.T) {
		dir := t.TempDir()
		path := buildZip(t, dir, []zipEntry{{name: "weird-inner-name", content: payload}})

		got, err := extractForTest(t, path, "app_1.0.0_linux_amd64.zip", "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("no fallback with several unnamed files", func(t *testing.T) {
		dir := t.TempDir()
		path := buildZip(t, dir, []zipEntry{
			{name: "one", content: "1"},
			{name: "two", content: "2"},
		})

		_, err := extractForTest(t, path, "app_1.0.0_linux_amd64.zip", "app", "linux", DefaultMaxExtractedBytes)
		if !errors.Is(err, ErrExecutableNotFound) {
			t.Fatalf("err = %v, want ErrExecutableNotFound", err)
		}
	})

	t.Run("zip ambiguity", func(t *testing.T) {
		dir := t.TempDir()
		path := buildZip(t, dir, []zipEntry{
			{name: "app", content: payload},
			{name: "bin/app", content: "other"},
		})

		_, err := extractForTest(t, path, "app_1.0.0_linux_amd64.zip", "app", "linux", DefaultMaxExtractedBytes)
		if !errors.Is(err, ErrAmbiguousExecutable) {
			t.Fatalf("err = %v, want ErrAmbiguousExecutable", err)
		}

		if !strings.Contains(err.Error(), "bin/app") {
			t.Errorf("error should list the members, got: %v", err)
		}
	})

	t.Run("tar ambiguity", func(t *testing.T) {
		dir := t.TempDir()
		path, assetName := buildTar(t, dir, false, []tarEntry{
			{name: "app", content: payload},
			{name: "lib/app", content: "other"},
		})

		_, err := extractForTest(t, path, assetName, "app", "linux", DefaultMaxExtractedBytes)
		if !errors.Is(err, ErrAmbiguousExecutable) {
			t.Fatalf("err = %v, want ErrAmbiguousExecutable", err)
		}
	})

	t.Run("tar named candidate beats spooled fallback", func(t *testing.T) {
		dir := t.TempDir()
		path, assetName := buildTar(t, dir, false, []tarEntry{
			{name: "unknown-name", content: "spooled"},
			{name: "app", content: payload},
		})

		got, err := extractForTest(t, path, assetName, "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want the named candidate %q", got, payload)
		}
	})

	t.Run("tar fallback when only one regular file", func(t *testing.T) {
		dir := t.TempDir()
		path, assetName := buildTar(t, dir, false, []tarEntry{
			{name: "somedir/", dir: true},
			{name: "the-only-file", content: payload},
		})

		got, err := extractForTest(t, path, assetName, "app", "linux", DefaultMaxExtractedBytes)
		if err != nil {
			t.Fatalf("extractBinary: %v", err)
		}

		if got != payload {
			t.Errorf("extracted %q, want %q", got, payload)
		}
	})

	t.Run("tar symlink match disqualifies", func(t *testing.T) {
		dir := t.TempDir()
		path, assetName := buildTar(t, dir, false, []tarEntry{
			{name: "app", symlink: "/etc/passwd"},
			{name: "other", content: "x"},
		})

		_, err := extractForTest(t, path, assetName, "app", "linux", DefaultMaxExtractedBytes)
		if !errors.Is(err, ErrExecutableNotFound) {
			t.Fatalf("err = %v, want ErrExecutableNotFound", err)
		}
	})

	t.Run("zip symlink match disqualifies", func(t *testing.T) {
		dir := t.TempDir()
		path := buildZip(t, dir, []zipEntry{
			{name: "app", symlink: "/etc/passwd"},
			{name: "other", content: "x"},
		})

		_, err := extractForTest(t, path, "app_1.0.0_linux_amd64.zip", "app", "linux", DefaultMaxExtractedBytes)
		if !errors.Is(err, ErrExecutableNotFound) {
			t.Fatalf("err = %v, want ErrExecutableNotFound", err)
		}
	})
}

func TestExtractBinaryZipSlip(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")

	if err := os.Mkdir(staging, 0o755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}

	path := buildZip(t, dir, []zipEntry{
		{name: "../evil", content: "escaped"},
		{name: "/tmp/evil_abs", content: "escaped"},
		{name: "app", content: "payload"},
	})

	dstPath := filepath.Join(staging, "staged")

	err := extractBinary(path, "app_1.0.0_linux_amd64.zip", "app", "linux", dstPath, DefaultMaxExtractedBytes)
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}

	// Nothing may exist outside the staging directory.
	for _, evil := range []string{filepath.Join(dir, "evil"), "/tmp/evil_abs"} {
		if _, statErr := os.Stat(evil); statErr == nil {
			t.Errorf("zip-slip: %s was created", evil)
		}
	}
}

func TestExtractBinaryBombs(t *testing.T) {
	const limit = int64(1 << 20) // 1 MiB

	t.Run("tar.gz decompression bomb is aborted", func(t *testing.T) {
		dir := t.TempDir()

		// 8 MiB of zeros compress to a few KiB.
		path, assetName := buildTar(t, dir, false, []tarEntry{
			{name: "app", content: strings.Repeat("\x00", 8<<20)},
		})

		dstPath := filepath.Join(t.TempDir(), "staged")

		err := extractBinary(path, assetName, "app", "linux", dstPath, limit)
		if !errors.Is(err, ErrExtractedTooLarge) {
			t.Fatalf("err = %v, want ErrExtractedTooLarge", err)
		}

		if _, statErr := os.Stat(dstPath); statErr == nil {
			t.Error("no partial output should survive a failed extraction")
		}
	})

	t.Run("zip member bomb is aborted", func(t *testing.T) {
		dir := t.TempDir()
		path := buildZip(t, dir, []zipEntry{
			{name: "app", content: strings.Repeat("\x00", 8<<20)},
		})

		dstPath := filepath.Join(t.TempDir(), "staged")

		err := extractBinary(path, "app_1.0.0_linux_amd64.zip", "app", "linux", dstPath, limit)
		if !errors.Is(err, ErrExtractedTooLarge) {
			t.Fatalf("err = %v, want ErrExtractedTooLarge", err)
		}
	})

	t.Run("entry flood is rejected", func(t *testing.T) {
		dir := t.TempDir()

		entries := make([]zipEntry, 0, maxArchiveEntries+1)

		for i := range maxArchiveEntries + 1 {
			entries = append(entries, zipEntry{name: fmt.Sprintf("f%04d", i), content: "x"})
		}

		path := buildZip(t, dir, entries)

		dstPath := filepath.Join(t.TempDir(), "staged")

		err := extractBinary(path, "app_1.0.0_linux_amd64.zip", "app", "linux", dstPath, DefaultMaxExtractedBytes)
		if !errors.Is(err, ErrExtractedTooLarge) {
			t.Fatalf("err = %v, want ErrExtractedTooLarge for the entry flood", err)
		}
	})
}

// Regression: the entry cap was enforced only after zip.OpenReader had
// walked the whole declared central directory, so an archive declaring
// millions of entries paid the full parse before the cap could fire. The
// declared count is now checked from the file tail before archive/zip sees
// the archive. These archives are hand-built: a payload of junk followed by
// end-of-central-directory records declaring far more entries than the file
// could possibly contain.
func TestExtractZipDeclaredEntryFloodRejectedBeforeParsing(t *testing.T) {
	const junkLen = 64

	writeFlood := func(t *testing.T, dir string, parts ...[]byte) string {
		t.Helper()

		var buf bytes.Buffer

		buf.Write(make([]byte, junkLen))

		for _, p := range parts {
			buf.Write(p)
		}

		path := filepath.Join(dir, "flood.zip")

		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatalf("writing hostile archive: %v", err)
		}

		return path
	}

	t.Run("declared through the zip64 record", func(t *testing.T) {
		dir := t.TempDir()

		zip64 := make([]byte, 56)

		binary.LittleEndian.PutUint32(zip64[0:], 0x06064b50) // zip64 EOCD signature
		binary.LittleEndian.PutUint64(zip64[4:], 44)         // size of the remaining record
		binary.LittleEndian.PutUint64(zip64[32:], 5_000_000) // total entry count
		// directorySize (+40) and directoryOffset (+48) stay zero.

		loc := make([]byte, 20)

		binary.LittleEndian.PutUint32(loc[0:], 0x07064b50) // zip64 locator signature
		binary.LittleEndian.PutUint64(loc[8:], junkLen)    // zip64 EOCD offset
		binary.LittleEndian.PutUint32(loc[16:], 1)         // total disks

		eocd := make([]byte, 22)

		binary.LittleEndian.PutUint32(eocd[0:], 0x06054b50) // EOCD signature
		binary.LittleEndian.PutUint16(eocd[8:], 0xffff)     // entries on this disk
		binary.LittleEndian.PutUint16(eocd[10:], 0xffff)    // total entries: the zip64 trigger
		// directorySize (+12) and directoryOffset (+16) stay zero.

		path := writeFlood(t, dir, zip64, loc, eocd)

		dstPath := filepath.Join(dir, "staged")

		err := extractBinary(path, "app_1.0.0_linux_amd64.zip", "app", "linux", dstPath, DefaultMaxExtractedBytes)
		if !errors.Is(err, ErrExtractedTooLarge) {
			t.Fatalf("err = %v, want ErrExtractedTooLarge for the zip64-declared flood", err)
		}
	})

	t.Run("declared through the plain EOCD count", func(t *testing.T) {
		dir := t.TempDir()

		eocd := make([]byte, 22)

		binary.LittleEndian.PutUint32(eocd[0:], 0x06054b50) // EOCD signature
		binary.LittleEndian.PutUint16(eocd[10:], maxArchiveEntries+1)

		path := writeFlood(t, dir, eocd)

		dstPath := filepath.Join(dir, "staged")

		err := extractBinary(path, "app_1.0.0_linux_amd64.zip", "app", "linux", dstPath, DefaultMaxExtractedBytes)
		if !errors.Is(err, ErrExtractedTooLarge) {
			t.Fatalf("err = %v, want ErrExtractedTooLarge for the EOCD-declared flood", err)
		}
	})
}
