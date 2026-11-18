# #5314 — write-path × side-effect audit

Repo: `home-os`, branch `fh/5314` off `origin/feat/family-hub`.

Write paths: **API REST handlers** (`apps/api/internal/*/handler.go`), **MCP tool layer**
(`apps/mcp/internal/*`), **worker** (`apps/worker`), **UI/BFF** (`apps/ui/app/api/*` — only
`auth/{login,callback,logout}`, no domain SQL, calls the API over HTTP → it is a REST caller and
inherits the REST column).

| Side-effect | REST handler | MCP tool | Worker | UI/BFF | Before |
|---|---|---|---|---|---|
| **search index** (`household_search`) | ✅ per-handler `h.indexXxx` | ❌ none, except `asset`+`guides` which built their own documents | n/a (owns `files` collection) | via REST | **BROKEN for MCP** — the #5298 cause |
| **activity_log** (050 trigger, 26 tables) | ✅ | ✅ | ✅ | ✅ | already at parity (trigger) |
| **notifications** (054, downstream of activity_log) | ✅ | ✅ | ✅ | ✅ | already at parity (trigger) |
| **file search deletions** (053 trigger + worker drain) | ✅ | ✅ | ✅ | ✅ | already at parity (trigger-fed queue) |
| **outbox_events** | ✅ bills, assets | ⚠️ bills only — assets miss it | — | — | **partial** — `asset_create`/`asset_update` via MCP write no `asset.created`/`asset.updated`. NOT fixed here (separate subsystem, no in-tree consumer) |
| **guide cross-hints** | — | ✅ (`createGuideHint`) | — | — | MCP-only by design (agent affordance, not a domain effect) |
| **cache invalidation** | none in this repo | — | — | — | no such subsystem → not a gap |

## After (#5314)

| Side-effect | REST | MCP | Worker | How |
|---|---|---|---|---|
| search index | ✅ (fast path) | ✅ (queued) | ✅ (any writer) | 075 trigger on 12 tables → `search_index_queue` → `packages/searchindex.Build`+`Sweep` |
| activity_log | ✅ | ✅ | ✅ | unchanged (050) |
| notifications | ✅ | ✅ | ✅ | unchanged (054) |
| file deletions | ✅ | ✅ | ✅ | unchanged (053) |
| outbox (assets) | ✅ | ❌ | — | **open** — same defect class, deliberately not bundled |

## Why the shared layer is Postgres, not a service object

`apps/api` and `apps/mcp` are separate Go modules in `go.work`; neither can import the other's
service layer. An in-process "domain layer both paths call" does not exist and cannot be created
without merging the modules. The one layer both already share is Postgres, so the shared write
path is a trigger (075) plus one shared Go package for the document builder and the drain
(`packages/searchindex`, `replace`d into both modules). Side-effect rules are NOT duplicated into
the MCP handlers — no MCP tool had to be changed to make its writes searchable.

## Backfill

Yes, needed: rows written via MCP before this change are absent from the index (the #5298
measurement). `076_search_index_backfill` enqueues every existing row once; the API's drain
catches up in batches on its first tick after deploy. No separate job, and search keeps serving.
`apps/api/cmd/reindex` still exists (now through the shared builder) but drops and rebuilds the
collection, leaving search empty until it finishes — prefer the queue.

Verified: `migrate up` applied 075 + 076 cleanly against a full 001→076 schema.
