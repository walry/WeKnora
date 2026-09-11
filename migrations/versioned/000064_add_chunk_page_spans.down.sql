-- Migration: 000064_add_chunk_page_spans (down)
-- Description: Remove page_start / page_end columns from the chunks table.
DO $$ BEGIN RAISE NOTICE '[Migration 000064 down] Removing page_start / page_end from chunks'; END $$;

DROP INDEX IF EXISTS idx_chunks_page_start;
ALTER TABLE chunks DROP COLUMN IF EXISTS page_start;
ALTER TABLE chunks DROP COLUMN IF EXISTS page_end;

DO $$ BEGIN RAISE NOTICE '[Migration 000064 down] page_start / page_end removed successfully'; END $$;
