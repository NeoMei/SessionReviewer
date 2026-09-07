# v4 scanned Session display repair

Approved in the current Codex task: expose already scanned Session contents through a read-only CLI API and the Obsidian panel, preserve existing layout, fix project switching, then install locally and verify in real Obsidian. Parser repair and a public release are excluded.

## Data boundary

Use the existing `inspect.SessionEventPage` and TypeScript `SessionEventPageV1` contracts. Implement `inspect session-events` using the existing exact argument parser, authenticated current project mapping, private published generation and immutable observation objects. Never read raw Codex JSONL or rescan. A supplied generation, provider, session and cursor must all belong together. Refuse stale, tampered, unavailable or cross-project state. Public snapshot validity does not establish semantic acceptance.

The panel reads the validated public Session index; event excerpts are requested through the CLI. The plugin must not read private storage directly. Keep the existing excerpt bound (512 bytes); right-hand details show the complete available excerpt and identity/time, explicitly labelled as an indexed excerpt rather than full transcript. Unknown roles or absent agent responses must not be invented. No model calls; no changes to source logs, ledger acceptance or accounting.

## Interface

`session-reviewer inspect session-events --project-id ID --provider codex --session-id ID --expected-generation-id ID --limit 25 --json [--cursor TOKEN | --anchor SEQUENCE]`

The existing parser permits limits 1–100. Return canonical contract JSON; cursors are opaque and bind project/provider/session/generation/view/limit/offset. A successful page identifies total and a zero-based half-open wire range, with first/last/previous/next navigation; UI converts it to a one-based inclusive display. Error responses are bounded machine-readable diagnostics, never arbitrary private paths. Empty is distinct from failed.

## Panel

Keep top navigation and the existing native Markdown summary. Add a clearly labelled `扫描记录` section: searchable/paged Session list on the left; paged event list and right-hand selected-event details. Show Session coverage including partial/error/unavailable states, event counts, indexed/excluded records and exact omission reason. Do not substitute empty summaries for scan records. Project name comes from the validated descriptor's containing project folder when v4 documents lack a name, keeping the stable ID as secondary identity.

Always offer a project selector and project-list refresh, including invalid/migration states. Switching persists the selected project, clears previous project/event state and cancels obsolete asynchronous responses. Newly added or changed project descriptors must be rediscovered on explicit refresh; no out-of-scope scan is triggered. CLI unavailable or stale generation leaves public coverage readable and presents a retryable error without showing another project's events.

## Acceptance

Tests cover paginated events, anchors, stale/cross-project cursor rejection, no filesystem writes; safe CLI argv and payload binding; event rendering, coverage, project persistence/rediscovery, race handling, empty/error/unavailable states. Preserve legacy project rendering and human-edit safety tests. Build and test Go and plugin, independently review, then back up installed artifacts, install the local repair and reload only SessionReviewer. Verify the real Session has 83 indexed events, can page to the last record, and can switch to AgentWiki and back. The 4684 undecodable records remain an explicitly reported upstream limitation.
