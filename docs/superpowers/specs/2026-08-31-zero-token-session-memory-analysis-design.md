# Zero-token Session memory analysis architecture

Date: 2026-08-31
Status: approved revised design, pending implementation plan

## Problem

SessionReviewer currently puts an ephemeral Agent proposal on the critical path for project-wide Session processing. Deterministic work—discovering Sessions, parsing JSONL, extracting files, commands, commits, tests, errors, artifacts, and usage—is coupled to probabilistic semantic generation. A malformed evidence tuple, revision, link, or text value rejects the proposal and stops the project queue.

The real AgentWiki scan demonstrated the product failure: only part of a 154-Session queue was accepted, review-run token usage became large, and different proposal-validation errors required repeated manual retries. The validator correctly prevented unsafe writes, but the system could not reliably finish the deterministic scan.

Project-wide scanning must not depend on Agent output. SessionReviewer should first become a deterministic history and memory-analysis foundation. Agent interpretation remains useful as an optional enhancement. Human-edited project-review content remains editable and has the highest authority over what is presented to people.

## Goals

- Scan every discoverable project Session without invoking an Agent or consuming model tokens.
- Process data in two deterministic passes: per-Session materialization, then project-level reduction.
- Preserve both passes as long-lived, versioned analysis assets without duplicating complete raw Session text.
- Keep one unambiguous machine-fact authority with complete provenance.
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
- `ObservationStore` is the sole authority for machine facts.
- `SessionView` and `ProjectView` are durable, versioned materialized views, not competing authorities.
- The complete private memory store lives in the platform SessionReviewer data root and remains logically isolated per project.
- Full original Session text is not duplicated. Facts retain typed provenance, hashes, source references, and only bounded safe excerpts needed for presentation.
- Original source disappearance does not trigger automatic backup. Retained facts remain available and the source is marked unavailable.
- A Session that touches multiple projects is partitioned at Observation granularity rather than assigned wholesale to one project.
- New versions are created only when input fact digests change.
- Agent semantic content is stored as granular dependency-bound annotations.
- Human-edited review/history content is a `HumanPresentation` layer with highest presentation precedence.
- Obsidian is a human-readable presentation and editing container for scan results, not the machine-fact authority.
- Existing accepted ledger content is migrated according to evidence validity; a fresh zero-token scan reconstructs machine facts from all source Sessions.

## Refined architecture

```text
Agent Session sources
        |
        v
SourceAdapter + content-free SourceCatalog
        |
        v
ObservationStore                         sole machine-fact authority
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

## ObservationStore: sole machine-fact authority

An Observation is a typed, immutable record of something witnessed in a Session or deterministically derived by an identified rule.

Every Observation contains:

- stable observation ID;
- project ID and Session ID;
- provider and source reference;
- source line/range and authenticated source hash;
- event time and sequence;
- typed kind, subject, operation, object, outcome, and relevant structured fields;
- certainty: `observed` or `derived`;
- provenance: source-adapter version or reducer-rule ID/version;
- bounded safe excerpt only when required for human presentation;
- source availability state; and
- redaction diagnostics without sensitive values.

Examples:

```text
observed: command exited with code 0
observed: file path was modified
observed: commit hash was created
derived: later compatible verification recovered an earlier failure signature
semantic: excluded from ObservationStore; belongs to AgentAnnotation or HumanPresentation
```

The distinction prevents a rule-based conclusion from silently becoming an original fact.

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

Directories and records use the existing private platform-data permissions, rooted path validation, identity pinning, atomic publication, and project locks. Complete facts are not placed in Git, Project Markdown, or the Obsidian Vault.

Observation chunks are immutable and content-addressed. A Session append writes only successor chunks. SessionView manifests reference old and new chunks instead of copying all prior facts. ProjectViews contain compact aggregates and references rather than duplicating complete Session observations.

Unchanged scans produce no new version. A SessionView is versioned only when its Observation dependency digest changes. A ProjectView is versioned only when its ordered SessionView dependency digest or reducer version changes. Referenced historical chunks and views are retained; unreferenced compaction is a future explicit maintenance operation, not an automatic first-release behavior.

## Project association and cross-project Sessions

A complete Session is never blindly assigned to its initial working directory.

Each Observation is associated using authenticated context available at that record:

- current working-directory identity;
- rooted file target identity;
- explicit project/session association;
- configured aliases and worktree identities; and
- remote identities where applicable.

Observations that belong to another mapped project go only to that project's store. Provider-level Session metadata may be referenced by multiple projects through the content-free SourceCatalog, but original text is not copied. Ambiguous observations remain quarantined and contribute a safe diagnostic; they are not assigned by guesswork.

## First pass: SessionView materialization

A scan freezes the ordered source candidates and readable boundaries. The first pass converts each project's Observations into one durable SessionView.

SessionView includes:

- Session identity, provider, time range, model, and token usage;
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
- current witnessed branch, version, Git status, and verification state with observation times;
- chronological Session and event timeline;
- files/modules ranked by documented frequency and recency rules;
- commits, tags, releases, deployments, and artifacts;
- tests, builds, lint, sync, and verification outcomes;
- errors, normalized signatures, and compatible later recovery links;
- recent activity, source availability, and inspection pointers;
- model and token accounting;
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

## Granular Agent annotations

Agent analysis is removed from the scan critical path. It is an explicit later operation over one selected ProjectView and only the necessary SessionView/Observation/source spans.

AgentAnnotation may contain narrative, phase interpretation, decision rationale, failure patterns, preference hypotheses, memory candidates, or tool-effectiveness analysis. Each annotation:

- has a stable semantic entity and field scope;
- cites exact Observation or SessionView dependencies;
- records schema, analysis profile, and Agent-run identity;
- cannot modify ObservationStore, SessionView, or ProjectView;
- is invalidated only when its cited dependency digest changes; and
- remains valid when unrelated Sessions are added.

Invalid, unsafe, or unavailable Agent output rejects only that annotation attempt. The zero-token scan and existing presentation remain complete.

## HumanPresentation: highest presentation authority

Project review and history remain human editable in the Project/Obsidian synchronization workflow. Parsed human edits form `HumanPresentation`, the highest-precedence source for editable semantic and display fields.

HumanPresentation may override:

- project goal, stage, and status wording;
- next action and first-inspection guidance;
- decision titles, explanations, and status presentation;
- risks, blockers, and explanatory notes;
- timeline titles/summaries; and
- visible selection, ordering, grouping, and emphasis.

HumanPresentation does not delete or rewrite underlying observed facts. If a human presentation conflicts with a machine observation, the human wording remains the primary human-facing view while the observation remains available for traceability and future analysis.

Stable hidden identities and existing parse/render contracts preserve edits across rescans. The renderer merges sources field by field:

```text
human value, when present
otherwise valid Agent annotation
otherwise deterministic ProjectView value
otherwise omit the optional section
```

Sync conflicts between Project and Obsidian continue through the existing explicit conflict workflow; the scan does not arbitrarily choose one human edit over another.

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

`failed` is reserved for project-wide integrity failures: unauthenticated project identity, inability to publish the private store atomically, corrupt manifest lineage, unrecoverable human-presentation parse conflict, or failure to render/sync an otherwise committed generation. A malformed Session is not a project-wide failure.

Agent annotation status is independent and cannot change scan completion.

## Migration from the Agent-first ledger

Migration is additive and recoverable:

1. Authenticate the project mapping and preserve existing review, history, machine ledger, sync state, and historical review job.
2. Freeze and scan every discoverable source Session through ObservationStore and SessionView.
3. Build the first ProjectView from all SessionViews.
4. Parse existing human-edited review/history into HumanPresentation and preserve it at highest presentation precedence.
5. Resolve each existing Agent-authored decision/narrative evidence tuple against new Observations.
6. Migrate resolved Agent semantics into granular valid annotations.
7. Retain safe unresolved legacy semantics as `legacy-unverified` history, excluded from current factual claims unless a human presentation explicitly preserves them.
8. Render through the existing human-readable format and verify stable identities before switching the default action to `更新项目脉络`.
9. Keep the old Agent job as historical status; it is not resumed or required for new scan completion.

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

Private-store migration writes new immutable chunks/views and atomically publishes a new manifest generation. Interrupted migration leaves the prior generation authoritative. Readers reject unsupported required versions without deleting existing objects.

Existing project-review Markdown, hidden stable identities, project/vault sync, conflicts, and concise Obsidian layout remain compatible. The existing machine ledger becomes a public projection compatibility artifact rather than the long-term machine-fact authority.

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
- Restart resumes from published dependencies without duplicate facts or broken lineage.
- Cross-platform fixtures normalize paths and produce equivalent semantic identities.

### Storage and privacy

- Complete private facts never appear in Project, Git, Vault, or plugin JSON.
- No complete user/assistant transcript or complete tool output is duplicated in the memory store.
- Source references and hashes allow bounded on-demand retrieval while the source exists.
- Missing sources preserve retained facts and display honest availability state.
- Content-addressed chunks and reference-based views prevent full-copy growth on every scan.

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

### Migration and Obsidian

- Current concise project review, history, evolution, status, and usage remain available.
- Existing human edits remain the highest presentation source.
- Legacy Agent semantics with valid evidence become granular annotations; unresolved legacy content is not promoted to machine fact.
- The partially completed Agent job is not required to scan all source Sessions.
- Obsidian displays only the concise scan-result presentation and safe scan status.

## Delivery sequence

Implementation planning must preserve these dependency boundaries:

1. private Observation schemas, chunk store, manifests, and SourceAdapter contract;
2. Codex source discovery, project-affinity partitioning, and first-pass SessionView materialization;
3. deterministic ProjectView reducer and version history;
4. HumanPresentation parse/precedence preservation and deterministic render bridge;
5. zero-token scan job/control plane and Obsidian `更新项目脉络` integration;
6. legacy-ledger migration and real AgentWiki 154-Session acceptance;
7. granular AgentAnnotation as a separate optional milestone; and
8. future query/global-catalog work as a separate project.

The first releasable milestone ends after step 6. Agent enrichment is not required to prove that project-wide scanning is complete, useful, human editable, and zero-token.
