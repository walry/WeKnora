package types

import "strings"

// ReadRequest is the unified transport-agnostic request for document reading.
// Set FileContent for file mode, URL for URL mode.
type ReadRequest struct {
	FileContent           []byte
	FileName              string
	FileType              string
	URL                   string
	Title                 string
	ParserEngine          string
	RequestID             string
	ParserEngineOverrides map[string]string
}

// PageSpan maps a half-open rune-offset range [Start, End) inside
// MarkdownContent back to a page of the source document.
//
// Offsets are counted in Unicode code points so that Python (len(str)) and Go
// (len([]rune(s))) agree without conversion. Page is 1-based; Label carries an
// optional human-readable locator (sheet name, slide title, ...) for formats
// whose "page" is not a number.
//
// Producers should emit spans in ascending Start order and ideally cover the
// whole document contiguously (extend each span's End to the next span's
// Start) so that no offset falls into a gap.
type PageSpan struct {
	Start int
	End   int
	Page  int
	Label string
}

// ReadResult is the transport-agnostic result of document reading.
type ReadResult struct {
	MarkdownContent string
	ImageRefs       []ImageRef
	ImageDirPath    string
	Metadata        map[string]string
	Error           string
	IsAudio         bool   // true when the result contains raw audio data needing ASR transcription
	AudioData       []byte // raw audio bytes for ASR processing
	// PageSpans locates each region of MarkdownContent in the source document.
	// Empty when the parser has no page concept (plain text, Markdown, ...),
	// in which case chunk page numbers stay unknown (0) and the UI hides them.
	PageSpans []PageSpan
}

// ResolvePageRange maps a chunk's rune-offset range [start, end) onto the
// page range of the source document. Page numbers are 1-based; (0, 0) means
// "unknown" (no spans available, or the offsets could not be located).
//
// A chunk straddling a page boundary yields (n, n+1); callers should render
// that as "p.n–n+1" and jump to the first page.
func ResolvePageRange(spans []PageSpan, start, end int) (int, int) {
	if len(spans) == 0 || end <= start {
		return 0, 0
	}
	// end is exclusive — the last rune of the chunk is at end-1.
	from := resolvePage(spans, start)
	to := resolvePage(spans, end-1)
	if from == 0 {
		from = to
	}
	if to == 0 {
		to = from
	}
	if from > to {
		from, to = to, from
	}
	return from, to
}

// resolvePage returns the page containing offset, or 0 when it cannot be
// determined. spans must be sorted by Start (as emitted by every parser).
// Offsets that fall into a gap between spans are attributed to the nearest
// preceding span — page-break separators and stripped boilerplate routinely
// create such gaps, and the preceding page is the most useful guess.
func resolvePage(spans []PageSpan, offset int) int {
	lo, hi := 0, len(spans)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if spans[mid].Start <= offset {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return 0 // before the first span
	}
	return spans[lo-1].Page
}

// ImageRef represents an image reference extracted from the document.
type ImageRef struct {
	Filename    string
	OriginalRef string
	MimeType    string
	StorageKey  string
	ImageData   []byte // inline image bytes (universal fallback for cross-machine deployments)
	// IsOriginal marks references that point to the originally uploaded file
	// itself (e.g. when the user uploads a standalone image). Such references
	// must not be dropped by the icon/size filter — otherwise a small image
	// upload would be silently discarded before multimodal processing.
	IsOriginal bool
}

// ParserEngineInfo describes a registered parser engine.
type ParserEngineInfo struct {
	Name              string
	Description       string
	FileTypes         []string
	Available         bool
	UnavailableReason string
}

// --- Internal types used by chunking pipeline ---

type DocParserStorageConfig struct {
	Provider        string
	Region          string
	BucketName      string
	AccessKeyID     string
	SecretAccessKey string
	AppID           string
	PathPrefix      string
	Endpoint        string
}

type DocParserVLMConfig struct {
	ModelName     string
	BaseURL       string
	APIKey        string
	InterfaceType string
}

type ParsedChunk struct {
	Content string
	// ContextHeader is an optional context string (e.g. a Markdown heading
	// breadcrumb) that should be prepended at embedding time but is NOT
	// part of the stored Content. Lets retrieval pipelines see section
	// context without breaking End-Start == len(Content) invariants.
	ContextHeader string
	Seq           int
	Start         int
	End           int
	Images        []ParsedImage
	ChunkID       string // populated by processChunks with the actual DB UUID

	// ParentIndex is set when using parent-child chunking strategy.
	// -1 (or unset/0 for flat chunks) means this is a top-level chunk.
	// >= 0 means this is a child chunk referencing the parent at this index
	// in the ParentChunks slice of ProcessChunksOptions.
	ParentIndex int

	// PageStart/PageEnd are the 1-based source-document pages this chunk
	// covers (PageStart == PageEnd for a single-page chunk). 0 means unknown.
	// Derived from ReadResult.PageSpans after chunking, before persistence.
	PageStart int
	PageEnd   int
}

// EmbeddingContent returns the text that should be sent to the embedding
// model: ContextHeader (if any) prepended to Content. Mirrors
// chunker.Chunk.EmbeddingContent so the choice is consistent across the
// chunker output and the indexing pipeline. Surrounding whitespace on
// Content is trimmed so leading/trailing newlines from boundary slicing
// don't dilute the embedded vector.
func (c ParsedChunk) EmbeddingContent() string {
	body := strings.TrimSpace(c.Content)
	if c.ContextHeader == "" {
		return body
	}
	return c.ContextHeader + "\n\n" + body
}

// ParsedParentChunk represents a parent chunk in the parent-child strategy.
// Parent chunks are stored in DB for context retrieval but NOT vector-indexed.
type ParsedParentChunk struct {
	Content string
	Seq     int
	Start   int
	End     int
	// PageStart/PageEnd mirror ParsedChunk: 1-based source pages, 0 = unknown.
	PageStart int
	PageEnd   int
}

type ParsedImage struct {
	URL         string
	Caption     string
	OCRText     string
	OriginalURL string
	Start       int
	End         int
}
