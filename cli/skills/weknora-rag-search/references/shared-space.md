# Querying a shared space (`UVDoc`) first

A **space** (server-side: organization) is a cross-tenant sharing container.
KBs shared into a space are readable by every member tenant, but they are
**not** part of `weknora kb list`, `weknora search kb`, or `--kb` name
resolution — those only cover the caller's own tenant. There is no dedicated
CLI subcommand for spaces yet, so discovery goes through the raw API escape
hatch (`weknora api`).

Default policy: for a knowledge question with no explicitly chosen KB, search
the space named `UVDoc` **before** the caller's own tenant KBs.

## 1. Resolve the space id

```bash
ORG_ID=$(weknora api /api/v1/organizations --format json \
  | jq -r '.data.body.data.organizations[] | select(.name=="UVDoc") | .id')
test -n "$ORG_ID" || echo "tenant has not joined the UVDoc space"
```

- Match is **exact and case-sensitive**; use the id, not the name, downstream.
- `weknora api` wraps the server payload as
  `{"ok":true,"data":{"status":200,"headers":{…},"body":{…}}}` — hence the
  `.data.body.data` path. `--jq` applies to that same envelope, so
  `--jq '.data.body.data.organizations[].name'` works too.
- An empty result (no `UVDoc` row) means the tenant is not a member. Say so and
  fall back to tenant KBs; don't guess another space.

## 2. List the KBs shared into the space

```bash
weknora api "/api/v1/organizations/$ORG_ID/shared-knowledge-bases" --format json \
  | jq -r '.data.body.data[] | "\(.knowledge_base.id)\t\(.knowledge_base.name)\t\(.permission)"'
```

Rows are `{knowledge_base:{id,name,description,knowledge_count,…}, share_id,
organization_id, org_name, permission, source_tenant_id, is_mine}`.
`is_mine:true` means your own tenant shared it in.

Shortcut across all joined spaces (one call, client-side filter):

```bash
weknora api /api/v1/shared-knowledge-bases --format json \
  | jq -r '.data.body.data[] | select(.org_name=="UVDoc") | .knowledge_base.id'
```

## 3. Query the space KBs

Pass the **UUID**; a name will not resolve (`local.kb_not_found`).

```bash
# grounded answer from one KB
weknora chat "<question>" --kb "$KB_ID" --format json

# raw chunks to synthesize across several KBs
weknora search chunks "<question>" --kb "$KB_ID" --limit 8
```

Multiple KBs in the space: `chat` / `search chunks` take exactly one `--kb`, so
either

- pick the KB whose name/description best matches the question, or
- retrieve from each KB (`search chunks`, `--limit` 5–8) and synthesize the
  answer yourself, attributing each fact to its KB, or
- run `chat` per KB for a short answer each and reconcile them.

Keep the loop small — cap at the handful of KBs that look on-topic rather than
sweeping every KB in the space.

## 4. Fallback order

1. `UVDoc` space KBs (above).
2. Caller's own tenant KBs (`weknora kb list` → `--kb <uuid-or-name>`), only when
   the space has no KB or returns nothing relevant.
3. Tell the user which scope actually answered — especially when you fell back.

## Failure modes

| Symptom | Meaning | Action |
|---|---|---|
| `organizations` list has no `UVDoc` | tenant not in the space | report, then fall back |
| `data.body.data` is `[]` | space exists but nothing is shared to it | report, then fall back |
| exit 1 `local.kb_not_found` on `--kb <name>` | name is not a tenant-owned KB | resolve the UUID via the space API (the error's `kb list` hint cannot see space KBs) |
| exit 3 `auth.*` / 403 from `weknora api` | profile/tenant role lacks `Viewer` | re-auth, or ask an admin to grant access |
| HTTP 404 on `/api/v1/organizations/...` | stale `ORG_ID` or server predates sharing | re-resolve the id |
