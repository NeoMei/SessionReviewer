# Zero-token Session memory analysis architecture

Date: 2026-08-31
Status: approved design, pending implementation plan

## Problem

SessionReviewer currently makes an ephemeral Agent proposal the critical path for project-wide Session processing. That design couples deterministic work—discovering Sessions, parsing JSONL, extracting files, commands, commits, tests, errors, artifacts, and usage—with probabilistic semantic generation. A malformed evidence tuple, revision, evidence link, or text value rejects the entire proposal and stops the project queue.

The real AgentWiki scan demonstrated the resulting product failure. The job safely accepted only part of a 154-Session queue, consumed substantial review-run tokens, and repeatedly required manual retries for different proposal-validation failures. The validator correctly prevented untrusted output from being written, but the system could not reliably finish the deterministic task the user requested.

Project-wide scanning must therefore stop depending on Agent output. SessionReviewer should become a deterministic history and memory-analysis foundation first. Agent interpretation remains valuable, but only as an optional, replaceable semantic overlay over a complete local fact base.

## Goals

- Scan every discoverable project Session without invoking an Agent or consuming model tokens.
- Process data in two deterministic passes: per-Session fact extraction, then project-level reduction.
- Preserve the full structured results of both passes as long-lived analysis data, not disposable rendering caches.
- Generate the current concise Obsidian project review, history, evolution, status, and usage views from deterministic data.
- Keep every persisted fact traceable to a Session source location and content hash.
- Support incremental, resumable, cross-platform rescans without reprocessing unchanged Sessions.
- Isolate malformed, missing, or unsupported Sessions so one source cannot stop the rest of the project.
- Make future Agent memory analysis, failure-pattern analysis, preference extraction, and skill discovery possible without redesigning the storage foundation.
- Allow an Agent to adjust or enrich the compact project result on demand without controlling scan completion or overwriting machine facts.

## Non-goals

- Producing human-quality causal explanations without an Agent.
- Inferring decisions, intent, or rationale when they are not explicitly represented by reliable source evidence.
- Copying every raw tool payload or unredacted conversation byte into the project store.
- Building a cross-project global memory database in the first implementation.
- Reorganizing the existing Obsidian information architecture or greatly increasing the amount of information shown there.
- Preserving the current Agent proposal job as the default project-scanning mechanism.
- Making SQLite, embeddings, a vector database, or a local model mandatory for the first implementation.

## Confirmed product decisions

- The default pipeline is `Session JSONL -> SessionFact -> ProjectSnapshot -> Obsidian projection` and uses zero model tokens.
- Every frozen Session reaches a terminal deterministic state: indexed, unsupported, missing, or unreadable. Nothing is silently skipped.
- “Scan complete” means every frozen Session has been processed deterministically. It does not mean every Session has an AI narrative.
- First-pass Session facts and second-pass project snapshots are durable, versioned data assets for future intelligent-agent history analysis.
- The project-local memory store is authoritative. A future global catalog may reference project stores but does not replace or merge them by default.
- Obsidian remains a concise projection. Limited display does not imply limited retention.
- Agent enrichment is manual or explicitly requested, reads compact selected data, writes a separate evidence-bound overlay, and never blocks scanning or synchronization.
- Existing accepted ledger content is retained as legacy semantic material during migration, while a fresh zero-token scan reconstructs authoritative machine facts from source Sessions.

## Authority and layer model

SessionReviewer uses five explicit layers:

```text
L0  Source manifests and authenticated Session JSONL boundaries
                         |
                         v
L1  Versioned SessionFact bundles
                         |
                         v
L2  Versioned ProjectSnapshot reductions
                         |
              +----------+----------+
              |                     |
              v                     v
L3  Optional semantic overlays   L4 deterministic Obsidian projection
              |                     ^
              +----------+----------+
                         |
                         v
                  rendered project context
```

### L0: source authority

Original Session JSONL remains the primary evidence source while it exists. The memory store records authenticated source identity, frozen upper boundary, line hashes, source format, and discovery metadata. A source pointer is never treated as proof without its recorded boundary and digest.

### L1: Session facts

`SessionFact` is the durable normalized representation of one bounded Session revision. It contains deterministic facts, ordered event structure, safe text needed for future analysis, provenance, and explicit diagnostics. It is not merely the subset currently rendered in Obsidian.

### L2: project snapshots

`ProjectSnapshot` is a deterministic reduction over one exact set of SessionFact digests. Snapshots are immutable and versioned. A new scan generation creates a new snapshot rather than destructively rewriting historical aggregate state.

### L3: semantic overlays

An Agent may add narrative, rationale, decision interpretation, phase labels, preference hypotheses, or memory recommendations. An overlay is non-authoritative, evidence-bound, versioned, and replaceable. It cannot modify L0-L2.

### L4: Obsidian projection

The current human-facing review and history are rendered from one ProjectSnapshot plus an optional compatible overlay. Markdown and plugin view data are derived outputs and may be rebuilt at any time.

## Project-local memory store

The canonical project store lives under the existing project-owned SessionReviewer directory:

```text
.session-reviewer/memory-v1/
├── manifest.json
├── session-index/
│   └── <session-id>.json
├── facts/
│   └── <content-sha256>.json
├── snapshots/
│   └── <snapshot-sha256>.json
├── overlays/
│   └── <overlay-sha256>.json
└── diagnostics/
    └── <scan-generation>.json
```

Content-addressed objects are immutable. Small mutable indexes point to the current object digests and are updated atomically. Repeated identical content is stored once. A source append creates a new SessionFact object and updates only that Session's index entry. Old objects remain reachable through generation history until an explicit future retention policy safely compacts them.

The first implementation uses canonical JSON objects and per-Session shards rather than a mandatory database. This keeps the store inspectable, recoverable, and portable across macOS, Windows, and Linux. A future SQLite or full-text cache may be derived from the canonical store for faster querying, but it is never the sole authority.

Every stored object includes:

- schema version;
- extractor or reducer version;
- project identity;
- source or parent object digests;
- deterministic object digest;
- creation generation;
- provenance and diagnostic counts; and
- no host-specific transient path as semantic identity.

## First pass: deterministic Session extraction

### Freeze and discovery

A scan freezes the ordered Session set and each Session's readable upper boundary before extraction. Sessions appended after the boundary remain pending for the next generation. Discovery records unsupported or unreadable candidates rather than silently excluding them.

### Extracted fact classes

The extractor records at least:

- Session identity, timestamps, source format, model, and token usage;
- ordered user, assistant, and system Turn metadata;
- safe normalized user and assistant text needed for later memory analysis;
- file reads, creations, modifications, deletions, and referenced paths;
- commands, arguments in safe normalized form, exit status, and duration;
- Git branch, status, commits, tags, and release-related operations;
- tests, builds, lint, verification commands, and observed outcomes;
- errors, typed error codes, normalized signatures, and source positions;
- tool calls and result status;
- generated artifacts and their source relationships;
- explicit version, package, deployment, and synchronization facts; and
- event-to-event ordering and source provenance.

Facts never claim inferred motivation or semantic importance. An explicit commit, test success, file change, or user statement is a fact; “this was the key architectural decision” is an overlay interpretation unless an authoritative structured marker says so.

### Safe text retention

Future memory analysis requires more than short UI excerpts, so L1 retains normalized safe conversational text where permitted. Credential material, connection URLs, private keys, and other sensitive spans are deterministically redacted before persistence. Large duplicated tool payloads are represented by typed metadata, bounded safe excerpts, and authenticated hashes rather than copied wholesale.

Protocol-owned IDs, hashes, timestamps, and enums are parsed as typed fields before prose scanning. This prevents valid machine identities from being misclassified as high-entropy prose while preserving strict detection for actual text.

### Incremental behavior

Each Session index entry records source identity, frozen boundary, hash-chain state, parser version, current fact digest, and terminal status.

- Unchanged source and parser version: reuse the existing fact object.
- Valid append after an authenticated boundary: parse only the successor range and emit a new fact revision.
- Interior mutation or incompatible parser change: rebuild only that Session.
- Missing or unreadable source: preserve the previous accepted fact revision, record the new diagnostic state, and do not pretend the source was rescanned.
- Unsupported format or malformed records: retain bounded diagnostics and continue processing all other Sessions.

Independent Sessions may be extracted with bounded parallelism. Object publication and index updates remain atomic and deterministic.

## Second pass: deterministic project reduction

The reducer consumes the exact ordered set of current SessionFact digests from one scan generation. It never opens Agent output and never scans raw Sessions itself.

### Aggregate outputs

`ProjectSnapshot` contains:

- Session coverage, terminal-state counts, time range, and generation identity;
- current witnessed branch, version, Git status, and verification state with timestamps;
- chronological Session and event timeline;
- files and modules ranked by transparent frequency and recency scores;
- commits, tags, releases, deployments, and artifacts;
- test, build, lint, and synchronization outcomes;
- normalized errors and later recovery evidence;
- model and token accounting;
- recent activity and first inspection pointers;
- explicit unresolved diagnostic signals;
- stable references back to contributing SessionFact and event IDs; and
- differences from the preceding ProjectSnapshot.

### Deterministic reduction rules

- Current state uses the newest witnessed fact for a field and carries its observation time. Stale facts are labeled rather than presented as current truth indefinitely.
- Repeated facts are deduplicated by stable typed identity and provenance, not natural-language similarity.
- A failure is marked recovered only when a later compatible success signal exists for the same normalized component or operation. Ambiguous cases remain unresolved.
- Version changes, tags, release commands, branch transitions, and bounded time gaps create transparent phase boundaries. The reducer does not invent phase names that require semantic judgment.
- Module importance uses a documented score derived from touch frequency, Session coverage, recency, and verification activity.
- Project changes are computed from snapshot-to-snapshot object differences, making project evolution reproducible.
- Unknown or unsupported data contributes diagnostics and coverage information but cannot block reduction of valid facts.

The complete aggregate remains durable even when only a small subset is projected to Obsidian.

## Obsidian and Markdown projection

The existing information volume and navigation remain the target. The deterministic renderer continues to produce the current project review and history surfaces, including:

- project review;
- project history;
- evolution timeline;
- current status;
- key witnessed events and recent progress; and
- model and token usage.

No per-Session Markdown explosion is introduced. Session details remain in the local memory store and are retrieved only when a future analysis or drill-down requires them.

Sections that require interpretation behave honestly:

- With no semantic overlay, they show explicit facts and deterministic signals only.
- With a compatible overlay, they may show Agent-authored narrative and interpretation with evidence references.
- Missing interpretation is not filled with invented decisions or causal claims.

Rendering is a pure function of snapshot digest, optional overlay digest, and renderer version. Re-rendering the same inputs produces byte-identical machine objects and semantically stable Markdown identities.

## Optional Agent semantic enrichment

Agent enrichment is removed from the scan critical path. It is an explicit later operation over a selected ProjectSnapshot.

By default the Agent receives:

- the compact ProjectSnapshot;
- only the selected SessionFact or event slices needed for requested enrichment;
- existing compatible overlay context; and
- a small overlay schema that excludes machine-owned fact fields.

It does not receive all raw project Sessions unless the user explicitly requests a bounded deep analysis that requires them.

Allowed overlay content includes:

- natural-language project narrative;
- interpreted project phases;
- decision and rationale hypotheses;
- recurring failure patterns;
- user preference hypotheses;
- agent or tool effectiveness observations;
- memory candidates; and
- suggested skills, rules, or future inspections.

Every assertion cites L1 or L2 fact IDs. The host fills snapshot identity, provenance, revision, and accounting fields. Unknown evidence IDs, sensitive text, or invalid shape reject only that overlay attempt. The deterministic scan, current snapshot, and Obsidian projection remain available.

Overlay cache identity includes snapshot digest, selected fact digests, overlay schema version, and analysis profile. A previously successful overlay may be reused only for the exact compatible identity.

## Completion, diagnostics, and failure isolation

The zero-token scan lifecycle is:

```text
queued -> discovering -> extracting -> reducing -> rendering -> syncing
       -> completed
       -> completed_with_issues
       -> failed
```

`completed_with_issues` means every frozen Session reached a terminal deterministic state, but one or more were unsupported, missing, unreadable, or malformed. The UI reports exact safe counts and allows inspection of bounded diagnostics. It does not describe those Sessions as successfully indexed.

`failed` is reserved for project-wide integrity failures such as an unauthenticated project identity, inability to publish the canonical store atomically, corrupted manifest lineage, or failure to render/sync an otherwise committed generation. One malformed Session is never a project-wide failure.

Agent enrichment has its own independent status and cannot change scan completion. A project may therefore display:

```text
结构化扫描：154 / 154 已处理
成功索引：151
需要处理：3
AI 深度分析：未运行
```

## Migration from the Agent-first ledger

Migration is additive and recoverable:

1. Authenticate the existing project mapping and preserve the current ledger, history, review, and job records.
2. Import accepted legacy narrative and decisions as a versioned `legacy` semantic overlay; do not reinterpret them as deterministic facts.
3. Freeze and scan all discoverable source Sessions through the new L0-L2 pipeline.
4. Build the first ProjectSnapshot and compare render identities with existing Obsidian content.
5. Switch the default project update action to the zero-token scan and deterministic sync path.
6. Keep the old Agent review job visible as historical status, but do not require it to resume or complete the new scan.
7. Permit an explicit later Agent enrichment to reconcile useful legacy narrative against the new fact IDs.

The existing partially accepted 13-Session AgentWiki review is therefore preserved but no longer controls project coverage. All 154 source Sessions are independently eligible for zero-token indexing.

## Future global memory analysis

The first implementation keeps each project's fact store physically and logically separate. A future global catalog may contain only:

- project identity and display name;
- canonical store locator under the user's configured roots;
- current manifest and snapshot digest;
- schema/version compatibility;
- safe coverage and update timestamps; and
- user-controlled analysis permissions.

Cross-project analysis resolves selected project stores through that catalog and builds a separate evidence-bound result. It does not silently copy all project history into one shared database. This preserves project isolation while enabling later questions such as recurring agent failures, reusable user preferences, effective tools, and memory candidates across projects.

## Security and privacy

- Scanning is local and makes no model or network call.
- Every persisted text value passes deterministic redaction before publication.
- Typed protocol fields are validated by field grammar and are not treated as prose.
- Source paths are normalized and retained only where required for local provenance; public/plugin projections use safe identities.
- Raw credentials and unbounded tool output are never copied into the canonical memory store.
- Obsidian receives only the current concise projection, not the complete analysis store.
- Agent enrichment receives a minimal selected projection and cannot mutate L0-L2.
- Diagnostics store rule names, typed locations, and counts without secret values.

## Compatibility and versioning

L0-L3 objects have independent schema versions. Extractor and reducer versions are recorded so upgrades can rebuild only affected layers. Readers reject unknown required schema versions without deleting older objects.

The project memory store is forward-migrated by writing new immutable objects and an atomic manifest generation. Migration never edits old content-addressed objects in place. Interrupted migration leaves the preceding manifest generation authoritative.

Obsidian consumes a versioned public projection rather than reading the private fact store directly. Existing review/history layout and stable event identities are preserved where the underlying fact identity is unchanged.

## Acceptance criteria

### Zero-token completeness

- A 154-Session project scan invokes no Agent adapter and records zero review-run model tokens.
- Every frozen Session reaches indexed, unsupported, missing, or unreadable state.
- Injected malformed Sessions do not prevent later Sessions from being processed.
- The final snapshot coverage exactly reconciles with the frozen Session set.

### Determinism and incrementality

- Identical source boundaries and component versions produce byte-identical canonical SessionFact and ProjectSnapshot objects.
- Appending one Session changes only that Session's fact revision, affected indexes, and successor project snapshot.
- Restart after interruption resumes from published objects without duplicating facts or losing lineage.
- macOS, Windows, and Linux fixtures produce equivalent canonical identities.

### Data value and traceability

- Every aggregate claim links to contributing fact IDs.
- Every fact links to an authenticated Session source boundary and location.
- Historical fact and snapshot versions remain queryable after later scans.
- The complete L1/L2 data survives even though Obsidian renders only the current concise projection.

### Security

- Credential canaries and private-key fixtures never appear in facts, snapshots, overlays, Markdown, plugin JSON, or diagnostics.
- Valid typed IDs and hashes do not trigger prose high-entropy rejection.
- Unrecognized high-entropy prose is redacted or rejected according to its layer contract.
- Agent overlay failure cannot alter scan state, facts, snapshots, or deterministic Markdown.

### Projection and migration

- Current project review, history, evolution, status, and usage remain available after migration.
- Existing ledger narrative is preserved as legacy overlay material.
- The partially completed Agent job is not required to scan all source Sessions.
- With AI enrichment disabled or unavailable, Obsidian still displays a complete deterministic project context.

## Delivery sequence

Implementation planning should preserve these dependency boundaries:

1. canonical memory-store schemas and atomic content-addressed storage;
2. deterministic Session discovery and first-pass extraction;
3. deterministic project reducer and versioned snapshots;
4. Markdown/public projection and Obsidian integration;
5. legacy-ledger migration and real AgentWiki full-scan acceptance;
6. optional semantic overlay generation; and
7. future query/global-catalog work as a separate project.

The first releasable milestone ends after step 5. Agent enrichment is not required to prove that project-wide scanning is complete, useful, and zero-token.
