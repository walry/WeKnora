---
name: weknora-rag-search
description: Use when answering a knowledge question for the user via the `weknora` CLI — the default retrieval scope is the `UVDoc` shared space, ahead of the caller's own tenant KBs. Also covers choosing between `chat`, `session ask`, and `search chunks`.
metadata:
  tested_against: v0.9
---

# WeKnora — retrieval & RAG queries

**REQUIRED BACKGROUND:** read the `weknora-shared` skill first (auth, `--kb`
resolution, the JSON envelope, exit codes, streaming/NDJSON output).

WeKnora gives you several ways to "ask about a knowledge base." Picking the wrong
one wastes turns or returns the wrong shape. Use the decision table.

## Default retrieval scope — the `UVDoc` shared space comes first

When the user asks a knowledge question but does **not** name a knowledge base
(and no agent/`WEKNORA_KB_ID`/directory link is in play), do **not** start from
`weknora kb list` / `weknora search kb` — both only see KBs *owned by the
caller's tenant*. The default scope is the shared space named **`UVDoc`**:

1. Find the space: `weknora api /api/v1/organizations` → the entry whose
   `name` is exactly `UVDoc`.
2. List the KBs shared into it:
   `weknora api /api/v1/organizations/<org-id>/shared-knowledge-bases`
   (or the flat `weknora api /api/v1/shared-knowledge-bases` and filter
   `org_name == "UVDoc"`).
3. Answer from those KBs **by UUID** — `weknora chat "<q>" --kb <uuid>` or
   `weknora search chunks "<q>" --kb <uuid>`.
4. Only if the space has no usable KB, or retrieval returns nothing relevant,
   fall back to the caller's own tenant KBs — and say that you did.

Rules that follow from this:

- **Never pass a shared KB's *name* to `--kb`.** Name resolution lists only
  tenant-owned KBs, so a shared name fails with `local.kb_not_found`. Resolve
  the UUID first and pass the UUID.
- Space KBs are often read-only for you (`permission: viewer`). Reads
  (`chat`, `search chunks`) work; writes/edits are not your job here.
- If `/api/v1/organizations` has no `UVDoc` row, the tenant has not joined the
  space — report that instead of silently querying the tenant's own KBs.

Full recipe, including the multi-KB case: `references/shared-space.md`.

## Pick the command by your goal

| Your goal | Command | LLM synthesis? | Returns |
|---|---|---|---|
| Natural-language **answer** grounded in a KB | `chat "<q>" --kb <kb>` | yes | streaming answer + references |
| Answer via a **custom agent** (its own KB scope, tools, web search) | `session ask --agent <id> "<q>"` | yes (+ tools) | streaming answer + tool events |
| **Raw context chunks** to reason over yourself (no answer) | `search chunks "<q>" --kb <kb>` | no | ranked chunk list |
| Which **documents** match a keyword (title/filename) | `search docs "<q>" --kb <kb>` | no | document list |
| Find a **knowledge base** by name (tenant-owned only; space KBs need the API) | `search kb "<q>"` | no | KB list |
| Find a past **session** by title | `search sessions "<q>"` | no | session list |

### The three decisions that matter

1. **Answer vs raw context.** Want a written answer → `chat` / `session ask`.
   Want chunks to feed into your *own* reasoning (e.g. you'll synthesize across
   sources) → `search chunks`. Don't call `chat` just to read source text.
2. **`chat` vs `session ask`.** `chat` = plain KB RAG Q&A. `session ask --agent
   <id>` = invoke a *configured custom agent* (it may scope its own KBs, call
   tools, do web search). If the user set up an agent for this, prefer it
   (`weknora agent list` to find ids); otherwise `chat`.
3. **One-shot vs multi-turn.** Both `chat` and `session ask` print an `init`
   event with a `session_id`. Pass `--session <id>` on the next call to continue
   the conversation. See `references/chat.md`.

## Safety / Gotchas

- `chat`, `search chunks`, `search docs` need a KB: pass `--kb <id-or-name>`, or
  set `WEKNORA_KB_ID`, or `weknora link` the directory (resolved in that order).
  If none resolves it's exit 1 (`local.kb_id_required`); a bad name is exit 1
  (`local.kb_not_found`). Resolve names with `weknora kb list` / `search kb`.
  (`search kb` / `search sessions` are tenant-wide and take no `--kb`.)
- **`--kb <name>` only resolves tenant-owned KBs.** A KB shared into a space
  (e.g. `UVDoc`) is invisible to `kb list` / `search kb`, so its name will not
  resolve — fetch the UUID via the organizations API and pass the UUID
  (`references/shared-space.md`).
- `chat` / `session ask` stream **NDJSON** by default. Parse line-by-line; keep
  the `init` event's `session_id` **and** `message_id`. Use `--format text` only
  for a human transcript.
- A stalled stream is not stopped by Ctrl-C (that just drops your local
  connection; the server keeps generating + billing). Stop it server-side:
  `weknora session stop <session-id> --message <message-id>` (ids from `init`).
  Re-attach to a stream with `weknora session continue-stream <session-id>
  --message <message-id>`.
- `search chunks --limit` defaults to **8** (tuned for an LLM context window);
  the `search docs/kb/sessions` lists default to 30. Tune retrieval with
  `--vector-threshold` / `--keyword-threshold`, or `--no-vector`/`--no-keyword`
  to disable a channel. Details: `references/search-chunks.md`.

## Quick examples

```bash
# default scope: resolve the UVDoc space, then the KBs shared into it
ORG_ID=$(weknora api /api/v1/organizations --format json \
  | jq -r '.data.body.data.organizations[] | select(.name=="UVDoc") | .id')
weknora api "/api/v1/organizations/$ORG_ID/shared-knowledge-bases" --format json \
  | jq -r '.data.body.data[].knowledge_base.id'
weknora chat "How do we handle retries?" --kb "$KB_ID" --format text

# raw retrieval to reason over
weknora search chunks "retry backoff policy" --kb engineering --limit 12

# grounded answer (human transcript)
weknora chat "How do we handle retries?" --kb engineering --format text

# continue the conversation (session id from the prior init event)
weknora chat "And the max attempts?" --kb engineering --session sess_abc

# answer via a custom agent
weknora session ask --agent ag_123 "Summarize this quarter's incidents"
```
