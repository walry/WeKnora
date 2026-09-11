package docparser

import (
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/docreader/proto"
	"github.com/Tencent/WeKnora/internal/types"
)

// pageSpansFromProto converts docreader gRPC page spans to the
// transport-agnostic form. Returns nil when the connected docreader predates
// page tracking: chunk pages then stay unknown (0) and the UI hides them,
// rather than the parse failing.
func pageSpansFromProto(spans []*proto.PageSpan) []types.PageSpan {
	if len(spans) == 0 {
		return nil
	}
	out := make([]types.PageSpan, 0, len(spans))
	for _, s := range spans {
		out = append(out, types.PageSpan{
			Start: int(s.GetStart()),
			End:   int(s.GetEnd()),
			Page:  int(s.GetPage()),
			Label: s.GetLabel(),
		})
	}
	return out
}

// pageSpansFromHTTP mirrors pageSpansFromProto for the JSON transport.
func pageSpansFromHTTP(spans []httpPageSpan) []types.PageSpan {
	if len(spans) == 0 {
		return nil
	}
	out := make([]types.PageSpan, 0, len(spans))
	for _, s := range spans {
		out = append(out, types.PageSpan{
			Start: s.Start,
			End:   s.End,
			Page:  s.Page,
			Label: s.Label,
		})
	}
	return out
}

// shiftPageSpans moves every span by delta runes. Post-processing steps that
// prepend a fixed prefix to the markdown (e.g. an injected original-image
// reference) shift every offset uniformly; without this the spans would point
// into the wrong part of the document.
func shiftPageSpans(spans []types.PageSpan, delta int) []types.PageSpan {
	if delta == 0 || len(spans) == 0 {
		return spans
	}
	for i := range spans {
		spans[i].Start += delta
		spans[i].End += delta
	}
	return spans
}

// joinPageSpans concatenates per-page Markdown blocks with sep and returns the
// joined text plus one span per block, numbered from page 1 in slice order.
// Empty blocks are skipped but do not shift the numbering, so block index i
// always means page i+1 of the source document.
//
// Offsets are rune counts (matching types.PageSpan's contract) and the spans
// are contiguous — each span's End is the next span's Start, and the last ends
// at len(text) — so an offset landing on a separator still resolves to a page.
func joinPageSpans(blocks []string, sep string) (string, []types.PageSpan) {
	var b strings.Builder
	spans := make([]types.PageSpan, 0, len(blocks))
	sepRunes := utf8.RuneCountInString(sep)
	offset := 0
	for i, block := range blocks {
		if block == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(sep)
			offset += sepRunes
		}
		start := offset
		b.WriteString(block)
		offset += utf8.RuneCountInString(block)
		spans = append(spans, types.PageSpan{Start: start, End: offset, Page: i + 1})
	}
	for i := 0; i < len(spans)-1; i++ {
		spans[i].End = spans[i+1].Start
	}
	return b.String(), spans
}
