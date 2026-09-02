# Zero-Token Session Memory Analysis & Publication Architecture

## Overview

SessionReviewer 0.3.0 replaces brittle, token-expensive LLM-based session scanning with a deterministic zero-token extraction, aggregation, projection, and durable publication pipeline.

## Delivery Gates

- **Gate A (Core Fact Engine)**:
  - Source discovery & freezing without side effects.
  - Content-addressed observation storage below the private project root.
  - Space-Saving aggregation with exact coverage accounting.
  - Prepared generation manifest commit.

- **Gate B (Projection & Publication)**:
  - Schema-v3 public projection with `minimum_writer_version: 0.3.0`.
  - Human presentation layer with highest precedence (`HumanPresentation > Deterministic ProjectView`).
  - Durable cross-root publication journal with compare-and-swap (CAS) writes.
  - Crash recovery & rollback protecting concurrent human edits.
  - Single-action Obsidian integration: `更新项目脉络`.

- **Gate C (Migration & Acceptance)**:
  - Safe migration of legacy v2 records to v3.
  - End-to-end multi-session project acceptance.
