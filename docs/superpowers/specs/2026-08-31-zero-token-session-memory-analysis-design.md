# Zero-token Session memory analysis architecture

Date: 2026-08-31
Status: final approved design, pending written-spec review

## Problem

SessionReviewer currently puts an ephemeral Agent proposal on the critical path for project-wide Session processing. Deterministic work—discovering Sessions, parsing JSONL, extracting files, commands, commits, tests, errors, artifacts, and usage—is coupled to probabilistic semantic generation. A malformed evidence tuple, revision, link, or text value rejects the proposal and stops the project queue.

The real AgentWiki scan demonstrated the product failure: only part of a 154-Session queue was accepted, review-run token usage became large, and different proposal-validation errors required repeated manual retries. The validator correctly prevented unsafe writes, but the system could not reliably finish the deterministic scan.

Project-wide scanning must not depend on Agent output. SessionReviewer should first become a deterministic history and memory-analysis foundation. Agent interpretation remains useful as an optional enhancement. Human-edited project-review content remains editable and has the highest authority over what is presented to people.

## Goals

- Scan every discoverable project Session without invoking an Agent or consuming model tokens.
- Process data in two deterministic passes: per-Session materialization, then project-level reduction.
- Preserve both passes as long-lived, versioned analysis assets without duplicating complete raw Session text.
- Keep one unambiguous authority for machine-observed facts, with every deterministic derived claim isolated in a dependency-bound view.
- Generate the current concise Obsidian project review, history, evolution, status, and usage presentation.
- Preserve human editability of the rendered review/history and give human edits highest presentation precedence.
- Support incremental, resumable, cross-platform rescans without rewriting unchanged data.
- Isolate malformed, missing, unsupported, and cross-project records so one source cannot stop or contaminate the project scan.
- Support future Agent memory analysis, failure-pattern analysis, preference extraction, and skill discovery without redesigning the storage foundation.
- Allow optional Agent enrichment without controlling scan completion or overwriting machine facts or human presentation decisions.

## Non-goals

- Producing human-quality causal explanations without a human or Agent.
- Inferring decisions, intent, or rationale when no reliable evidence or approved semantic content exists.
- Copying complete Session transcripts or complete tool outputs into SessionReviewer storage by default.
- Automatically archiving original Agent Session files.
- Building a cross-project global memory database in the first implementation.
- Expanding Obsidian into a memory database, analysis console, or configuration surface unrelated to project-scan presentation.
- Greatly increasing the amount of information shown in Obsidian.
- Preserving the current Agent proposal job as the default project-scanning mechanism.
- Making SQLite, embeddings, a vector database, or a local model mandatory for the first implementation.

## Confirmed product decisions

- The default pipeline is zero-token and has two passes: Session materialization followed by project reduction.
- `ObservationStore` is the sole authority for machine-observed facts. Deterministic derived claims live only in dependency-bound `SessionView` or `ProjectView` records and never flow back into `ObservationStore`.
- `SessionView` and `ProjectView` are durable, versioned materialized views, not competing authorities.
- The complete private memory store lives in the platform SessionReviewer data root and remains logically isolated per project.
- Full original Session text is not duplicated. Facts retain typed provenance, hashes, source references, and only bounded safe excerpts needed for presentation.
- Original source disappearance does not trigger automatic backup. Retained facts remain available and the source is marked unavailable.
- A Session that touches multiple projects is partitioned at Observation granularity rather than assigned wholesale to one project.
- New versions are created only when input fact digests change.
- Agent semantic content is stored as granular dependency-bound annotations.
- Human-edited review/history content is a `HumanPresentation` layer with highest presentation precedence.
- Human edits are represented as field-level patches with explicit set, suppress, and restore-default operations, while unknown custom Markdown blocks remain byte-preserved.
- Obsidian is a human-readable presentation and editing container for scan results, not the machine-fact authority.
- Private generations and Project/Vault projections are published through a durable journal; a generation is not authoritative until every required destination verifies the same generation and hashes.
- Public projection schema v3 declares a minimum writer version, and older writers fail closed rather than downgrade or overwrite it.
- A zero-token, read-only `ProjectProbe` records live project state separately from Session history.
- Shared Session usage is stored once in `SourceCatalog`; project views report associated usage with an explicit shared marker, while future global totals deduplicate by Session ID.
- Human presentation authority never bypasses structural, identity, or security checks during Project/Vault propagation.
- Observation identity separates a stable fact key from immutable extractor revisions; each generation selects the active revision set without deleting history.
- Human patches remain authoritative when their generated baseline changes, but do not silently transfer to a replaced or deleted entity.
- A scan freezes human presentation and source boundaries, then uses destination preimage hashes to prevent concurrent edits from being overwritten.
- Published Observation and view history is retained by default; automatic cleanup is limited to unreachable, never-published staging objects and duplicate cache.
- The first public release passes three internal gates: zero-token core, schema-v3 projection/sync, and migration plus real-project acceptance. AgentAnnotation remains a later milestone.
- A stable `project_id` survives path moves and verified worktrees; paths and remotes are authenticated aliases, never implicit merge authority.
- Existing accepted ledger content is preserved as human or legacy semantic presentation in the first release; a fresh zero-token scan reconstructs machine facts from all source Sessions.

## Refined architecture

```text
Agent Session sources
        |
        v
SourceAdapter + content-free SourceCatalog
        |
        v
ObservationStore                    sole machine-observation authority
        |
        +--------------------+
        |                    |
        v                    v
SessionView              ProjectView     durable materialized views
                              |
                    +---------+----------+
                    |                    |
                    v                    v
            AgentAnnotation      HumanPresentation
                    |                    |
                    +---------+----------+
                              |
                    precedence resolution
                              |
                              v
                 Markdown / Obsidian presentation
```

Presentation precedence is:

```text
HumanPresentation > valid AgentAnnotation > deterministic ProjectView
```

This precedence applies to editable semantic and presentation fields such as project goal, stage, current status, next action, decision wording, risk explanation, summaries, and display order. It does not rewrite immutable observed facts such as source identity, commit hash, command exit code, test outcome, file event, timestamp, or token accounting. Those facts remain queryable even when a human chooses a different presentation.

## Source adapters and source catalog

Session formats are provider-specific, but the long-lived memory model is provider-neutral.

```text
CodexSourceAdapter
OpenCodeSourceAdapter       future
ClaudeCodeSourceAdapter     future
        |
        v
canonical Observation stream
```

The first implementation supports Codex only, but uses this interface boundary:

```text
Discover(project mapping) -> source candidates and issues
Freeze(candidate) -> authenticated readable boundary
Decode(boundary) -> canonical observations and diagnostics
Read(ref) -> bounded source material for explicit later analysis
```

The platform-level `SourceCatalog` stores no conversation body. It records provider, Session identity, authenticated physical source identity, time range, available boundary, source availability, and project-affinity metadata. It prevents duplicate discovery work and lets project stores refer to one source without copying the original Session.

`project_id` is assigned by authenticated project registration and does not derive from the current filesystem path. A moved project or a verified worktree adds or updates a rooted alias to the same ID. Remote URL, Git common-directory identity, and existing mapping evidence may authenticate an alias, but no single mutable path or remote string may silently merge two project stores. Conflicting identity evidence quarantines the alias until the user explicitly re-associates it.

## ObservationStore: sole machine-observation authority

An Observation is a typed, immutable record of something directly witnessed in a Session source. `ObservationStore` contains only observed records. It never stores reducer output or a conclusion derived from other observations.

Every Observation revision contains:

- stable observation key;
- immutable observation revision ID;
- project ID and Session ID;
- provider and source reference;
- source line/range and authenticated source hash;
- event time and sequence;
- typed kind, subject, operation, object, outcome, and relevant structured fields;
- certainty: always `observed` in this store;
- provenance: source-adapter ID and version;
- bounded safe excerpt only when required for human presentation;
- redaction diagnostics without sensitive values.

Source availability is mutable catalog state, not part of an immutable Observation revision. `SourceCatalog` and each frozen generation record whether the referenced source boundary was available when checked.

Examples:

```text
observed: command exited with code 0
observed: file path was modified
observed: commit hash was created
derived: excluded from ObservationStore; belongs to a dependency-bound SessionView or ProjectView
semantic: excluded from ObservationStore; belongs to AgentAnnotation or HumanPresentation
```

Deterministic conclusions such as compatible failure recovery, rankings, phase boundaries, and current-state selection are versioned view fields. Each carries the exact Observation or lower-level view dependencies and reducer rule/version that produced it. Derived output is never written back as Observation input, preventing circular authority and self-reinforcing reductions.

### Observation identity and supersession

The stable observation key identifies one canonical fact slot from authenticated provider, Session, source event coordinate, project association, kind, and typed subject identity. It excludes adapter version and extracted payload. The immutable revision ID hashes that key together with the normalized payload, source hash, and adapter version.

Each Session has one immutable content-addressed `SessionLineage` head containing that Session's bounded active-revision map and only the `superseded` or `withdrawn` delta from its preceding head. A generation references one lineage head beside each SessionView; it never repeats project-wide observation chunks or revision maps. Re-decoding after an adapter upgrade or interior source mutation emits successor revisions and selects them in the affected Session's new head. Prior revisions remain queryable as inactive history and are never mutated in place. SessionViews, ProjectViews, and AgentAnnotations bind to exact revision IDs, so a superseded dependency invalidates only the affected derived view or annotation.

### Text and raw-source policy

SessionReviewer does not copy complete user/assistant transcripts or complete tool output into the private store.

It retains:

- structured facts extracted from the text or tool envelope;
- source references and hashes;
- bounded, redacted excerpts required for current project history presentation; and
- normalized signatures needed for errors, commands, verification, files, versions, and artifacts.

An explicit later Agent analysis retrieves only selected source spans through `SourceAdapter.Read`. If the original source no longer exists, the retained facts and excerpts remain usable, the source is marked `source_unavailable`, and deep reconstruction is honestly limited. No automatic raw archive is created.

Typed protocol IDs, hashes, timestamps, paths, and enums are parsed before prose redaction. Valid machine identities therefore cannot be misclassified as high-entropy prose.

## Private project-scoped storage

The complete fact and view store lives outside Project and Vault:

```text
<SessionReviewer data>/
├── source-catalog/
├── publication-journal/
└── projects/
    └── <project-id>/
        └── memory-v1/
            ├── manifest.json
            ├── observations/
            │   └── <chunk-sha256>.jsonl
            ├── sessions/
            │   └── <session-id>.json
            ├── project-views/
            │   └── <view-sha256>.json
            ├── annotations/
            │   └── <annotation-sha256>.json
            └── diagnostics/
                └── <scan-generation>.json
```

Directories and records use the existing private platform-data permissions, rooted path validation, identity pinning, atomic private-object writes, and project locks. Complete facts are not placed in Git, Project Markdown, or the Obsidian Vault.

Observation chunks are immutable and content-addressed. A Session append writes only successor chunks. SessionViews reference old and new chunks instead of copying all prior facts. Decode output is first validated into private, per-source typed-observation spools; replay keeps at most one source payload resident, removes every run on success/error/cancellation/panic, and rejects identity, permission, size, timestamp, or canonical-content changes during replay. The per-source ceiling remains 65,536 revisions, but there is no project-global observation ceiling.

ProjectViews contain bounded compact aggregates and references rather than duplicating complete Session observations. Their reducer performs a chronological k-way merge with one cursor per Session. Witnessed state, recovery candidates/results, phase boundaries, event references, Session-derived records, and module candidates each have explicit resident/output bounds. Every bounded channel except cross-Session recovery carries content-addressed coverage metadata with exact streamed candidate, emitted, collapsed, dropped, evicted, and truncation counts as applicable; cross-Session recovery coverage counts decided candidates only — pending-bound rejections and failures matched by a later cross-Session success — because a failure admitted to pending but never matched receives no selection decision. The view also records the exact ObservationSummary total and selected-evidence coverage. Module candidates use deterministic Space-Saving admission: per-candidate `estimated_activity` and `error_upper_bound` are estimates, and `counts_complete=false` whenever eviction or Session-cardinality truncation means exact per-module counts cannot be claimed, while the channel's own admission/eviction coverage remains exact. This metadata describes ProjectView selection only: authoritative retained facts remain complete in each SessionView and SessionLineage.

Unchanged scans produce no new Observation, SessionView, or ProjectView version; a bounded scan-run/ProbeCheck record may still record that the check occurred. A SessionView is versioned only when its active Observation revision digest, frozen source-availability digest, or materializer version changes. A ProjectView is versioned only when its ordered SessionView dependency digest, ProjectProbe state digest, or reducer version changes.

All objects reachable from a committed generation or committed lineage are retained by default because historical revisions are future analysis assets. Automatic cleanup is limited to duplicate cache and never-published staging objects that are unreachable from every committed generation, prepared generation, and live publication journal for at least seven days. The CLI reports private-store size, reachable history, and cleanup candidates. Deleting committed history or compacting lineage is a future explicit operation with dry-run and reference verification, not first-release automatic behavior.

Private immutable objects can be committed atomically within the SessionReviewer data root, but the private store, Project files, and Obsidian Vault cannot share one filesystem transaction. Publication therefore uses a durable, restartable journal:

1. write and verify a complete private generation as `prepared`, without changing `published_generation`;
2. durably record a publication intent containing the generation ID, all destination preimage hashes, and all expected output hashes;
3. update the Project projection only if its preimage still matches;
4. use the existing Project/Vault sync transaction to update the Vault projection only if its preimage still matches;
5. reopen and verify the private manifest, Project projection, Vault projection, schema, generation ID, and hashes; and
6. atomically switch `published_generation` and mark the journal entry `committed`.

Only one scan generation may build for a project at a time. At scan start, a short sync lock resolves any existing Project/Vault conflict, parses the current human presentation, freezes its digest, and freezes source boundaries. The long extraction phase does not hold the human-edit lock. At publication, the sync lock is reacquired and every destination preimage must still match the frozen digest.

On restart, the journal either completes the same intent idempotently or restores presentation from its recorded preimages before abandoning the prepared generation. Rollback is compare-and-swap: a destination is restored only while its current hash still equals the journaled system-output hash; any other value becomes an explicit conflict. A prepared or partially projected generation is never exposed as the published machine state. Concurrent human edits produce a preimage mismatch and enter explicit conflict handling; the prepared private generation remains reusable, while the edit is never overwritten by publication or recovery.

## Project association and cross-project Sessions

A complete Session is never blindly assigned to its initial working directory.

Each Observation is associated using authenticated context available at that record:

- current working-directory identity;
- rooted file target identity;
- explicit project/session association;
- configured aliases and worktree identities; and
- remote identities where applicable.

Observations that belong to another mapped project go only to that project's store. Provider-level Session metadata may be referenced by multiple projects through the content-free SourceCatalog, but original text is not copied. Ambiguous observations remain quarantined and contribute a safe diagnostic; they are not assigned by guesswork.

Provider-level model and token usage belongs to the Session as a whole and is stored exactly once in `SourceCatalog`. It is not divided among projects using guessed ratios. Each contributing project may display that Session's usage as `associated_usage` with `shared=true`; the project total is therefore explicitly an associated-usage total, not an exclusive cost allocation. Future cross-project/global totals deduplicate by provider and Session ID before summing.

## First pass: SessionView materialization

A scan freezes the ordered source candidates and readable boundaries. The first pass converts each project's Observations into one durable SessionView.

SessionView includes:

- Session identity, provider, time range, the current SourceRecord digest, and an independently authenticated digest of normalized SourceCatalog model/token usage;
- coverage and source-availability state;
- ordered references to observations and chunks;
- user-request presentation excerpts;
- files, commands, commits, branches, versions, tests, errors, tools, and artifacts;
- observed outcomes and explicit derived recovery links;
- current terminal status and bounded diagnostics; and
- dependency digest and materializer version.

Every frozen Session reaches one state:

```text
indexed | unsupported | missing | unreadable | ambiguous
```

One failing Session never blocks later Sessions. Independent Sessions may be decoded with bounded parallelism; publication remains atomic per project generation.

Incremental rules are:

- unchanged source and adapter version: reuse observations and SessionView;
- authenticated append: decode only the successor boundary and publish successor chunks;
- interior mutation or incompatible adapter change: rebuild only the affected Session observations and view;
- missing source: preserve prior facts, mark source unavailable, and do not claim a fresh scan;
- malformed or unsupported records: retain diagnostics and continue the frozen queue.

## Second pass: ProjectView reduction

The reducer consumes the exact ordered SessionView dependency set for one project generation. It never opens raw Sessions and never calls an Agent.

ProjectView contains:

- Session coverage and terminal-state counts;
- project time range and change generation;
- current Session-witnessed branch, version, Git status, and verification state with observation times;
- current read-only ProjectProbe state and state digest;
- chronological Session and event timeline;
- files/modules ranked by documented frequency and recency rules;
- commits, tags, releases, deployments, and artifacts;
- tests, builds, lint, sync, and verification outcomes;
- errors, normalized signatures, and compatible later recovery links;
- recent activity, source availability, and inspection pointers;
- associated model and token accounting, including shared-Session markers;
- deterministic differences from the preceding ProjectView; and
- references to every contributing SessionView and Observation.

Reduction rules are transparent and versioned:

- newest witnessed value wins only for machine-observed current-state fields and carries its observation time;
- repeated facts are deduplicated by typed identity and provenance, never natural-language similarity;
- a failure is recovered only by a later compatible success for the same normalized operation/component;
- versions, tags, releases, branches, and bounded time gaps create structural phase boundaries without invented semantic names;
- module ranking uses documented frequency, Session coverage, recency, and verification activity;
- ambiguous data contributes diagnostics, not guessed facts.

ProjectView is the complete deterministic second-pass result. Obsidian renders only its concise human-facing subset.

The private JSON Schemas describe interoperable Unicode code-point lengths, while the Go runtime intentionally enforces stricter valid-UTF-8 byte bounds and rejects all C0/DEL control characters in structured single-line fields. Schemas exclude standard controls where expressible, but non-ASCII text remains valid. External schema acceptance therefore does not replace runtime validation or content-addressed-store revalidation.

## Read-only ProjectProbe

Session history alone cannot prove the project's state at scan time. After Session materialization and before ProjectView reduction, a zero-token `ProjectProbe` produces a content-addressed `ProjectProbeState` and a generation-local `ProbeCheck`.

`ProjectProbeState` records:

- project identity and canonical root;
- Git branch, HEAD, worktree status, and configured remote identity when available;
- declared version-file paths and content hashes;
- existence and hashes of files required by the public projection contract;
- probe version and typed state diagnostics; and
- a state digest that excludes wall-clock check time.

`ProbeCheck` records the check time, state digest, and read/availability diagnostics for the current scan generation. ProjectView depends only on the state digest. Therefore checking an unchanged project updates scan/check status without creating a new ProjectProbeState or ProjectView version.

The probe does not run tests, builds, scripts, package installation, network requests, or dependency resolution. It does not reinterpret prior Session events. ProjectView keeps live probe values and historical Session-witnessed values distinct so the UI cannot claim that a present Git state proves a historical release, deployment, or test outcome.

## Granular Agent annotations

Agent analysis is removed from the scan critical path. It is an explicit later operation over one selected ProjectView and only the necessary SessionView/Observation/source spans.

AgentAnnotation may contain narrative, phase interpretation, decision rationale, failure patterns, preference hypotheses, memory candidates, or tool-effectiveness analysis. Each annotation:

- has a stable semantic entity and field scope;
- cites exact Observation or SessionView dependencies;
- records schema, analysis profile, and Agent-run identity;
- cannot modify ObservationStore, SessionView, or ProjectView;
- is valid only while every cited revision/view remains active in the selected generation, and is invalidated when a cited dependency is superseded, withdrawn, or changes digest; and
- remains valid when unrelated Sessions are added.

Invalid, unsafe, or unavailable Agent output rejects only that annotation attempt. The zero-token scan and existing presentation remain complete.

## HumanPresentation: highest presentation authority

Project review and history remain human editable in the Project/Obsidian synchronization workflow. Parsed human edits form field-level `HumanPresentationPatch` records, the highest-precedence source for editable semantic and display fields.

Each supported patch records:

```text
HumanPresentationPatch {
  entity_id
  field
  operation: set | suppress | restore_default
  value                         required only for set
  base_generated_hash
}
```

`set` pins the human value, including an intentionally empty value when the field contract permits it. `suppress` intentionally removes a generated item or optional section. `restore_default` removes the human override so normal Agent/ProjectView precedence resumes. The stored `base_generated_hash` distinguishes a human change from unchanged generated text and supports later rebase/conflict diagnostics.

When a later ProjectView changes the generated baseline but preserves the same stable `entity_id` and field contract, `set` and `suppress` continue to win. The patch keeps its original baseline hash and gains an `underlay_changed` diagnostic until the user edits or restores the field; the change is visible but does not weaken human authority. If the entity disappears, is replaced, or changes identity contract, the patch becomes an `orphan_patch`: it is retained and shown for recovery, but is not automatically attached to another entity.

HumanPresentation may override:

- project goal, stage, and status wording;
- next action and first-inspection guidance;
- decision titles, explanations, and status presentation;
- risks, blockers, and explanatory notes;
- timeline titles/summaries; and
- visible selection, ordering, grouping, and emphasis.

HumanPresentation does not delete or rewrite underlying observed facts. If a human presentation conflicts with a machine observation, the human wording remains the primary human-facing view while the observation remains available for traceability and future analysis.

Stable hidden identities and schema-v3 parse/render contracts preserve edits across rescans. Unknown custom Markdown blocks and unsupported fields remain byte-preserved in place; they are not normalized or deleted merely because the scanner does not understand them. The renderer merges supported fields individually:

```text
human value, when present
otherwise valid Agent annotation
otherwise deterministic ProjectView value
otherwise omit the optional section
```

Sync conflicts between Project and Obsidian continue through the existing explicit conflict workflow; the scan does not arbitrarily choose one human edit over another. Human precedence governs accepted presentation meaning, but it does not bypass rooted-path checks, stable entity identity, schema validation, redaction, link safety, or Project/Vault propagation rules. A human edit that is valid locally but unsafe to propagate remains on its originating side and creates an explicit diagnostic/conflict; it is never auto-copied, silently discarded, or converted into a machine fact.

## Obsidian projection boundary

Obsidian is only the human-readable presentation and editing container for project-scan results. The plugin does not expose the private Observation store, build a general memory-analysis UI, or own source parsing.

The current information volume remains the target:

- project review;
- project history and evolution timeline;
- current status and next action;
- selected key decisions and risks when supplied by human or valid Agent semantics;
- recent witnessed progress; and
- model/token usage.

The zero-token ProjectView supplies machine-backed content. HumanPresentation supplies authoritative edits. Optional semantic sections with no human or Agent content are omitted rather than filled with invented conclusions or empty cards.

Usage is labeled as associated project usage. When a Session contributes to multiple projects, the relevant row/card carries a shared marker so a human cannot mistake per-project associated totals for mutually exclusive allocation.

The primary scan action is named `更新项目脉络`. It freezes, extracts, reduces, renders, and synchronizes. Agent deep analysis is not part of this action and need not be implemented as an Obsidian workflow; it may be invoked explicitly through the Agent/CLI and its accepted annotations then appear in the normal projection.

The concise status is:

```text
项目脉络已更新 · 154 个 Session
```

When issues exist:

```text
项目脉络已更新 · 154 个 Session · 151 已索引 · 3 需检查
```

Only `需检查` expands safe scan diagnostics. Obsidian does not display unrelated memory-store internals.

## Completion and failure isolation

The zero-token scan lifecycle is:

```text
queued -> discovering -> extracting -> reducing -> rendering -> syncing
       -> completed
       -> completed_with_issues
       -> failed
```

`completed_with_issues` means every frozen Session reached a terminal deterministic state but one or more were unsupported, missing, unreadable, ambiguous, or malformed. It is a complete scan with explicit coverage issues, not a successful index claim for those sources.

`failed` is reserved for project-wide integrity failures: unauthenticated project identity, inability to prepare or journal a private generation, corrupt manifest lineage, unrecoverable human-presentation parse conflict, or inability to complete/restore a journaled Project/Vault publication. A malformed Session is not a project-wide failure.

Agent annotation status is independent and cannot change scan completion.

## Migration from the Agent-first ledger

First-release migration is additive and recoverable:

1. Authenticate the project mapping and preserve existing review, history, machine ledger, sync state, and historical review job.
2. Freeze and scan every discoverable source Session through ObservationStore and SessionView.
3. Build the first ProjectView from all SessionViews.
4. Parse existing human-edited review/history into HumanPresentation and preserve it at highest presentation precedence.
5. Preserve safe existing Agent-authored decisions and narrative as human-approved or `legacy-unverified` presentation, without promoting it to an observed or derived machine fact.
6. Render through the existing human-readable format and verify stable identities before switching the default action to `更新项目脉络`.
7. Keep the old Agent job as historical status; it is not resumed or required for new scan completion.

A later AgentAnnotation milestone may resolve legacy evidence tuples against active Observation revisions and convert only valid semantics into granular annotations. That optional conversion is not required for first-release scan completion or migration safety.

The partially accepted AgentWiki 13-Session result is preserved as human/legacy semantic material, while all 154 source Sessions are independently eligible for zero-token indexing.

## Future global memory analysis

Each project keeps a physically private, logically independent Observation store under the platform data root. A future global catalog may reference project IDs, private store locators, current dependency digests, schema compatibility, safe coverage, timestamps, and user-controlled analysis permission.

Cross-project analysis reads only explicitly selected stores and produces separately evidence-bound annotations. It does not silently merge project histories or copy complete Session text. This supports future analysis of recurring Agent failures, durable user preferences, effective tools, memory candidates, and reusable skills while preserving project isolation.

## Security and privacy

- Default scanning is local and makes no model or network call.
- Full Session transcripts and full tool outputs are not duplicated.
- Stored excerpts pass deterministic redaction before publication.
- Typed protocol fields are validated by grammar rather than prose entropy rules.
- Private facts/views stay under the platform data root with private permissions.
- Project/Vault receive only human-facing review/history and their existing compatibility metadata.
- SourceAdapter reads for Agent analysis are explicit, bounded, and minimal.
- AgentAnnotation cannot modify machine facts or human presentation.
- Diagnostics store typed locations, rules, and counts without sensitive values.
- Human edits remain visible through presentation precedence but cannot forge the immutable evidence record.

## Compatibility and versioning

Observation, SessionView, ProjectView, AgentAnnotation, HumanPresentation, and public projection have independent schema versions. Adapter/materializer/reducer versions are included in dependency identities so only affected layers rebuild.

Private-store migration writes new immutable chunks/views and publishes them through the durable generation journal. Interrupted migration leaves the prior committed generation authoritative. Readers reject unsupported required versions without deleting existing objects.

The new public Project/Vault projection is schema v3 and declares `minimum_writer_version`. A CLI or Obsidian plugin that cannot parse the schema or does not satisfy the minimum writer version must fail closed before any mutation and show an upgrade-required message. It may render only an explicitly supported read-only fallback; it may not rewrite the file as schema v2, strip unknown blocks, or silently downgrade metadata.

Existing project-review Markdown, hidden stable identities, project/vault sync, conflicts, and concise Obsidian layout remain compatible through an explicit v2-to-v3 migration. The existing machine ledger becomes a public projection compatibility artifact rather than the long-term machine-fact authority.

## Acceptance criteria

### Zero-token completeness

- A 154-Session project scan invokes no Agent adapter and records zero review-run model tokens.
- Every frozen Session reaches indexed, unsupported, missing, unreadable, or ambiguous state.
- Injected malformed or cross-project records do not block or contaminate later Sessions.
- Final coverage exactly reconciles with the frozen source set and project-affined Observations.

### Determinism and incrementality

- Identical source boundaries and component versions produce identical Observation chunks, SessionViews, and ProjectViews.
- An authenticated source append writes only successor Observation chunks and changed views.
- A repeated unchanged scan writes no new versions.
- A repeated ProjectProbe check with identical state updates only generation check metadata and writes no new ProjectView.
- Restart resumes from published dependencies without duplicate facts or broken lineage.
- Cross-platform fixtures normalize paths and produce equivalent semantic identities.
- Deterministic derived fields exist only in SessionView/ProjectView and never re-enter ObservationStore.
- Adapter upgrades create successor Observation revisions, preserve inactive history, and rebuild only exact dependents.
- Project path moves and verified worktrees retain project identity; conflicting aliases never merge stores automatically.

### Storage and privacy

- Complete private facts never appear in Project, Git, Vault, or plugin JSON.
- No complete user/assistant transcript or complete tool output is duplicated in the memory store.
- Source references and hashes allow bounded on-demand retrieval while the source exists.
- Missing sources preserve retained facts and display honest availability state.
- Content-addressed chunks and reference-based views prevent full-copy growth on every scan.
- Shared Session usage exists once in SourceCatalog; project totals are marked associated/shared and global totals deduplicate by Session ID.
- Automatic cleanup removes only unreachable never-published staging/cache older than seven days; committed history remains queryable.

### Traceability and future analysis

- Every observed fact links to an authenticated source location.
- Every derived fact links to exact dependencies and a versioned rule.
- Every Agent annotation links to stable fact/view dependencies and survives unrelated scans.
- SessionView and ProjectView history remains queryable without copying complete source text.
- Machine facts, Agent semantics, and human presentation can be distinguished during later analysis.

### Human presentation

- Human edits to supported review/history fields survive zero-token rescans.
- Human values win over Agent and deterministic values in the rendered presentation.
- Machine facts remain immutable and traceable beneath a conflicting human presentation.
- Project/Vault concurrent human edits still use the existing explicit conflict workflow.
- Empty semantic sections are omitted rather than populated with invented content.
- `set`, `suppress`, and `restore_default` survive rescans at field level; unknown custom Markdown remains byte-preserved.
- Unsafe human edits remain visible at their origin with an explicit conflict and are not propagated or silently deleted.
- A changed generated baseline preserves the human patch and reports `underlay_changed`; a replaced entity retains an unattached `orphan_patch`.

### Migration and Obsidian

- Current concise project review, history, evolution, status, and usage remain available.
- Existing human edits remain the highest presentation source.
- Safe legacy Agent semantics remain human/legacy presentation in the first release and are not promoted to machine fact; optional later conversion requires valid evidence.
- The partially completed Agent job is not required to scan all source Sessions.
- Obsidian displays only the concise scan-result presentation and safe scan status.
- Schema-v2 writers fail closed on schema-v3 projections and cannot downgrade or overwrite them.
- A crash at every publication-journal step either resumes the same generation or leaves/restores the prior published generation without losing concurrent human edits.
- Crash rollback uses compare-and-swap and never restores over a destination changed after the journaled write.
- ProjectProbe reports live Git/version/file state without running tests, scripts, installs, or network requests and without rewriting historical claims.
- A Project/Vault edit made during extraction causes a preimage conflict and is never overwritten; the prepared private generation can be retried.

## Delivery sequence

Implementation planning must use three internal gates under this architecture. No partial gate is published as the new default workflow.

### Gate A: zero-token core

1. stable project identity, content-free SourceCatalog, and observed-only revision schemas;
2. immutable chunk store, generation manifests, retention accounting, and SourceAdapter contract;
3. Codex discovery, project-affinity partitioning, shared-usage references, and SessionView materialization; and
4. read-only ProjectProbe, deterministic ProjectView reducer, incremental rebuilds, and CLI-level fixture acceptance.

### Gate B: human projection and publication

1. schema-v3 HumanPresentationPatch parsing, rebasing, precedence, and byte-preserved unknown Markdown;
2. deterministic Project/Vault render bridge, minimum-writer enforcement, and explicit conflict handling;
3. durable publication journal with crash recovery and concurrent-edit preimage protection; and
4. Obsidian `更新项目脉络` integration and installed-bundle validation.

### Gate C: migration and real-project acceptance

1. v2-to-v3 migration preserving human and legacy semantic presentation;
2. full AgentWiki 154-Session zero-token scan with coverage reconciliation;
3. unchanged rescan, append rescan, malformed source, missing source, path move, shared Session, and crash-recovery acceptance; and
4. local CLI, Project/Vault, and installed Obsidian plugin verification before public release.

The first public release requires Gates A, B, and C. Granular AgentAnnotation and legacy-evidence conversion are a later optional milestone. Future query/global-catalog work remains a separate project.
