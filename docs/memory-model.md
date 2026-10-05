# PRISM memory model

PRISM's memory follows the design in the `memo` project (a fact-first memory engine), built on PRISM's own fact
table rather than a second store. A **fact** is a claim with a lifecycle, evidence and a place on one or more
shelves (banks). Free text stays the primary form; structure is optional.

## Lifecycle

```
proposed → active ⇄ contested → superseded | retracted | expired
```

* `valid_to IS NULL` still means *current*. Retiring a fact sets `valid_to` and records why in `status`.
* **contested** is never stored: a fact is contested exactly while a live fact contradicts it (a `contradicts`
  link), so it clears by itself when the conflict is resolved. Contested facts are still recalled, at 45% weight
  and flagged *disputed*.
* **proposed**: what an agent on probation wants in a *shared* bank (`memory_store` files a proposal). It is
  stored and shown in Review, never retrieved, never used for reflection/analysis, and retires nothing, until a
  person accepts it (`Promote`) or rejects it (`RejectProposal`). A guard test
  (`proposed_guard_test.go`) fails if a new query over live facts forgets to exclude proposals.
* Nothing is silently overwritten: corrections supersede, retraction keeps the row, expiry sets `valid_to` to the
  expiry moment, so time-travel views stay right.

## Trust

* `confirmation`: `unconfirmed`, `user_confirmed` (the user vouched: confidence ≥ 0.95, forgotten 4× slower),
  `multi_source_confirmed` (two independent source groups, none refuting). User confirmation is never downgraded
  by evidence bookkeeping.
* **Evidence ledger** (`memory_evidence`): supporting and refuting sources with the exact reference and an
  independent *source group* (the registrable domain). Independence is counted by group, never by rows; unknown
  origin never counts as independent.
* **Audit trail** (`memory_audit`): who did what — retain, supersede, retract, expire, confirm, move, share,
  propose, promote, reject.

## Recall

* Reinforcement: retrieval alone does not raise rank. A fact is reinforced at most once per task (or chat-day)
  (`memory_use_receipts`), so the same facts recalled every turn do not climb to the top by being retrieved.
* Volatile facts (`ttl_days` at extraction or `memory_store`) retire themselves (`expires_at`).
* Selection is diversified (maximal marginal relevance) so near-paraphrases do not fill the slots.
* The auto-recalled packet is budgeted in tokens, rendered with `FactFlags` (disputed, unverified, confirmed by
  you / independent sources, may go stale…), and **bundles** facts that share subject, predicate, bank and standing
  into one line ("the user owns A, B and C"). Bundles are only a view: every fact keeps its own id and history.

## Structure and shelves

* Optional `subject / predicate / object / qualifiers` on a fact, filled in at extraction for plain statements.
* A fact has one **home bank** and can be shown in others (`memory_fact_banks`, `ShareFact`) without copying:
  one claim, one evidence set, many shelves.

## Coverage, rules and inference (added later)

* **Required evidence**: `memory_check` / `memory.coverage` take the specific things a task needs and answer per
  item: supported, disputed, stale (optionally with a max age), weak (unverified) or not found in the searched
  banks — plus the gap list. Read-only: checking reinforces nothing. "Not found" describes the searched scope only.
* **Predicate cardinality** (`memory_predicates`): a single-valued predicate ("lives in", "works at") replaces the
  old value for the same subject; a user-confirmed or trusted fact is disputed instead of replaced by a rule or by a
  weak report. Aliases map phrasings to one rule; unknown predicates accumulate.
* **Transitive staleness**: a conclusion is stale when any premise beneath it, at any depth, was retired.
  Derived on read (recursive query), flagged and down-weighted in recall, listed in Review and the digest.
* **Knowledge pages** (from memo's article cache): a page is stale as soon as a fact it was written from is
  retired, corrected or lapses (`kb_read` warns and queues a rebuild; the sweep rebuilds promptly; a rebuild
  re-reads its sources before committing).
* **Trackers** (from memo's trackers): typed columns (checked, coerced), `tracker_snapshot` — one atomic,
  idempotent run per refresh where a complete snapshot marks vanished rows *missing* (never deleted) and a partial
  one leaves absent rows alone — row events with a kind, and alerts: deterministic three-valued conditions on typed
  columns (`tracker_alert`) that fire only on a real transition and never treat an unobserved value as false.

## Agents and tools

Agents have fixed toolsets: nothing is loaded at run time. `tool_search` only looks up who holds a tool; a call to a
tool outside the run's toolset is refused. A missing tool means `ask_colleague` a holder, or `report_blocked`
(what is missing, what was done, artifact references). Material moves between agents by reference
(`artifact_save`, `scratchpad_share`, `refs`), never pasted.

## Not implemented (from memo)

Outcome feedback on retrieved evidence (helped / misled / missing), qualifier-aware validity intervals on facts,
section-level (rather than page-level) invalidation of knowledge pages, tracker proposals into shared memory.
