package releasefmt

import "strings"

// shortCommitSHALength is the number of characters a commit hash is displayed
// with when Options.ShortCommitSHA is enabled.
const shortCommitSHALength = 10

// commitSHAHexLength is the length of a full hexadecimal commit hash.
const commitSHAHexLength = 40

// issueRefBlockers lists the bytes that may not directly precede a "#123"
// reference, so that paths, URLs, and other references are left alone.
const issueRefBlockers = "/:\\#"

// Options configures MarkdownBody and Changelog.
type Options struct {
	// Linker resolves issue references, commit hashes, and user mentions to
	// provider URLs. A nil Linker disables linkification entirely.
	Linker Linker

	// ShortCommitSHA displays commit hashes with their first ten characters.
	// Without a Linker hashes are still shortened, they are just not linked.
	ShortCommitSHA bool
}

// MarkdownBody applies presentation-oriented transformations to raw Markdown.
//
// Full commit hashes, "#123" issue references, and "@user" mentions are
// rewritten into links through opts.Linker; a hash is shortened to its first
// ten characters when opts.ShortCommitSHA is set, with or without a Linker.
// Everything else is left as it is: fenced code blocks, inline code spans,
// links, images, autolinks, and link reference definitions are never
// modified, so the raw document structure survives. Links and images include
// the inline "[text](destination)" form as well as the reference forms
// "[text][label]", "[label][]", and "[label]" for labels the document
// defines.
//
// Applying MarkdownBody to its own output changes nothing.
func MarkdownBody(body string, opts Options) string {
	if body == "" {
		return ""
	}

	lines := strings.Split(body, "\n")

	labels := definedLinkLabels(lines)

	fenced := false

	// open holds the state of the fenced code block the document currently
	// carries, so only the marker that opened the block can close it.
	var open fence

	for i, line := range lines {
		if fenced {
			if closesFence(line, open) {
				fenced = false
				open = fence{}
			}

			continue
		}

		if opened, ok := openFence(line); ok {
			fenced = true
			open = opened

			continue
		}

		lines[i] = transformLine(line, labels, opts)
	}

	return strings.Join(lines, "\n")
}

// fence is the state of the fenced code block a document currently holds.
type fence struct {
	// marker is the character the block was opened with.
	marker byte

	// length is the number of marker characters the opening fence used.
	length int
}

// openFence reports that line opens a fenced code block, returning the state
// of the opened block.
//
// A fence is an up to three space indented run of at least three backticks or
// tildes, optionally followed by an info string. An info string of a backtick
// fence may not contain backticks; such a line is content, not a fence.
func openFence(line string) (fence, bool) {
	marker, length, rest, ok := fenceRun(line)
	if !ok {
		return fence{}, false
	}

	if marker == '`' && strings.IndexByte(rest, '`') >= 0 {
		return fence{}, false
	}

	return fence{marker: marker, length: length}, true
}

// closesFence reports whether line closes the fenced code block open holds.
//
// A closing fence uses the same marker character as the opening one, uses at
// least as many of them, and carries nothing but whitespace after the run.
func closesFence(line string, open fence) bool {
	marker, length, rest, ok := fenceRun(line)
	if !ok {
		return false
	}

	return marker == open.marker && length >= open.length && strings.TrimLeft(rest, " \t") == ""
}

// fenceRun returns the marker character, the marker run length, and the rest
// of the line for a fence candidate. The boolean result reports that line
// starts with a run of at least three backticks or tildes.
func fenceRun(line string) (byte, int, string, bool) {
	i := 0

	for i < len(line) && i < 3 && (line[i] == ' ' || line[i] == '\t') {
		i++
	}

	if i >= len(line) {
		return 0, 0, "", false
	}

	marker := line[i]
	if marker != '`' && marker != '~' {
		return 0, 0, "", false
	}

	n := 0

	for i+n < len(line) && line[i+n] == marker {
		n++
	}

	if n < 3 {
		return 0, 0, "", false
	}

	return marker, n, line[i+n:], true
}

// transformLine rewrites the transformable parts of a single Markdown line.
// labels holds the normalized labels of the document's link reference
// definitions, so shortcut references can be recognized as links.
func transformLine(line string, labels map[string]struct{}, opts Options) string {
	if isLinkRefDef(line) {
		return line
	}

	spans := protectedSpans(line, labels)
	if len(spans) == 0 {
		return transformSegment(line, opts)
	}

	var out strings.Builder

	start := 0

	for _, s := range spans {
		if s.start > start {
			out.WriteString(transformSegment(line[start:s.start], opts))
		}

		out.WriteString(line[s.start:s.end])

		start = s.end
	}

	if start < len(line) {
		out.WriteString(transformSegment(line[start:], opts))
	}

	return out.String()
}

// transformSegment rewrites commit hashes, issue references, and user
// mentions in one run of unprotected text.
func transformSegment(text string, opts Options) string {
	var out strings.Builder

	i := 0

	for i < len(text) {
		replacement, end := matchReference(text, i, opts)
		if end > i {
			out.WriteString(replacement)

			i = end

			continue
		}

		out.WriteByte(text[i])

		i++
	}

	return out.String()
}

// matchReference looks for a commit hash, an issue reference, or a user
// mention at position i of text. It returns the replacement for the match and
// the position just after it, or an empty replacement when nothing matches.
func matchReference(text string, i int, opts Options) (string, int) {
	switch text[i] {
	case '#':
		if end := issueRefEnd(text, i); end > i {
			linkURL, ok := resolveIssueURL(opts, text[i+1:end])

			return linkedLabel("#"+text[i+1:end], linkURL, ok), end
		}

	case '@':
		if end := mentionEnd(text, i); end > i {
			linkURL, ok := resolveMentionURL(opts, text[i+1:end])

			return linkedLabel("@"+text[i+1:end], linkURL, ok), end
		}
	default:
		if end := commitSHAEnd(text, i); end > i {
			return commitDisplay(text[i:end], opts), end
		}
	}

	return "", i
}

// linkedLabel turns a reference label into a Markdown link when the resolver
// produced a URL for it, and returns the label unchanged otherwise.
func linkedLabel(label, linkURL string, ok bool) string {
	if !ok {
		return label
	}

	return "[" + label + "](" + linkURL + ")"
}

// commitDisplay renders a full commit hash, linked when the options carry a
// commit resolver.
func commitDisplay(sha string, opts Options) string {
	display := sha
	if opts.ShortCommitSHA {
		display = sha[:shortCommitSHALength]
	}

	linkURL, ok := resolveCommitURL(opts, sha)
	if !ok {
		return display
	}

	return "[" + display + "](" + linkURL + ")"
}

// resolveCommitURL resolves sha through opts.Linker, reporting false when no
// linker is configured.
func resolveCommitURL(opts Options, sha string) (string, bool) {
	if opts.Linker == nil {
		return "", false
	}

	return opts.Linker.CommitURL(sha)
}

// resolveIssueURL resolves number through opts.Linker, reporting false when
// no linker is configured.
func resolveIssueURL(opts Options, number string) (string, bool) {
	if opts.Linker == nil {
		return "", false
	}

	return opts.Linker.IssueURL(number)
}

// resolveMentionURL resolves username through opts.Linker, reporting false
// when no linker is configured.
func resolveMentionURL(opts Options, username string) (string, bool) {
	if opts.Linker == nil {
		return "", false
	}

	return opts.Linker.MentionURL(username)
}

// issueRefEnd returns the end of a "#123" style issue reference starting at
// i, or i when there is no reference at that position.
//
// References preceded by "/", ":", "\", or another "#" are not recognized, and
// neither is a reference whose digits start a full commit hash, so that hashes
// are never turned into issue links.
func issueRefEnd(text string, i int) int {
	if i > 0 && strings.IndexByte(issueRefBlockers, text[i-1]) >= 0 {
		return i
	}

	end := i + 1

	for end < len(text) && isDigit(text[end]) {
		end++
	}

	if end == i+1 {
		return i
	}

	if commitSHAEnd(text, i+1) > i+1 {
		return i
	}

	return end
}

// mentionEnd returns the end of an "@user" mention starting at i, or i when
// there is no mention at that position.
//
// Mentions are only recognized at the start of a line or after whitespace, so
// e-mail addresses are never mistaken for them. A trailing "." closes the
// sentence rather than ending the name.
func mentionEnd(text string, i int) int {
	if i > 0 && !isSpaceByte(text[i-1]) {
		return i
	}

	end := i + 1

	for end < len(text) && isMentionByte(text[end]) {
		end++
	}

	for end > i+1 && text[end-1] == '.' {
		end--
	}

	if end == i+1 {
		return i
	}

	return end
}

// commitSHAEnd returns the end of a full commit hash starting at i, or i when
// there is no hash at that position.
//
// A hash must be surrounded by non-word characters, which keeps longer
// hexadecimal sequences and identifiers containing a hash untouched.
func commitSHAEnd(text string, i int) int {
	if i+commitSHAHexLength > len(text) {
		return i
	}

	if i > 0 && isWordByte(text[i-1]) {
		return i
	}

	if !isCommitSHA(text[i : i+commitSHAHexLength]) {
		return i
	}

	if isWordAt(text, i+commitSHAHexLength) {
		return i
	}

	return i + commitSHAHexLength
}

// span is a half-open byte range of a line that must not be transformed.
type span struct {
	start int
	end   int
}

// protectedSpans returns the ranges of line that hold existing Markdown
// constructs, in order. labels holds the normalized labels of the document's
// link reference definitions.
func protectedSpans(line string, labels map[string]struct{}) []span {
	var spans []span

	i := 0

	for i < len(line) {
		next, found := protectedSpanAt(line, i, labels)
		if !found {
			i++

			continue
		}

		spans = append(spans, next)

		i = next.end
	}

	return spans
}

// protectedSpanAt returns the protected construct starting at i, if any.
func protectedSpanAt(line string, i int, labels map[string]struct{}) (span, bool) {
	switch line[i] {
	case '`':
		if end := codeSpanEnd(line, i); end > i {
			return span{start: i, end: end}, true
		}

	case '<':
		if end := autolinkEnd(line, i); end > i {
			return span{start: i, end: end}, true
		}

	case '[':
		if end, ok := linkSpan(line, i, labels); ok {
			return span{start: i, end: end}, true
		}

	case '!':
		if i+1 < len(line) && line[i+1] == '[' {
			if end, ok := linkSpan(line, i+1, labels); ok {
				return span{start: i, end: end}, true
			}
		}
	}

	return span{}, false
}

// linkSpan returns the end of the link or image construct whose opening
// bracket is at i, and whether the line holds one.
//
// Inline links "[text](destination)" and the reference forms "[text][label]"
// and "[label][]" are protected as a whole, so a label or a reference text
// holding references is never rewritten inside them. A shortcut reference
// "[label]" is protected when the document defines the label; an undefined
// one is plain text.
func linkSpan(line string, i int, labels map[string]struct{}) (int, bool) {
	closing, ok := bracketEnd(line, i)
	if !ok {
		return i, false
	}

	if closing+1 < len(line) {
		switch line[closing+1] {
		case '(':
			return destinationEnd(line, closing+1)

		case '[':
			if end, ok := bracketEnd(line, closing+1); ok {
				return end + 1, true
			}

			return i, false
		}
	}

	if _, ok := labels[normalizeLabel(line[i+1:closing])]; ok {
		return closing + 1, true
	}

	return i, false
}

// destinationEnd returns the end of the parenthesized destination that opens
// at i, and whether it is closed on the same line.
func destinationEnd(line string, i int) (int, bool) {
	depth := 1

	for j := i + 1; j < len(line); j++ {
		switch line[j] {
		case '(':
			depth++

		case ')':
			depth--
		}

		if depth == 0 {
			return j + 1, true
		}
	}

	return i, false
}

// codeSpanEnd returns the end of the inline code span whose backtick run
// opens at i, or i when the span is not closed on the same line.
func codeSpanEnd(line string, i int) int {
	open := backtickRun(line, i)

	closing := i + open

	for closing < len(line) {
		if line[closing] != '`' {
			closing++

			continue
		}

		run := backtickRun(line, closing)
		if run == open {
			return closing + run
		}

		closing += run
	}

	return i
}

// backtickRun returns the length of the backtick run starting at i.
func backtickRun(line string, i int) int {
	n := 0

	for i+n < len(line) && line[i+n] == '`' {
		n++
	}

	return n
}

// autolinkEnd returns the end of an angle bracket autolink or inline tag
// starting at i, or i when the brackets are not closed on the same line
// without whitespace in between.
func autolinkEnd(line string, i int) int {
	for j := i + 1; j < len(line); j++ {
		switch line[j] {
		case '>':
			return j + 1

		case '<', ' ', '\t':
			return i
		}
	}

	return i
}

// bracketEnd returns the position of the "]" closing the bracket opened at i,
// and whether it is present on the same line.
func bracketEnd(line string, i int) (int, bool) {
	depth := 1

	for j := i + 1; j < len(line); j++ {
		switch line[j] {
		case '[':
			depth++

		case ']':
			depth--
		}

		if depth == 0 {
			return j, true
		}
	}

	return i, false
}

// isLinkRefDef reports whether line is a Markdown link reference definition
// such as "[1]: https://example.com".
func isLinkRefDef(line string) bool {
	_, ok := linkRefDefLabel(line)

	return ok
}

// linkRefDefLabel returns the label of the link reference definition line
// holds, and whether line is one. The label is returned as written.
func linkRefDefLabel(line string) (string, bool) {
	i := 0

	for i < len(line) && i < 3 && (line[i] == ' ' || line[i] == '\t') {
		i++
	}

	if i >= len(line) || line[i] != '[' {
		return "", false
	}

	closing := strings.IndexByte(line[i:], ']')
	if closing < 0 {
		return "", false
	}

	rest, found := strings.CutPrefix(line[i+closing+1:], ":")
	if !found {
		return "", false
	}

	if strings.TrimLeft(rest, " \t") == "" {
		return "", false
	}

	return line[i+1 : i+closing], true
}

// definedLinkLabels collects the labels of the link reference definitions a
// document carries, normalized for comparison.
func definedLinkLabels(lines []string) map[string]struct{} {
	labels := make(map[string]struct{})

	for _, line := range lines {
		if label, ok := linkRefDefLabel(line); ok {
			labels[normalizeLabel(label)] = struct{}{}
		}
	}

	return labels
}

// normalizeLabel normalizes a link label for comparison the way Markdown
// renderers compare them: case folded, with whitespace runs collapsed.
func normalizeLabel(label string) string {
	return strings.Join(strings.Fields(strings.ToLower(label)), " ")
}

// isDigit reports whether b is an ASCII decimal digit.
func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// isHexDigit reports whether b is an ASCII hexadecimal digit.
func isHexDigit(b byte) bool {
	return isDigit(b) || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// isMentionByte reports whether b may appear inside a user or group name.
func isMentionByte(b byte) bool {
	return isWordByte(b) || b == '-' || b == '.'
}

// isWordByte reports whether b is an ASCII word character.
func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || isDigit(b)
}

// isSpaceByte reports whether b is ASCII whitespace.
func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

// isWordAt reports whether the byte at position i of text is a word
// character. The end of text counts as a non-word character.
func isWordAt(text string, i int) bool {
	return i < len(text) && isWordByte(text[i])
}
