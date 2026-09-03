// Package markdown renders Markdown as a Fyne [widget.RichText].
//
// [NewRichText] parses a Markdown document and rewrites every image it finds
// into a lazily loaded segment: the image is displayed as a placeholder
// immediately and fetched in the background only when the rendering policy
// allows it.
//
// Remote images are opt-in, bounded, and restricted to http and https:
//
//	rich := markdown.NewRichText(body, markdown.Options{
//		RemoteImages: true,
//	})
//
// With the zero-value options no network or filesystem access happens at all,
// and image locations outside http and https are never read even when remote
// images are enabled. There is no fallback to arbitrary storage URIs.
package markdown
