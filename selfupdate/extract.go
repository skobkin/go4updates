package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"strconv"
	"strings"
)

// maxArchiveEntries caps how many entries an archive may contain. Executable
// archives ship a handful of files; thousands of entries are a
// resource-exhaustion attack, not a release layout.
const maxArchiveEntries = 1024

// Zip end-of-central-directory structure offsets, used only by the declared
// entry count pre-parse. The record is fixed-size and little-endian, so it
// is decoded by hand instead of pulling the whole central directory through
// archive/zip first.
const (
	zipEOCDSignature      = 0x06054b50 // "PK\x05\x06"
	zip64LocatorSignature = 0x07064b50 // "PK\x06\x07"
	zip64EOCDSignature    = 0x06064b50 // "PK\x06\x06"

	zipEOCDLen            = 22
	zip64LocatorLen       = 20
	zip64EOCDLen          = 56
	zipCommentMaxLen      = 65535
	zipEOCDTotalEntries   = 10 // uint16: total central-directory entries
	zipEOCDCommentLen     = 20 // uint16: comment length
	zip64LocatorEOCDOff   = 8  // uint64: offset of the zip64 EOCD record
	zip64EOCDTotalEntries = 32 // uint64: total central-directory entries
	zip64EOCDDirSize      = 40 // uint64: central directory size
	zip64EOCDDirOffset    = 48 // uint64: central directory offset
)

// zipDeclaredEntryCount reads the total entry count declared in the
// archive's end-of-central-directory record from the file tail, without
// parsing the central directory itself. The zip64 record supersedes the
// 16-bit count when a well-formed locator points at it. ok is false when no
// plausible EOCD can be located or decoded: the caller then falls through to
// archive/zip, which reports the format problem itself.
//
// It exists so the caller can reject an archive that declares more entries
// than the limit before archive/zip walks the directory: OpenReader parses
// every declared record before the parsed count could be checked, which is
// exactly the resource cost a hostile declared count tries to extract.
func zipDeclaredEntryCount(archivePath string) (n int64, ok bool) {
	f, err := os.Open(archivePath)
	if err != nil {
		return 0, false
	}

	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return 0, false
	}

	// The EOCD is at most its fixed length plus a maximum comment away from
	// the end of the file.
	tailLen := int64(zipEOCDLen + zipCommentMaxLen)

	if size := info.Size(); size < tailLen {
		tailLen = size
	}

	tail := make([]byte, tailLen)

	if _, err := f.ReadAt(tail, info.Size()-tailLen); err != nil {
		return 0, false
	}

	// Scan backward like archive/zip: the record closest to the end wins,
	// signatures inside a comment lose to the real record.
	for i := len(tail) - zipEOCDLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) != zipEOCDSignature {
			continue
		}

		count := int64(binary.LittleEndian.Uint16(tail[i+zipEOCDTotalEntries:]))

		// A zip64 locator directly precedes the EOCD and points at a zip64
		// EOCD record whose 64-bit count supersedes the 16-bit one.
		if i >= zip64LocatorLen && binary.LittleEndian.Uint32(tail[i-zip64LocatorLen:]) == zip64LocatorSignature {
			if c, ok := zip64DeclaredEntryCount(f, tail[i-zip64LocatorLen+zip64LocatorEOCDOff:]); ok {
				count = c
			}
		}

		// Sanity-check the record the way archive/zip does before trusting
		// anything from it, so junk inside a comment cannot smuggle in a
		// count: the comment must span the rest of the tail, and the
		// directory it describes must fit inside the file.
		dirEnd := info.Size() - tailLen + int64(i)
		dirSize := int64(binary.LittleEndian.Uint32(tail[i+12:]))

		if binary.LittleEndian.Uint16(tail[i+zipEOCDCommentLen:]) != uint16(len(tail)-i-zipEOCDLen) ||
			dirSize > dirEnd || dirEnd-dirSize+int64(binary.LittleEndian.Uint32(tail[i+16:])) >= info.Size() {
			return 0, false
		}

		return count, true
	}

	return 0, false
}

// zip64DeclaredEntryCount reads the 64-bit total entry count from the zip64
// EOCD record at the offset carried in locatorOff (an 8-byte little-endian
// slice taken from a zip64 locator). ok is false when the offset is out of
// range or the record there is not a zip64 EOCD.
func zip64DeclaredEntryCount(f *os.File, locatorOff []byte) (int64, bool) {
	off := binary.LittleEndian.Uint64(locatorOff)
	if off > uint64(math.MaxInt64) {
		return 0, false
	}

	rec := make([]byte, zip64EOCDLen)

	if _, err := f.ReadAt(rec, int64(off)); err != nil {
		return 0, false
	}

	if binary.LittleEndian.Uint32(rec) != zip64EOCDSignature {
		return 0, false
	}

	dirSize := binary.LittleEndian.Uint64(rec[zip64EOCDDirSize:])
	dirOffset := binary.LittleEndian.Uint64(rec[zip64EOCDDirOffset:])

	if dirSize > math.MaxInt64 || dirOffset > math.MaxInt64 {
		return 0, false
	}

	return int64(binary.LittleEndian.Uint64(rec[zip64EOCDTotalEntries:])), true
}

// DefaultMaxExtractedBytes bounds the size of the extracted executable when
// no limit is configured with [WithMaxExtractedBytes]. It is enforced while
// streaming decompressed data, so a decompression bomb is aborted early
// instead of filling the disk. What the limit covers is format-dependent:
// tar streams bound the whole decompressed archive, zip bounds the selected
// member.
const DefaultMaxExtractedBytes int64 = 512 << 20 // 512 MiB

// extractBinary locates the executable inside the downloaded asset at
// archivePath and writes it to dstPath. The archive format is derived from
// assetName's extension (zip, tar.gz/tgz, tar, single-member gz, or a raw
// binary); a recognized but unsupported format fails with
// [ErrUnsupportedArchive] before the archive is opened.
//
// The executable is located by case-insensitive base-name comparison
// against the executable name derived from appName and goos, at any archive
// depth; several members differing only in letter case are an ambiguity
// error. Zip member paths that use backslash separators are normalized
// before the comparison. Member names are only ever compared, never used to
// construct output paths, so path traversal in archive entries ("zip slip")
// cannot escape dstPath.
// Directories are skipped; a non-regular entry (symlink, device) whose base
// name matches the executable disqualifies the archive — bytes are never
// extracted through a link. Several qualifying members are an ambiguity
// error, never an arbitrary pick. When no member matches and the archive
// contains exactly one regular file, that file wins.
//
// The extracted byte count is bounded by maxExtracted, enforced while
// streaming decompressed data. What the bound covers is format-dependent by
// nature: a tar stream is sequential and must be read through to its end,
// so the whole decompressed archive counts against the limit, while zip is
// randomly accessible and only the selected member's decompressed bytes
// count.
func extractBinary(archivePath, assetName, appName, goos, dstPath string, maxExtracted int64) error {
	kind, err := classifyAssetName(assetName)
	if err != nil {
		return err
	}

	executable := executableName(appName, goos)

	switch kind {
	case kindZip:
		return extractFromZip(archivePath, executable, dstPath, maxExtracted)

	case kindTarGz, kindTar:
		f, err := os.Open(archivePath)
		if err != nil {
			return fmt.Errorf("go4updates/selfupdate: opening archive: %w", err)
		}

		defer func() { _ = f.Close() }()

		var src io.Reader = f

		if kind == kindTarGz {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return fmt.Errorf("go4updates/selfupdate: reading gzip stream: %w", err)
			}

			defer func() { _ = gz.Close() }()

			src = gz
		}

		bounded := &boundedReader{r: src, maxBytes: maxExtracted}

		return extractFromTar(bounded, executable, dstPath)

	case kindGz:
		return extractGzipMember(archivePath, dstPath, maxExtracted)

	default: // kindRaw: the asset itself is the executable
		f, err := os.Open(archivePath)
		if err != nil {
			return fmt.Errorf("go4updates/selfupdate: opening asset: %w", err)
		}

		defer func() { _ = f.Close() }()

		bounded := &boundedReader{r: f, maxBytes: maxExtracted}

		return streamToFile(dstPath, bounded)
	}
}

// executableName derives the executable file name to look for inside
// archives from the application name and target operating system.
func executableName(appName, goos string) string {
	if goos == "windows" {
		return appName + ".exe"
	}

	return appName
}

// boundedReader counts bytes read from a decompressed stream and fails with
// [ErrExtractedTooLarge] as soon as the limit is exceeded, aborting the
// decompression instead of filling the disk.
type boundedReader struct {
	r        io.Reader
	maxBytes int64
	n        int64
}

// Read implements io.Reader.
func (b *boundedReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)

	b.n += int64(n)

	if b.n > b.maxBytes {
		return n, fmt.Errorf("%w: decompressed data exceeds %d bytes", ErrExtractedTooLarge, b.maxBytes)
	}

	return n, err
}

// streamToFile creates dstPath (mode 0600; the installer applies the final
// mode) and streams r into it.
func streamToFile(dstPath string, r io.Reader) error {
	f, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: creating staged file: %w", err)
	}

	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()

		_ = os.Remove(dstPath)

		return fmt.Errorf("go4updates/selfupdate: writing staged file: %w", err)
	}

	return f.Close()
}

// zipCandidate is a zip member that qualified for extraction.
type zipCandidate struct {
	file *zip.File
	name string
}

// zipScan is the classification result over the zip member list.
type zipScan struct {
	candidates []zipCandidate

	// regularCount is the number of regular-file members.
	regularCount int

	// disqualified reports that a non-regular member matches the executable
	// name: the archive must not be extracted at all.
	disqualified bool

	// onlyRegular is the single regular member when regularCount == 1, for
	// the single-file fallback.
	onlyRegular *zip.File
}

// scanZip classifies the archive members. Only regular files qualify as the
// executable; a matching non-regular file disqualifies the archive.
func scanZip(files []*zip.File, executable string) zipScan {
	var scan zipScan

	for _, f := range files {
		info := f.FileInfo()

		if info.IsDir() {
			continue
		}

		// Archives authored on Windows separate path elements with
		// backslashes; normalize so the base-name comparison sees them.
		name := strings.ReplaceAll(f.Name, "\\", "/")

		base := path.Base(name)

		if info.Mode()&os.ModeType != 0 {
			// Symlinks, devices, and other special members are never
			// extracted; a matching one disqualifies the archive.
			if strings.EqualFold(base, executable) {
				scan.disqualified = true
			}

			continue
		}

		scan.regularCount++

		switch scan.regularCount {
		case 1:
			scan.onlyRegular = f

		default:
			scan.onlyRegular = nil
		}

		if strings.EqualFold(base, executable) {
			scan.candidates = append(scan.candidates, zipCandidate{file: f, name: name})
		}
	}

	return scan
}

// extractFromZip extracts the executable member from a ZIP archive. Zip
// members are randomly accessible, so selection happens first and the chosen
// member is streamed second.
func extractFromZip(archivePath, executable, dstPath string, maxExtracted int64) error {
	// Reject archives that declare more entries than the limit before
	// archive/zip walks the declared directory. When the declaration cannot
	// be read, OpenReader reports the format problem itself.
	if declared, ok := zipDeclaredEntryCount(archivePath); ok && declared > maxArchiveEntries {
		return fmt.Errorf("%w: archive declares %d entries, limit is %d",
			ErrExtractedTooLarge, declared, maxArchiveEntries)
	}

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: opening zip archive: %w", err)
	}

	defer func() { _ = reader.Close() }()

	if len(reader.File) > maxArchiveEntries {
		return fmt.Errorf("%w: archive contains %d entries, limit is %d",
			ErrExtractedTooLarge, len(reader.File), maxArchiveEntries)
	}

	scan := scanZip(reader.File, executable)

	var chosen zipCandidate

	switch {
	case scan.disqualified:
		return fmt.Errorf("%w: %q matches a non-regular archive member",
			ErrExecutableNotFound, executable)

	case len(scan.candidates) > 1:
		return fmt.Errorf("%w: %s", ErrAmbiguousExecutable, joinQuoted(namesOf(scan.candidates)))

	case len(scan.candidates) == 1:
		chosen = scan.candidates[0]

	case scan.onlyRegular != nil:
		// Single-file fallback: with no name match and exactly one regular
		// file, that file is the executable.
		chosen = zipCandidate{file: scan.onlyRegular, name: scan.onlyRegular.Name}
	}

	if chosen.file == nil {
		return fmt.Errorf("%w: looked for %q among %d regular files",
			ErrExecutableNotFound, executable, scan.regularCount)
	}

	if chosen.file.UncompressedSize64 > uint64(maxExtracted) {
		return fmt.Errorf("%w: member %q declares %d bytes, limit is %d",
			ErrExtractedTooLarge, chosen.name, chosen.file.UncompressedSize64, maxExtracted)
	}

	rc, err := chosen.file.Open()
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: opening archive member %q: %w", chosen.name, err)
	}

	defer func() { _ = rc.Close() }()

	if err := copyBounded(dstPath, rc, maxExtracted); err != nil {
		return fmt.Errorf("go4updates/selfupdate: extracting archive member %q: %w", chosen.name, err)
	}

	return nil
}

// extractGzipMember decompresses a single-member gzip stream into dstPath;
// the decompressed content is the executable itself.
func extractGzipMember(archivePath, dstPath string, maxExtracted int64) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: opening archive: %w", err)
	}

	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: reading gzip stream: %w", err)
	}

	defer func() { _ = gz.Close() }()

	bounded := &boundedReader{r: gz, maxBytes: maxExtracted}

	return streamToFile(dstPath, bounded)
}

// extractFromTar extracts the executable member from a tar stream read from
// r (already wrapped in a boundedReader so the total decompressed byte count
// stays bounded).
//
// Tar streams are sequential, so the scan is single-pass: the first matching
// member is extracted while the scan continues, and a second match aborts
// with an ambiguity error. The first non-matching regular file is spooled to
// a side file so the single-file fallback can still work without buffering
// it in memory or rewinding the stream. On error, no partial output survives
// at dstPath.
func extractFromTar(r io.Reader, executable, dstPath string) error {
	const spoolSuffix = ".fallback"

	spoolPath := dstPath + spoolSuffix

	ok := false

	defer func() {
		if !ok {
			_ = os.Remove(dstPath)

			_ = os.Remove(spoolPath)
		}
	}()

	tr := tar.NewReader(r)

	var (
		matches      []string
		regularCount int
		spoolOpen    bool
	)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return fmt.Errorf("go4updates/selfupdate: reading tar archive: %w", err)
		}

		if hdr.Typeflag == tar.TypeDir {
			continue
		}

		base := path.Base(hdr.Name)

		if hdr.Typeflag != tar.TypeReg {
			// Symlinks, hardlinks, and special members are never extracted;
			// a matching one disqualifies the archive.
			if strings.EqualFold(base, executable) {
				return fmt.Errorf("%w: %q matches a non-regular archive member",
					ErrExecutableNotFound, executable)
			}

			continue
		}

		regularCount++

		if strings.EqualFold(base, executable) {
			matches = append(matches, hdr.Name)

			if len(matches) > 1 {
				return fmt.Errorf("%w: %s", ErrAmbiguousExecutable, joinQuoted(matches))
			}

			if err := streamToFile(dstPath, tr); err != nil {
				return fmt.Errorf("go4updates/selfupdate: extracting archive member %q: %w", hdr.Name, err)
			}

			if spoolOpen {
				_ = os.Remove(spoolPath)

				spoolOpen = false
			}

			continue
		}

		// A non-matching regular file.
		switch {
		case len(matches) > 0:
			// The named candidate already won; a fallback is impossible.

		case regularCount == 1:
			// Might be the archive's only regular file; spool it as the
			// fallback candidate.
			if err := streamToFile(spoolPath, tr); err != nil {
				return fmt.Errorf("go4updates/selfupdate: staging fallback member %q: %w", hdr.Name, err)
			}

			spoolOpen = true

		case spoolOpen:
			// A second regular file while spooling: the single-file
			// fallback is impossible; drop the spool.
			_ = os.Remove(spoolPath)

			spoolOpen = false
		}
	}

	switch {
	case len(matches) == 1:
		ok = true

		return nil

	case spoolOpen:
		if err := os.Rename(spoolPath, dstPath); err != nil {
			return fmt.Errorf("go4updates/selfupdate: publishing extracted executable: %w", err)
		}

		ok = true

		return nil

	default:
		return fmt.Errorf("%w: looked for %q among %d regular files",
			ErrExecutableNotFound, executable, regularCount)
	}
}

// copyBounded streams r into dstPath through a [boundedReader], failing
// with [ErrExtractedTooLarge] once more than maxExtracted bytes are read.
// The counting reader is used directly instead of an io.LimitReader(max+1)
// wrapper, which would overflow for a maxExtracted of math.MaxInt64 and
// silently truncate the member to zero bytes.
func copyBounded(dstPath string, r io.Reader, maxExtracted int64) error {
	return streamToFile(dstPath, &boundedReader{r: r, maxBytes: maxExtracted})
}

// namesOf lists candidate names for error messages.
func namesOf(candidates []zipCandidate) []string {
	names := make([]string, 0, len(candidates))

	for _, c := range candidates {
		names = append(names, c.name)
	}

	return names
}

// joinQuoted renders names as a comma-separated quoted list.
func joinQuoted(names []string) string {
	parts := make([]string, 0, len(names))

	for _, n := range names {
		parts = append(parts, strconv.Quote(n))
	}

	return strings.Join(parts, ", ")
}
