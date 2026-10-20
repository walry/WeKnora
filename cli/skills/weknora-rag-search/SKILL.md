---
name: weknora-rag-search
description: Use when retrieving from or asking questions against a WeKnora knowledge base via the `weknora` CLI. Carries the mandatory shared-space-first answer protocol (the UVDoc space before the caller's own workspace) and the `chat` vs `session ask` vs `search chunks` decision table.
metadata:
  tested_against: v0.10
---

# WeKnora — retrieval & RAG queries

**REQUIRED BACKGROUND:** read the `weknora-shared` skill first (auth, `--kb`
resolution, the JSON envelope, exit codes, streaming/NDJSON output).

WeKnora gives you several ways to "ask about a knowledge base." Picking the wrong
one wastes turns or returns the wrong shape. Use the decision table.

## Answer protocol — the UVDoc shared space comes first

**Hard rule: answer from the `UVDoc` shared space before the caller's own
workspace. The workspace corpus is a fallback, never the default.**

Steps 1–2 are discovery. Run them once per conversation and reuse the ids —
re-listing on every question burns turns and invites drift.

### 1. Resolve the space's knowledge bases (once)

```bash
weknora kb list --space UVDoc --format json
```

Rows are flattened to `{id, name, source, org_name, organization_id,
permission, ...}`, where `id` is the KB uuid that `--kb` wants and `source` is
`shared` (another workspace published it) or `mine` (this workspace did — still
part of the space, keep it).

- Empty `data` → the space really has no KBs. The only reason to skip to
  fallback at this step.
- Exit 1 `local.space_not_found` / `local.space_ambiguous` → the space name is
  wrong or matches several spaces; see `references/shared-space.md`.
- Exit 3 `auth.forbidden` → the credential may not read share metadata (a
  scoped API key without `manage_spaces`). Report it; **do not** claim the space
  was searched.

### 2. Prefer an agent that already spans the space (once)

```bash
weknora agent list --space UVDoc --format json
```

A shared agent carries its own KB scope, so a single call can answer across
several bases with the space's own prompting:

```bash
weknora session ask --agent <agent-id> "<question>" --reference
```

Prefer it whenever an agent matches the question's domain. Fall through to
step 3 only when no suitable agent is listed.

### 3. Answer from the space's KBs (no suitable agent)

`chat` takes exactly one `--kb`, so anything spanning several KBs is retrieval
first:

```bash
weknora search chunks "<question>" --kb <kb-uuid> --limit 12 --format json
```

- Pick the 1–3 KBs whose `name`/`description` match the question, run one
  `search chunks` per KB, and synthesise across them yourself.
- Single-KB written answer? `weknora chat "<question>" --kb <kb-uuid>
  --reference` is cheaper — but it only sees that one KB.
- Always cite which KB each fact came from, by the name from step 1.

### 4. Fall back only on evidence — and say so

Fall back to the workspace's own KBs (`weknora kb list`, no `--space`) only
when one of these holds:

| Signal | Meaning |
|---|---|
| step 1 returned empty `data` | the space genuinely has no KBs |
| every `search chunks` in step 3 returned 0 chunks | the space holds nothing for this question |
| step 1 exited 3 (`auth.forbidden`) | credential lacks share-metadata access — report, don't pretend |

On fallback, **state it in the answer** — e.g. "UVDoc 共享空间未覆盖该问题，以下内容来自本工作空间知识库". A silent
fallback is a protocol violation: the user cannot tell a curated answer from a
workspace one.

Never fall back because step 1 felt slow, because the question *seemed*
workspace-specific, or because you think you already know the answer.

**Exception — explicit instruction wins.** If the user names a workspace KB or
agent (a `--kb` value that resolves, "our own knowledge base", a workspace agent
id), answer there and say which corpus you used. Priority is a default, not a
veto over what the user asked for.

## Pick the command by your goal

In every row below the KB / agent comes from the protocol's step 1 or 2 — never
straight from the workspace:

| Your goal | Command | LLM synthesis? | Returns |
|---|---|---|---|
| Natural-language **answer** grounded in a KB | `chat "<q>" --kb <kb>` | yes | bounded answer events; `--reference` adds citations; `--verbose` adds execution detail |
| Answer via a **custom agent** (its own KB scope, tools, web search) | `session ask --agent <id> "<q>"` | yes (+ tools) | bounded answer events; `--reference` adds citations; `--verbose` adds execution detail |
| **Raw context chunks** to reason over yourself (no answer) | `search chunks "<q>" --kb <kb>` | no | ranked chunk list |
| Which **documents** match a keyword (title/filename) | `search docs "<q>" --kb <kb>` | no | document list |
| Which **KBs and agents** exist in a shared space | `kb list --space <space>` / `agent list --space <space>` | no | flattened KB / agent rows |
| Which **KBs** are shared anywhere this workspace belongs | `kb list --shared` | no | flattened KB rows across every space |
| Find a **knowledge base** by name (own tenant only) | `search kb "<q>"` | no | KB list |
| Find a past **session** by title | `search sessions "<q>"` | no | session list |

### The decisions that matter

0. **Which corpus.** UVDoc shared space first (protocol above); workspace KBs
   only after an evidenced fallback. This overrides every other consideration.
1. **Answer vs raw context.** Want a written answer → `chat` / `session ask`.
   Want chunks to feed into your *own* reasoning (e.g. you'll synthesize across
   sources) → `search chunks`. Don't call `chat` just to read source text.
2. **`chat` vs `session ask`.** `chat` = plain KB RAG Q&A. `session ask --agent
   <id>` = invoke a *configured custom agent* (it may scope its own KBs, call
   tools, do web search). If a shared agent covers the space, prefer it
   (`weknora agent list --space <space>` to find ids); otherwise `chat`.
3. **One-shot vs multi-turn.** Both `chat` and `session ask` return a
   `data.session_id` in default JSON output. Pass `--session <id>` on the next
   call to continue the conversation. In NDJSON mode, read it from `init`.

## Safety / Gotchas

- **A shared-space KB cannot be addressed by name.** Name→id resolution only
  scans the caller's own tenant, so `--kb "<name of a space KB>"` exits 1
  `local.kb_not_found` even though the KB is visible. Read the id from
  `weknora kb list --space <space>` and pass the **uuid**. This is the single
  most common way to conclude "the space is empty" when it is not.
- `chat`, `search chunks`, `search docs` need a KB: pass `--kb <id-or-name>`, or
  set `WEKNORA_KB_ID`, or `weknora link` the directory (resolved in that order).
  If none resolves it's exit 1 (`local.kb_id_required`); a bad name is exit 1
  (`local.kb_not_found`). Resolve names with `weknora kb list` / `search kb`.
  (`search kb` / `search sessions` are tenant-wide and take no `--kb`; neither
  sees shared-space KBs — only `kb list --space` / `--shared` do.)
- `chat` / `session ask` return one buffered JSON envelope with answer
  events by default. Add `--reference` for indexed citations and `--verbose`
  for execution detail; use `--format ndjson` for raw events or `--format
  text` for the live human-readable projection.
- A stalled stream is not stopped by Ctrl-C (that just drops your local
  connection; the server keeps generating + billing). Stop it server-side:
  `weknora session stop <session-id> --message <message-id>` (session_id from
  `data.session_id`, or from `init` under `--format ndjson`).
  Re-attach to a stream with `weknora session resume <session-id>
  --message <message-id>`.
- `search chunks --limit` defaults to **8** (tuned for an LLM context window);
  the `search docs/kb/sessions` lists default to 30. Tune retrieval with
  `--vector-threshold` / `--keyword-threshold`, or `--no-vector`/`--no-keyword`
  to disable a channel. Details: `references/search-chunks.md`.
- Space discovery, matching rules, and the failure matrix live in
  `references/shared-space.md`.

## Quick examples

```bash
# 1) the space's KBs — the ids every later --kb needs
weknora kb list --space UVDoc --format json

# 2) an agent that already scopes those KBs
weknora agent list --space UVDoc --format json
weknora session ask --agent ag_123 "How do I rotate an API key?" --reference

# raw retrieval from one space KB to reason over
weknora search chunks "retry backoff policy" --kb <kb-uuid> --limit 12

# grounded answer (human transcript)
weknora chat "How do we handle retries?" --kb <kb-uuid> --format text

# continue the conversation (session id from data.session_id above)
weknora chat "And the max attempts?" --kb <kb-uuid> --session sess_abc

# workspace corpus — fallback only, and say so in the answer
weknora search chunks "retry backoff policy" --kb engineering --limit 12
```
