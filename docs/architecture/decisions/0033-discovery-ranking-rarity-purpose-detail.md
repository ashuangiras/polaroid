# 0033. Discovery ranks by term rarity, stated purpose and bounded detail

**Status:** Accepted. Replaces the ranking of [ADR-0032](0032-lexical-discovery-and-duplicate-suggestions.md); its terms, candidates, visibility, bounds and duplicate suggestions are unchanged.
**Date:** 2026-10-11

## Context

ADR-0032 scored a candidate by adding, for each distinct task term it contains, a fixed weight for the best field holding the term (3 canonical key or goal, 2 method, 1 otherwise). Terms are sets, so a word repeated in a procedure or in a task counts once. On Polaroid's own catalog, broad procedures still outranked the procedures written for a task ([#81](https://github.com/ashuangiras/polaroid/issues/81)): for "add a new HTTP endpoint and MCP tool that read procedure records…", `repo.polaroid.adopt`, an onboarding procedure, came before `polaroid.record-model.change`. The causes are not repetition:

- every term weighed the same, whether nearly every procedure contains it (`procedure`, `record`, `change`) or one does;
- a long goal or method contains more of any task's words by chance than a short, specific one;
- words found only in contract or instructions added up without bound, one point each.

## Decision

For a task with distinct terms *t* and the request's candidates *C* (the latest versions ADR-0032 selects, after the repository filter), discovery computes:

- **Rarity.** *df(t)* is the number of candidates containing *t* in any searched field, and *rarity(t)* = ln(1 + |C| ÷ *df(t)*). A term in every candidate gets ln 2 ≈ 0.69; one in a single candidate of 16 gets ln 17 ≈ 2.83. The corpus is exactly *C*: a repository-scoped request derives nothing from procedures that do not apply there, and a catalog-wide one uses the whole catalog.
- **Field weight.** Canonical key and goal, the stated purpose, weigh **4**; method **2**; philosophy, contract and instructions **1**.
- **Length.** Goal, method and philosophy are prose. A prose field longer than the average of that field among *C* (over candidates that have it) weighs less: its weight is divided by max(1, 0.25 + 0.75 × its distinct terms ÷ the average), BM25's normalization with b = 0.75, never raised for a short field. The canonical key, contract and instructions are not normalized.
- **Points.** A matched term earns *rarity(t)* × the highest (normalized) weight among the fields containing it; on equal weights the earlier field in the order above is credited.
- **Detail cap.** Points of terms whose best field is contract or instructions add up to at most 2 × the highest rarity among the task's terms: together, detail can add what the task's most distinctive term adds in a method of average length.
- **Score** = the points of terms credited to key, goal, method or philosophy + min(detail points, cap). Candidates matching at least one term are ranked by score, then matched terms, both descending, then by canonical key. Ranking compares unrounded values; responses round to two decimals.

Responses explain the result: `considered` (|*C*|), `rarity` (each term with its *df* and rarity), `detail_cap`, and per candidate `score`, `detail_points` (uncapped), `contributions` (each matched term with the field credited and its points) and, as before, `matches` (every field containing each term).

A score is a ranking value for one task over one corpus. It is not a probability, a confidence or a claim that a version suits or was verified for anything; it changes when the catalog changes. Matching stays lexical, generic and read-only: no procedure, task category or synonym is special.

## Consequences

- Specific procedures whose purpose states the task's distinguishing words rank above broad ones that mention common words widely. On the unchanged catalog of #79, `polaroid.record-model.change` now leads the HTTP-endpoint task, and `polaroid.change.docs` leads the README task by score instead of by tie-break.
- Synonyms still do not meet ("compile" never matches "build"), a task's distinguishing word may appear only in another procedure's tools or contract (the MCP-description typo task still ranks `go.module.checks` first), and a long contract can still credit a rare term (the linters task ranks `go.module.build` above `go.module.checks`). Semantic matching remains later work.
- Rankings depend on the corpus, so adding procedures can reorder results for the same task, including in repositories where the new procedures apply.
- `score` is no longer an integer, and responses gain fields. Discovery was unreleased when this changed, so no published client depends on the ADR-0032 shape. Each request still reads each candidate once; no index, schema or migration.
