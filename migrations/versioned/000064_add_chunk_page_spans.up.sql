-- Migration: 000064_add_chunk_page_spans
-- Description: Add page_start / page_end columns to the chunks table so each
-- chunk records the source-document page range it was drawn from. This enables
-- "jump to original page" from search/retrieval results.
--
-- 0 means the page is unknown (format has no page concept, or the document
-- predates page tracking). 1-based, matching human-readable page numbers.
DO $$ BEGIN RAISE NOTICE '[Migration 000064] Adding page_start / page_end to chunks'; END $$;

ALTER TABLE chunks ADD COLUMN IF NOT EXISTS page_start INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS page_end INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN chunks.page_start IS '1-based first source-document page of this chunk; 0 = unknown';
COMMENT ON COLUMN chunks.page_end IS '1-based last source-document page covered by this chunk; equal to page_start for single-page chunks; 0 = unknown';

-- Optional aid for "search within pages N..M" queries.
CREATE INDEX IF NOT EXISTS idx_chunks_page_start ON chunks (knowledge_id, page_start);

DO $$ BEGIN RAISE NOTICE '[Migration 000064] page_start / page_end added successfully'; END $$;
