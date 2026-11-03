package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

// maxArchiveEntries caps how many entries an archive may contain. Executable
// archives ship a handful of files; thousands of entries are a
// resource-exhaustion attack, not a release layout.
const maxArchiveEntries = 1024

// DefaultMaxExtractedBytes bounds the size of the extracted executable when
// no limit is configured with [WithMaxExtractedBytes]. It is enforced while
// streaming decompressed data, so a decompression bomb is aborted early
// instead of filling the disk.
const DefaultMaxExtractedBytes int64 = 512 << 20 // 512 MiB

// extractBinary locates the executable inside the downloaded asset at
// archivePath and writes it to dstPath. The archive format is derived from
// assetName's extension (zip, tar.gz/tgz, tar, single-member gz, or a raw
// binary); a recognized but unsupported format fails with
// [ErrUnsupportedArchive] before the archive is opened.
//
// The executable is located by base-name comparison against the executable
// name derived from appName and goos, at any archive depth. Member names are
// only ever compared, never used to construct output paths, so path
// traversal in archive entries ("zip slip") cannot escape dstPath.
// Directories are skipped; a non-regular entry (symlink, device) whose base
// name matches the executable disqualifies the archive — bytes are never
// extracted through a link. Several qualifying members are an ambiguity
// error, never an arbitrary pick. When no member matches and the archive
// contains exactly one regular file, that file wins.
//
// The extracted byte count is bounded by maxExtracted, enforced while
// streaming decompressed data.
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

		base := path.Base(f.Name)

		if info.Mode()&os.ModeType != 0 {
			// Symlinks, devices, and other special members are never
			// extracted; a matching one disqualifies the archive.
			if base == executable {
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

		if base == executable {
			scan.candidates = append(scan.candidates, zipCandidate{file: f, name: f.Name})
		}
	}

	return scan
}

// extractFromZip extracts the executable member from a ZIP archive. Zip
// members are randomly accessible, so selection happens first and the chosen
// member is streamed second.
func extractFromZip(archivePath, executable, dstPath string, maxExtracted int64) error {
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
			if base == executable {
				return fmt.Errorf("%w: %q matches a non-regular archive member",
					ErrExecutableNotFound, executable)
			}

			continue
		}

		regularCount++

		if base == executable {
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

// copyBounded streams r into dstPath, failing with [ErrExtractedTooLarge]
// once more than maxExtracted bytes are read.
func copyBounded(dstPath string, r io.Reader, maxExtracted int64) error {
	bounded := &byteCounter{r: io.LimitReader(r, maxExtracted+1)}

	if err := streamToFile(dstPath, bounded); err != nil {
		return err
	}

	if bounded.n > maxExtracted {
		return fmt.Errorf("%w: member exceeds %d bytes", ErrExtractedTooLarge, maxExtracted)
	}

	return nil
}

// byteCounter counts the bytes read through it.
type byteCounter struct {
	r io.Reader
	n int64
}

// Read implements io.Reader.
func (c *byteCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)

	c.n += int64(n)

	return n, err
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
