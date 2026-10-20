# Shared spaces (organizations) — discovery and failure modes

A **space** is a WeKnora organization: a collaboration surface several
workspaces join. Knowledge bases and agents keep living in their owner
workspace; the space only makes them *visible* (and addressable) to member
workspaces. Nothing is copied.

The protocol in `SKILL.md` makes the `UVDoc` space the primary corpus. This file
covers the mechanics behind it.

## Why every space KB has to be addressed by UUID

- `--kb` resolution accepts a UUID verbatim, but a **name** is looked up only in
  the caller's own workspace list (`kb list`). A shared KB belongs to another
  workspace, so its name never matches → exit 1 `local.kb_not_found`.
- `search kb "<q>"` is the same own-workspace list, filtered client-side. It
  cannot see space KBs either.
- `kb list --space` / `kb list --shared` project each KB to
  `{id, name, source, org_name, organization_id, permission, source_tenant_id,
  knowledge_count, chunk_count}` — `id` is the UUID to pass to `--kb`.

```
weknora kb list --space UVDoc --format json --jq '.data[].id'
```

## Commands

| Goal | Command |
|---|---|
| KBs in one space | `weknora kb list --space UVDoc --format json` |
| KBs in every space this workspace belongs to | `weknora kb list --shared --format json` |
| Agents in one space | `weknora agent list --space UVDoc --format json` |
| Same, by UUID (no name lookup) | `weknora kb list --space <space-uuid>` |

Drop the workspace's own contributions (rows it shared into the space) when you
only want other teams' material:

```bash
weknora kb list --space UVDoc --format json --jq '.data[] | select(.source=="shared")'
weknora kb list --space UVDoc --format json --jq '[.data[] | select(.source=="shared") | .id]'
```

`--space` and `--shared` are mutually exclusive (both flag-error, exit 2).
`--pinned` and `--limit` still apply, client-side.

### How a space name is matched

`--space` accepts a name or a UUID:

1. UUID → used as-is (the server decides membership);
2. case-insensitive **exact** name — `uvdoc` matches `UVDoc`;
3. otherwise a **unique** case-insensitive prefix — `uv` is rejected as
   ambiguous when both `UVDoc` and `UVDoc Docs` exist;
4. no match → `local.space_not_found` (exit 1).

Ambiguity is an error on purpose: answering from the wrong space silently is
worse than asking again. Re-run with the UUID.

## Answering from a space

`chat` takes exactly one `--kb`; `search chunks` likewise. So:

```bash
# one written answer from one KB
weknora chat "<question>" --kb <kb-uuid> --reference

# retrieval across 2-3 relevant KBs, synthesised by you
weknora search chunks "<question>" --kb <kb-uuid-a> --limit 12 --format json
weknora search chunks "<question>" --kb <kb-uuid-b> --limit 12 --format json

# an agent that already scopes several space KBs — preferred when it exists
weknora session ask --agent <agent-id> "<question>" --reference
```

Pick the agent when `agent list --space` returns one matching the question's
domain: it carries the space's own KB scope and prompting, and it is a single
round-trip. Otherwise retrieve per KB and synthesise yourself.

## Permissions — what actually gates this

| Caller | List space KBs | `chat` / `search chunks` on a space KB | Needs `manage_spaces`? |
|---|---|---|---|
| Normal user (JWT via `auth login`) | yes, if a member | yes, with viewer permission | **no** — the API-key gate does not run for JWT |
| API key, full access | yes | yes | no |
| API key, scoped **without** `manage_spaces` | **403** | yes (`chat` / `retrieve` capabilities) | — only listing is blocked |

So a scoped key can still answer from a space KB once it knows the UUID; it just
cannot enumerate the space. If listing is 403, do not conclude the space is
empty — report the credential gap (exit 3 `auth.forbidden`).

Reading a space KB requires at least `viewer` on the share, capped by the
caller's own workspace role. `permission` in the listing tells you what you got;
`viewer` is enough for `chat` / `search chunks` / `session ask`.

## Failure matrix

| Symptom | Cause | Agent action |
|---|---|---|
| exit 1 `local.space_not_found` | name misspelled, or the workspace is not a member | check the name; list spaces with `weknora api /api/v1/organizations` |
| exit 1 `local.space_ambiguous` | prefix matched several spaces | re-run with the UUID or full name |
| exit 3 `auth.forbidden` on `kb list --space` | scoped API key without `manage_spaces` | report the credential gap; never present it as "space has no KBs" |
| empty `data` | the space really has no KBs shared in | legitimate fallback trigger |
| 0 chunks for every KB | the space has no content for this question | legitimate fallback trigger |
| `chat` answers but cites nothing | `--reference` not passed | re-run with `--reference`; a space answer must be attributable |

## Making a space KB the default for a session

```bash
# env, per process
export WEKNORA_KB_ID=<kb-uuid>

# or bind the current directory (writes ./.weknora/project.yaml)
weknora link <kb-uuid>
```

Both take the UUID, never a name. `link` writes to the user's working directory —
only run it when the user asked for a binding, never as a side effect of
answering a question.
