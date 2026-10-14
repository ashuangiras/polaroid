# 0050. Corpus Logic v1: typed knowledge records and bounded reasoning on Git-native knowledge

**Status:** Proposed (owner review), milestone Corpus Logic v1 ([#134](https://github.com/ashuangiras/polaroid/issues/134)).
**Date:** 2026-10-14

## Context

Git-native knowledge ([ADR-0045](0045-living-knowledge-kernel.md)) holds procedures only. Corpus Logic v1 adds domain-independent knowledge (domains, concepts, claims, rules, procedure semantics, evidence, exceptions) and reasoning over it: inference, specialization, composition and controlled exceptions. The brief prescribes the libraries: Mangle (`codeberg.org/TauCeti/mangle-go` v0.5.0) for Datalog, goRDFlib (`github.com/tggo/goRDFlib` v0.6.0) for RDF terms, graphs and SHACL, and JSON-goLD (`github.com/piprate/json-gold` v0.8.0) for JSON-LD. Polaroid implements only what those libraries cannot: the record model, Corpus-specific validation, budgets, obligation semantics, exceptions and explanations.

A compatibility experiment in an isolated module ([#135](https://github.com/ashuangiras/polaroid/issues/135)) passed on the pinned versions. It evaluated a recursive rule, built and SHACL-validated a two-concept graph (conforming and violating), converted JSON-LD with an offline context, refused an invalid rule (unbound head variable, arity, syntax), and refused a remote context with zero requests reaching a live test server. It also passed on linux and darwin (amd64, arm64), offline under a network-denying sandbox, and with govulncheck clean. Newly compiled modules: the three above plus `bitbucket.org/creachadair/stringset` v0.0.11, `github.com/antlr4-go/antlr/v4` v4.13.1, `github.com/cayleygraph/quad` v1.3.0, `github.com/pquerna/cachecontrol` v0.2.0, `go.uber.org/multierr` v1.11.0 and `golang.org/x/exp`. Their licenses are BSD, Apache-2.0 or MIT. goRDFlib's storage adapters (SQLite, Badger, remote SPARQL) are not compiled.

The experiment found three library behaviors this design must compensate for:

1. **Mangle ignores fact-store read errors.** `engine.premiseAtom` (v0.5.0, `engine/premise.go`) discards the error of a premise lookup. Evaluation has no context. A store that returns `ctx.Err()` therefore yields an incomplete fixed point rather than an error.
2. **goRDFlib's `jsonld.Parse` and json-gold's `NewJsonLdOptions` default to an HTTP document loader.** goRDFlib's `shacl.Validate` runs SHACL Core and SHACL-SPARQL by default; SHACL-AF needs an explicit option.
3. **`@base` in a remote JSON-LD context is ignored**, so keys cannot be relative IRIs against a base.

## Decision

1. **Packages and dependency direction**, enforced by `internal/archtest`:
   - `internal/knowledge` gains the record model: identities, types, relationships, canonical encoding, catalog validation and the format manifest. It imports no RDF, JSON-LD or Datalog library. A `RecordValidator` port, implemented by `internal/knowledge/semantic`, checks record semantics.
   - `internal/knowledge/semantic` is the only package that imports goRDFlib and json-gold. It holds the bundled context, the offline loader, JSON-LD to RDF, SHACL Core shapes and limits, and persists nothing.
   - `internal/reasoning` is pure Corpus logic: resolution, obligations, specialization, composition and explanations. It depends on an `Evaluator` contract it defines.
   - `internal/reasoning/datalog` is the only package that imports Mangle, and it implements `Evaluator`.
   - `internal/reasoning/policy` handles exceptions, authority, Ed25519 verification and conflicts. It is pure and imports no inference package.
   - None of these import Git, SQLite, HTTP or MCP. `knowledgeops` wires them, and transports stay where they are.

2. **Snapshot format 2.** A snapshot without `corpus.json` is format 1 and is read exactly as before. A snapshot with typed records carries `corpus.json`, `{"format":2,"capabilities":[...]}`, which the kernel writes on publication. Each record lives at `records/<type>/<first key segment>/<key>.jsonld`.
   - **Identity.** A record's identity is its key. Keys form one namespace with procedure keys, and the path is derived from the key, never the reverse.
   - **Encoding.** A record is JSON-LD that names exactly the bundled context `urn:polaroid:context:v1` and is stored in canonical form, checked the way procedure documents are: strict decoding, unknown members refused, byte-equal re-encoding, sets sorted.
   - **Instance safety.** Instances built before this ADR already refuse any path outside `procedures/`, so they fail closed on format 2. New instances refuse an unknown format or capability, and refuse records when no validator is configured. Format-1 documents and the existing keys keep their exact bytes and meaning.

3. **Vocabulary.** Record IRIs are `urn:polaroid:k:<key>`, written `k:<key>` in documents. Standard terms are used only where their meaning matches:
   - `skos:Concept`, `skos:prefLabel`, `skos:definition` and `skos:broader` for taxonomy only.
   - `dcterms:subject` for `classified_as`, and `dcterms:replaces` for `supersedes`.
   - Corpus-specific predicates under `urn:polaroid:corpus:`: `specializes`, `uses`, `composes`, `supports`, `contradicts`, `excepts`, `requires`, and the contract and exception terms.

   SHACL Core shapes are bundled, never loaded from knowledge. Neither `sh:sparql` nor SHACL-AF is used. Rules are not SHACL rules.

4. **JSON-LD processing.** It uses json-gold only, with an offline loader that serves only the bundled context. Every other URL is refused without I/O, and the default loader is never constructed on the processing path. Processing uses JSON-LD 1.1 with `SafeMode`, which refuses undefined terms. Limits apply to document bytes, nesting, expanded triples and graph size.

   The semantic validator also checks that each record's RDF projection states exactly the typed relationships the record model decoded, so the two readings cannot diverge.

5. **Reasoning** is `Resolve(catalog, context, goal, at, trust) -> Resolution`. Its result depends only on the snapshot, the context (string name and value pairs), the goal key, the reasoning version `corpus-logic/1`, the evaluation time and the trust material.
   - **Facts.** Validated records become Mangle facts deterministically: `record`, `classified_as`, `broader`, `specializes`, `uses`, `composes`, `supports`, `contradicts`, `supersedes`, `excepts`, `requires`, `asserts`, `context` and `goal`.
   - **Rule records** carry a Mangle program limited to positive atoms, `=`, `!=`, constants and variables. Heads may only be `obligation(S, R)` or `derived(S, P, O)`. The adapter appends the rule key to each head, which gives every derivation its provenance. Negation, transforms, functions, temporal and external predicates, declarations and packages are refused before Mangle analysis runs.
   - **Budgets.** Evaluation is bounded by rule count, program bytes, created facts and the context deadline. Cancellation is recorded by a sticky fact-store wrapper. Any stopped or exceeded evaluation returns an error and discards its partial result. A panic inside Mangle is recovered and returned as an error; it never reaches the caller.
   - **Open and closed world.** Negation is never inferred from absence in domain facts. A conclusion that holds only for context the caller did not give is reported as an unknown, never as absent:
     - The values considered for a missing name are the constants rules compare it with, directly (`context("mode", "ci")`) or through `=` (`context("mode", V), V = "ci"`).
     - Every combination of one value per missing name is evaluated, as a real request would give it, so two values of one name are never assumed together and conclusions that need several names are found. Positive inference is monotone, so this finds every conclusion such context could add.
     - Each unknown names its source rule and `when`, the values it needs: a name is listed when removing it or changing its value loses the conclusion.
     - What cannot be enumerated (a rule that reads a context name or value it does not fix, or more than 64 combinations) makes the result explicitly incomplete: `complete` is false and `incomplete` says why. Nothing is skipped silently.

     Absence is decisive only about the snapshot's own declarations, for example which records exist or what supersedes what.
   - **Undefined requirements.** A rule whose head names a requirement constant that is not a Concept record is refused at publication. A requirement derived through a variable that is not a Concept is reported as an `undefined_requirement` conflict.

6. **Specialization.** Obligations are `own_obligation(S, R, Source)`, where the source is a rule or the record that `requires` R. Inheritance follows `specializes` transitively, and each derivation is `(R, Source, Via)`.
   - Diamonds keep one entry per distinct derivation, and different sources stay separate.
   - Specialization only adds obligations. `contradicts` between two required concepts is a conflict.
   - A child procedure's applicability must lie within its parent's.
   - `skos:broader` never inherits obligations, and specialization does not imply substitutability.

7. **Composition** never executes anything, and reads only the snapshot: caller context is not a fact about either procedure. It compares typed inputs and outputs and the formal pre-, post-conditions and invariants, which are concept keys:
   - `compatible`: everything the consumer needs is formally established.
   - `incompatible`: there is a formal contradiction, or a needed type is missing from a producer that declares its outputs complete.
   - `undetermined`: anything else, including any informal condition.

   The result carries both formal contracts and one finding per consumer input, precondition and informal condition; findings that are not satisfied are the missing prerequisites. It is served by `resolve` with `then`: the goal is the producer and `then` the consumer.

8. **Exceptions.**
   - **Records.** An exception record targets one rule, that rule's `revision`, and its `rule_digest`: the SHA-256 of the RFC 8785 canonical JSON of the rule's normative semantics (key, statement, program, protected, waivable and sorted domains). Its approval is an Ed25519 signature (standard library) over the RFC 8785 canonical JSON of a fixed payload that includes the revision, the rule digest and the justification's SHA-256 digest. An approval therefore covers exactly the semantics it was given for.
   - **Rule governance.** Rule semantics change only by a governed revision. Every publication, rollback, activation and validated fetch checks the transition from the published catalog:
     - a rule whose semantics changed must take a higher revision, and a revision never decreases;
     - a protected rule is never removed or unprotected;
     - within a catalog, a protected rule is never waivable, no exception may target it, and only a protected rule may supersede it.

     A rollback is exempt only for rules it restores exactly.
   - **Protected invariants** cannot be waived by any exception, at any authority, corpus included. Changing one takes a governed revision of the rule itself.
   - **Trust material.** Keys, the authority each key holds (corpus, a domain, or a project repository) and revocations come from operator-held material. They never come from the knowledge being authorised, and never from Git authorship.
   - **Trusted context.** The request context is what the caller asserts. It selects which rules derive, but never authorises a waiver. Project authority and exception conditions are checked against trusted context: name and value pairs the server itself established. Agent mode authenticates no project identity and establishes none, so project-scoped and conditional exceptions fail closed there; the verdict says when the caller asserted a matching value.
   - **When an exception applies.** It changes only the derivation from its target rule, and only when every one of these holds:
     - its signature verifies with a trusted, unrevoked key whose authority equals the declared one;
     - the revision and the rule digest match the rule in the snapshot;
     - the rule is not protected, and is waivable;
     - a domain authority covers only rules classified in its domain or a narrower one;
     - for project authority, the repository is in the trusted context;
     - the subject matches, and the conditions match the trusted context;
     - the evaluation time is within the validity period;
     - no other valid exception targeting the same derivation has a different effect.
   - **Effects.** Rejected exceptions are reported with a reason. An exception never makes a claim true or an execution successful.
   - **Claims.** A claim's evidence status (`supported`, `contested`, `unsupported`) summarizes the evidence present. It is never a verdict.

9. **Agent surface.** `/mcp/agent` v1 is unchanged. Resolution is served on a new, versioned endpoint, `/mcp/agent/v2`, that adds one tool, `resolve`, to the same three. Its result is concise:
   - the snapshot, the goal, the applicable keys, and whether the result is `complete`;
   - obligations, each with its derivation identifiers;
   - specializations, verified and rejected exceptions;
   - conflicts, unknowns, derived facts and evidence status;
   - with `then`, the composition of the goal and that procedure.

   An explanation identifier is the first 16 bytes, in hex, of SHA-256 over the reasoning version, the snapshot, the goal and the derivation's parts (`obligation`, requirement, source, via; or `derived`, predicate, object; or `replacement`, requirement, exception, replaced requirement), joined by NUL bytes. Anyone holding the snapshot can recompute it. It identifies the derivation, not its status: whether the derivation holds, is waived or is unknown depends on the context, time and trust given, which the result reports alongside it. The RDF graph and full derivation sets are never returned by default.

## Consequences

- Three direct dependencies and seven transitive modules are added, listed in [dependencies.md](../../development/dependencies.md). goRDFlib is pre-v1 and sits behind one adapter package.
- Graphs, fact stores and indexes are rebuilt from a snapshot on demand and are never authoritative.
- Format-2 knowledge cannot be activated by instances built before this ADR. That is intended: they refuse it.
- Mangle provides no proof trees. Explanations are derivation tuples (rule, via record, specialization path), not full proofs.
- Open risks:
  - Mangle's ignored lookup errors are compensated in the adapter. A test fails if a future Mangle version starts propagating them, so the workaround can be revisited.
  - Default library behaviors that reach the network are avoided by construction and covered by tests that count requests.
- Limitations of this version:
  - No transport establishes trusted context yet, so project-scoped and conditional exceptions never apply through `/mcp/agent/v2`. Corpus and domain exceptions without conditions do.
  - A specialization path in a result is the shortest path, with the lexically first parent at each step. Other paths are not listed, though every distinct derivation is.
  - An exception applies to the subjects it names and to derivations inherited through them. It matches exact keys, not domains.
  - Declared requirements (`requires`) cannot be excepted. Only rule-derived obligations can.
  - Unknowns are enumerated only for values rules fix by constants or `=`; anything else is reported as incompleteness. At most 64 combinations of missing values are evaluated per resolution.
  - Rule governance is checked between consecutive published catalogs. Knowledge fetched from a source whose parent is not readable here is checked when it is activated, against the active snapshot. Activation grants no rollback exemption, so a fetched rollback that lowers a rule's revision cannot be activated; the operator rolls back locally instead.
  - Validating a record parses its JSON-LD twice: once strictly as a typed document, and once through json-gold for the RDF projection. The projection check keeps the two readings in agreement.
