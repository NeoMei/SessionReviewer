# Shared Markdown corpus semantics

`expected_code` is the ledger-only diagnostic. `expected_markdown_code`, when present, is a non-empty closed Markdown diagnostic for a parser-negative draft. Such a draft deliberately reuses an otherwise valid accepted ledger, so its changed document hash is not an accepted snapshot binding and is not checked. Ledger and Session Index validation still run.

`expected_fields` is an exact-value subset of human fields to assert after both documents have been parsed. An empty object means there are no additional field-value assertions; it never skips either document parser. Completeness against an authenticated Presentation belongs to the later projection-validation layer.
