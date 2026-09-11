package docparser

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestJoinPageSpans(t *testing.T) {
	blocks := []string{"第一页内容", "", "第三页abc"}
	text, spans := joinPageSpans(blocks, "\n\n")
	// "第一页内容" (5) + "\n\n" (2) + "第三页abc" (6) => 13 runes
	if got := len([]rune(text)); got != 13 {
		t.Fatalf("text len = %d, want 13 (%q)", got, text)
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	// span 0 = page 1, [0, 5) extended to next start 7
	if spans[0].Page != 1 || spans[0].Start != 0 || spans[0].End != 7 {
		t.Fatalf("span0 = %+v", spans[0])
	}
	// span 1 = page 3 (empty page 2 skipped but numbering preserved)
	if spans[1].Page != 3 || spans[1].Start != 7 || spans[1].End != 13 {
		t.Fatalf("span1 = %+v", spans[1])
	}

	// offset 6 (in the separator gap) resolves to page 1; offset 8 -> page 3.
	if s, e := types.ResolvePageRange(spans, 6, 7); s != 1 || e != 1 {
		t.Fatalf("offset 6 => (%d,%d), want (1,1)", s, e)
	}
	if s, e := types.ResolvePageRange(spans, 8, 9); s != 3 || e != 3 {
		t.Fatalf("offset 8 => (%d,%d), want (3,3)", s, e)
	}
	// chunk straddling the boundary => (1,3)
	if s, e := types.ResolvePageRange(spans, 3, 10); s != 1 || e != 3 {
		t.Fatalf("straddle => (%d,%d), want (1,3)", s, e)
	}
}

func TestShiftPageSpans(t *testing.T) {
	spans := []types.PageSpan{{Start: 0, End: 5, Page: 1}}
	shifted := shiftPageSpans(spans, 3)
	if shifted[0].Start != 3 || shifted[0].End != 8 {
		t.Fatalf("shifted = %+v", shifted[0])
	}
}
