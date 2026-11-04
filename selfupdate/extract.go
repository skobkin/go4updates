package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
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
		dirSize := int64(binary.LittleEndian.Uint32(tail[i+12:]))
		dirOffset := int64(binary.LittleEndian.Uint32(tail[i+16:]))

		// A zip64 locator directly precedes the EOCD and points at a zip64
		// EOCD record whose 64-bit fields supersede the 32-bit ones — the
		// count and the directory placement alike, so real zip64 archives
		// (whose 32-bit fields carry 0xffffffff placeholders) are still
		// sanity-checked against their true layout.
		if i >= zip64LocatorLen && binary.LittleEndian.Uint32(tail[i-zip64LocatorLen:]) == zip64LocatorSignature {
			if c, s, o, ok := zip64EOCDFields(f, tail[i-zip64LocatorLen+zip64LocatorEOCDOff:]); ok {
				count, dirSize, dirOffset = c, s, o
			}
		}

		// Sanity-check the record the way archive/zip does before trusting
		// anything from it, so junk inside a comment cannot smuggle in a
		// count: the comment must span the rest of the tail, and the central
		// directory must start at a non-negative offset once the
		// prepended-data base is derived the way archive/zip derives it
		// (directory end − directory size − directory offset).
		dirEnd := info.Size() - tailLen + int64(i)

		if binary.LittleEndian.Uint16(tail[i+zipEOCDCommentLen:]) != uint16(len(tail)-i-zipEOCDLen) ||
			dirEnd-dirSize-dirOffset < 0 {
			return 0, false
		}

		return count, true
	}

	return 0, false
}

// zip64EOCDFields reads the 64-bit total entry count, central-directory
// size, and central-directory offset from the zip64 EOCD record at the
// offset carried in locatorOff (an 8-byte little-endian slice taken from a
// zip64 locator). ok is false when the offset is out of range, the record
// there is not a zip64 EOCD, or any field exceeds math.MaxInt64 — a
// declared count of 2^63 and above must not survive the int64 conversion
// as a negative value that slips past the caller's guard comparison.
func zip64EOCDFields(f *os.File, locatorOff []byte) (count, dirSize, dirOffset int64, ok bool) {
	off := binary.LittleEndian.Uint64(locatorOff)
	if off > uint64(math.MaxInt64) {
		return 0, 0, 0, false
	}

	rec := make([]byte, zip64EOCDLen)

	if _, err := f.ReadAt(rec, int64(off)); err != nil {
		return 0, 0, 0, false
	}

	if binary.LittleEndian.Uint32(rec) != zip64EOCDSignature {
		return 0, 0, 0, false
	}

	countU := binary.LittleEndian.Uint64(rec[zip64EOCDTotalEntries:])
	sizeU := binary.LittleEndian.Uint64(rec[zip64EOCDDirSize:])
	offsetU := binary.LittleEndian.Uint64(rec[zip64EOCDDirOffset:])

	if countU > math.MaxInt64 || sizeU > math.MaxInt64 || offsetU > math.MaxInt64 {
		return 0, 0, 0, false
	}

	return int64(countU), int64(sizeU), int64(offsetU), true
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
// error, never an arbitrary pick. A member whose base name does not match
// is never installed: there is no fallback that guesses among regular
// files, so an archive without a name-matched executable fails with
// [ErrExecutableNotFound] instead of silently installing a README.
//
// The extracted byte count is bounded by maxExtracted, enforced while
// streaming decompressed data. What the bound covers is format-dependent by
// nature: a tar stream is sequential and must be read through to its end,
// so the whole decompressed archive counts against the limit, while zip is
// randomly accessible and only the selected member's decompressed bytes
// count.
//
// The context bounds the streaming: a cancellation between archive reads
// aborts extraction with an error wrapping the context error, and no
// partial output survives at dstPath. Byte observations of the streamed
// executable are reported to onBytes when it is not nil; total is zero for
// formats that do not declare the member size.
func extractBinary(ctx context.Context, archivePath, assetName, appName, goos, dstPath string, maxExtracted int64, onBytes progressFunc) error {
	kind, err := classifyAssetName(assetName)
	if err != nil {
		return err
	}

	executable := executableName(appName, goos)

	switch kind {
	case kindZip:
		err = extractFromZip(ctx, archivePath, executable, dstPath, maxExtracted, onBytes)

	case kindTarGz, kindTar:
		err = extractTarArchive(ctx, archivePath, kind == kindTarGz, executable, dstPath, maxExtracted, onBytes)

	case kindGz:
		err = extractGzipMember(ctx, archivePath, dstPath, maxExtracted, onBytes)

	default: // kindRaw: the asset itself is the executable
		err = extractRawAsset(ctx, archivePath, dstPath, maxExtracted, onBytes)
	}

	return wrapExtractCancel(err)
}

// wrapExtractCancel labels context errors surfacing from the streaming
// helpers so a cancellation is not misread as a plain read or write
// failure. The context error stays in the chain for errors.Is.
func wrapExtractCancel(err error) error {
	if err == nil ||
		(!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
		return err
	}

	return fmt.Errorf("go4updates/selfupdate: extraction canceled: %w", err)
}

// ctxReader aborts a stream when ctx is canceled: Read reports the context
// error instead of pulling more bytes through the decompressor, so large
// sequential streams stop promptly instead of running to completion.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

// Read implements io.Reader.
func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}

	return c.r.Read(p)
}

// extractTarArchive opens the tar (optionally gzip-compressed) archive at
// archivePath and extracts the executable member from it.
func extractTarArchive(ctx context.Context, archivePath string, gzipped bool, executable, dstPath string, maxExtracted int64, onBytes progressFunc) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: opening archive: %w", err)
	}

	defer func() { _ = f.Close() }()

	var src io.Reader = f

	if gzipped {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("go4updates/selfupdate: reading gzip stream: %w", err)
		}

		defer func() { _ = gz.Close() }()

		src = gz
	}

	bounded := &boundedReader{r: &ctxReader{ctx: ctx, r: src}, maxBytes: maxExtracted}

	return extractFromTar(ctx, bounded, executable, dstPath, onBytes)
}

// extractRawAsset streams the asset at archivePath itself into dstPath; the
// asset is the executable, no archive unwrapping happens.
func extractRawAsset(ctx context.Context, archivePath, dstPath string, maxExtracted int64, onBytes progressFunc) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: opening asset: %w", err)
	}

	defer func() { _ = f.Close() }()

	bounded := &boundedReader{r: &ctxReader{ctx: ctx, r: f}, maxBytes: maxExtracted}

	return streamToFile(dstPath, bounded, onBytes, 0)
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
// mode) and streams r into it, reporting copied bytes to onBytes when it is
// not nil. total is the declared member size when the format declares one,
// and zero when it does not.
func streamToFile(dstPath string, r io.Reader, onBytes progressFunc, total int64) error {
	f, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("go4updates/selfupdate: creating staged file: %w", err)
	}

	var sink io.Writer = f

	if onBytes != nil {
		sink = &progressWriter{w: f, onBytes: onBytes, total: total}
	}

	if _, err := io.Copy(sink, r); err != nil {
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

	// regularCount is the number of regular-file members, reported in the
	// not-found error so a missing executable is distinguishable from an
	// empty archive.
	regularCount int

	// disqualified reports that a non-regular member matches the executable
	// name: the archive must not be extracted at all.
	disqualified bool
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

		if strings.EqualFold(base, executable) {
			scan.candidates = append(scan.candidates, zipCandidate{file: f, name: name})
		}
	}

	return scan
}

// extractFromZip extracts the executable member from a ZIP archive. Zip
// members are randomly accessible, so selection happens first and the chosen
// member is streamed second. The context bounds the member streaming; byte
// observations carry the member's declared UncompressedSize64 as total.
func extractFromZip(ctx context.Context, archivePath, executable, dstPath string, maxExtracted int64, onBytes progressFunc) error {
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

	if err := copyBounded(dstPath, &ctxReader{ctx: ctx, r: rc}, maxExtracted, onBytes, int64(chosen.file.UncompressedSize64)); err != nil {
		return fmt.Errorf("go4updates/selfupdate: extracting archive member %q: %w", chosen.name, err)
	}

	return nil
}

// extractGzipMember decompresses a single-member gzip stream into dstPath;
// the decompressed content is the executable itself. The context bounds the
// decompression; the format declares no upfront member size, so byte
// observations carry a zero total.
func extractGzipMember(ctx context.Context, archivePath, dstPath string, maxExtracted int64, onBytes progressFunc) error {
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

	bounded := &boundedReader{r: &ctxReader{ctx: ctx, r: gz}, maxBytes: maxExtracted}

	return streamToFile(dstPath, bounded, onBytes, 0)
}

// extractFromTar extracts the executable member from a tar stream read from
// r (already wrapped in a boundedReader so the total decompressed byte count
// stays bounded).
//
// Tar streams are sequential, so the scan is single-pass: the first matching
// member is extracted while the scan continues, and a second match aborts
// with an ambiguity error. Members whose base name does not match are never
// installed — there is no fallback that guesses among regular files, so an
// archive without a name-matched executable fails with
// [ErrExecutableNotFound]. On error, no partial output survives at dstPath.
// The context is checked between archive entries; mid-entry cancellation
// surfaces through the stream reader. Byte observations of the matched
// member carry the header's declared size as total.
func extractFromTar(ctx context.Context, r io.Reader, executable, dstPath string, onBytes progressFunc) error {
	ok := false

	defer func() {
		if !ok {
			_ = os.Remove(dstPath)
		}
	}()

	tr := tar.NewReader(r)

	var (
		matches      []string
		regularCount int
		entries      int
	)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return fmt.Errorf("go4updates/selfupdate: reading tar archive: %w", err)
		}

		// Every entry — regular files, directories, and special members —
		// counts against the same limit the zip path enforces: thousands of
		// entries are a resource-exhaustion pattern, not a release layout.
		entries++

		if entries > maxArchiveEntries {
			return fmt.Errorf("%w: archive contains %d entries, limit is %d",
				ErrExtractedTooLarge, entries, maxArchiveEntries)
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

		if !strings.EqualFold(base, executable) {
			// A non-matching regular file: never a fallback candidate.
			continue
		}

		matches = append(matches, hdr.Name)

		if len(matches) > 1 {
			return fmt.Errorf("%w: %s", ErrAmbiguousExecutable, joinQuoted(matches))
		}

		if err := streamToFile(dstPath, tr, onBytes, hdr.Size); err != nil {
			return fmt.Errorf("go4updates/selfupdate: extracting archive member %q: %w", hdr.Name, err)
		}
	}

	if len(matches) == 1 {
		ok = true

		return nil
	}

	return fmt.Errorf("%w: looked for %q among %d regular files",
		ErrExecutableNotFound, executable, regularCount)
}

// copyBounded streams r into dstPath through a [boundedReader], failing
// with [ErrExtractedTooLarge] once more than maxExtracted bytes are read.
// The counting reader is used directly instead of an io.LimitReader(max+1)
// wrapper, which would overflow for a maxExtracted of math.MaxInt64 and
// silently truncate the member to zero bytes. Byte observations are
// reported to onBytes with the declared total when it is not nil.
func copyBounded(dstPath string, r io.Reader, maxExtracted int64, onBytes progressFunc, total int64) error {
	return streamToFile(dstPath, &boundedReader{r: r, maxBytes: maxExtracted}, onBytes, total)
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
