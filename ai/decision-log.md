# Decision log

Prepared retrospectively on 2026-11-22 from the recorded session, not presented as a contemporaneous candidate-written diary. Descriptions below summarize observable proposals, user instructions, edits, and verification. They do not invent candidate reasoning or rejection events.

Session: `01a2010b-42bb-7be2-8a7c-0c0593e8127e` — **Review interview assessment requirements**. Tool: Codex Desktop; recorded model: `gpt-6-astra`. All source timestamps in the exports use UTC; the working timezone was Asia/Shanghai (UTC+08:00).

**Commit links are unavailable for every entry:** this workspace had no Git repository/history at export. File and test links identify the current artifacts, not historical commits. No commits or decision dates have been fabricated.

## D1 — Accepted suggestion: durable operation results for retry-safe issuance

The user proposed six APIs and six business tables. The assistant recommended retaining `audit_events` and adding `bulk_operations`, with a unique tenant/admin/key constraint and a fingerprint including action, course, and canonical body. Certificates, audits, and the saved response would commit together. The user's implementation request explicitly included this design.

- Proposal: [session L441](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.md#source-L441).
- Accepted implementation instruction: [session L505](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.md#source-L505).
- Implementation: [migration v2](../backend/internal/store/migration_v2.sql), [issuance handler](../backend/internal/api/mutations.go).
- Verification: [passing acceptance run](../output/verification/acceptance-run-2.txt), particularly `A2_A4_same_key_concurrency_and_outcomes`, `A2_different_keys_compete`, and `A3_key_misuse_preserves_result`. These check original outcomes, certificate/audit counts, and conflict behavior.
- Commit: **unavailable — no Git history**.

## D2 — Corrected AI output: A6 test did not reliably overlap requests

The AI-authored first acceptance test failed with `list did not complete while export was running`. The export could finish before the list response, so that run did not establish availability during an ongoing export. The assistant disclosed the failure and changed the test's first CSV consumer to read at most 32 KiB with a 10 ms pause. The second export remained unpaced. This was a correction to the verification method, not a claimed fix to server behavior and not a user rejection invented after the fact.

- Original failure: [first acceptance run](../output/verification/acceptance-run-1.txt).
- Recorded correction: [session L621](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.md#source-L621). The corresponding edit is recorded around source L627 in the [tool-call export](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.jsonl).
- Current test: [acceptance_test.go](../backend/internal/api/acceptance_test.go), `pacedReader` and `A6_100000_row_exports`.
- Verification: [second acceptance run](../output/verification/acceptance-run-2.txt); 100,000 exact rows, identical CSVs, approximately 2.579 s paced / 0.372 s unpaced, and a list response in approximately 0.633 s before the first download finished. [Recorded result](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.md#source-L679).
- Limit: these timings exercise Gin and PostgreSQL directly, not a full-size Nginx/browser download.
- Commit: **unavailable — no Git history**.

## D3 — Scope/design tradeoff: synchronous issuance and streamed export

The user explicitly requested the smallest design focused on A1–A7 and asked to avoid overdesign. The accepted plan uses one Gin application, PostgreSQL transactions, and the existing actor header. Issuance completes in one request; export reads an ordered query incrementally. Background workers, caching, production authentication, and additional operation-status APIs were excluded.

- User constraint: [session L472](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.md#source-L472).
- Proposed plan and explicit implementation instruction: [L496](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.md#source-L496), [L505](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.md#source-L505).
- Implementation: [mutations](../backend/internal/api/mutations.go), [list/export](../backend/internal/api/list.go), [React workflows](../frontend/src/main.tsx).
- Verification: [acceptance run](../output/verification/acceptance-run-2.txt), [browser evidence](../output/demo/completed-workflow.jpg).
- Tradeoff: issuance has a 30-second context and retains per-learner results in memory. The 100,000-row benchmark covers export, not issuance at that scale. The actor header is a demo mechanism, not production authentication.
- Commit: **unavailable — no Git history**.

## D4 — Accepted query design: shared filtering and local-date ordering

The list design uses literal substring matching, allowlisted sorts, a certificate-ID tie-breaker, and tenant-local calendar dates. List count and page reads share a short read-only Repeatable Read transaction. Export reuses the same filter/order builder without pagination. `LIMIT/OFFSET` was kept for simplicity.

- Design discussion: [session L462](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.md#source-L462).
- Accepted plan: [session L505](sessions/2026-11-22-01a2010b-42bb-7be2-8a7c-0c0593e8127e.md#source-L505).
- Implementation: [list.go](../backend/internal/api/list.go).
- Verification: `A5_dates_and_list_contract`, `A6_100000_row_exports`, and the Sydney winter test in the [passing run](../output/verification/acceptance-run-2.txt).
- Tradeoff: deep offsets and substring searches can need additional indexing or cursor pagination beyond the measured dataset.
- Commit: **unavailable — no Git history**.
