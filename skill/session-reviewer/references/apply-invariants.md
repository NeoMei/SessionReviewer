# Apply semantic invariants

Read this reference whenever synthesizing a proposal. JSON Schema checks shape; `session-reviewer apply` also enforces these stateful rules.

## Packet and evidence binding

- Bind the proposal to the exact schema-v2 packet identity, cursor range, and digest. Packet `expected_cursor` must be `from_cursor - 1`; `next_cursor` must be `to_cursor`; an empty packet has equal boundaries.
- If the packet has `session_usage`, `session_report.accounting` is mandatory. Its usage values must match the packet exactly. Use public list pricing in USD per million tokens, with an explicit source and `as_of` date. Cost is `(uncached_input * input_rate + cached_input * cached_rate + cache_write_input * cache_write_rate + output * output_rate) / 1_000_000`; output already includes reasoning output, so do not charge reasoning twice.
- Each evidence reference is an exact packet tuple: `evidence_id`, current `session_id`, `jsonl_line`, `source_hash`, and `summary`. The summary must equal the packet summary exactly; do not paraphrase it inside the evidence reference.
- Every changed entity needs non-empty current-packet evidence. Final `source_sessions` for decisions, open loops, and current state must include the current session.
- Add exactly one `evidence_link` for every changed-entity/evidence pair, with no link to unchanged or unbound data. Use `supports`, `verifies`, or `contradicts`. An inference or pending-confirmation timeline upgraded to verified needs a `verifies` link.
- Proposal and packet text must remain free of new redaction findings. Never restore redacted content.

## Identity, revisions, and transitions

- IDs are stable and globally unique across current state, decisions, open loops, timeline events, and session reports. Do not change an existing entity's identity or project.
- New decisions use initial revision `1` and status `proposed` or `accepted`. A decision update names an existing ID, sets `expected_revision` to its exact current revision, supplies replacement evidence, makes a real change, and becomes current revision plus one. Status transitions are `proposed -> accepted|archived`, `accepted -> superseded|archived`, and `superseded -> archived`; archived is terminal.
- New open loops use initial revision `1` and status `open` or `blocked`. Updates require the exact `expected_revision`, replacement evidence, a real change, and revision plus one. Transitions are `open <-> blocked`, `open|blocked -> resolved|abandoned`, and `resolved|abandoned -> archived`; archived is terminal.
- New timeline events use revision `1`; updates use exactly current revision plus one. Their decision/open-loop IDs must exist after this proposal. A timeline change also needs current-packet evidence.
- Every `supersedes` target must be an existing same-project decision. Reject self-reference, duplicates, missing targets, and every supersedes cycle.

## Current state and session report

- The `current-state` patch is mandatory and non-empty. Its `expected_revision` equals the accepted current-state revision; both `evidence` and `source_sessions` are present, evidence is current-packet evidence, source sessions include the current session, the result is not a no-op, and the accepted revision increments by one.
- A session has one stable report identity. Create at revision `1`; update the same project/session report at exactly current revision plus one. The report and phase evidence together must include current-packet evidence with exact tuples.
- For the first accepted session report, set both session-link fields to empty strings. For every new later report, set `previous_session_id` to the accepted terminal session and keep `next_session_id` empty; apply atomically increments the previous report revision and writes its reciprocal next link. Existing report updates preserve both links.
- `decisions_added`, `decisions_revised`, `open_loops_created`, and `open_loops_closed` are sorted exact packet effects: no omissions, extras, or stale IDs. A closed loop is precisely a changed `open`/`blocked` loop that becomes `resolved`/`abandoned`; archiving an already terminal loop is not a newly closed loop.
