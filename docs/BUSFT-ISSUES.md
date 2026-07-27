---
summary: "Current status, decision gates, and planning artifacts for the BUSFT work packages and cross-cutting migration issues."
read_when:
  - Choosing, assigning, or reviewing the next Pinakes BUSFT work slice.
  - Reconciling the BUSFT specification with current GitHub issue state.
status: active
---

# BUSFT Work Package Issue Index

Maintainer index for **JK-SPEC-BUSFT-001 — Fault-Tolerant Pinakes Bus**
(spec: [docs/JK-SPEC-BUSFT-001.md](JK-SPEC-BUSFT-001.md)).

This index is not an assignment. Contributor participation is opt-in and
requires explicit acceptance.

| Work | Name | Issue | State | Planning artifact |
| --- | --- | --- | --- | --- |
| WP1 | Namespace + scope enforcement | [#4](https://github.com/joelkehle/pinakes/issues/4) | done | HTTP contract |
| WP2 | Delivery durability + replication + restore | [#5](https://github.com/joelkehle/pinakes/issues/5) | direct delivery merged; replication + restore pending | [durability contract](BUSFT_DURABILITY_CONTRACT_DRAFT.md) |
| WP3 | Client SDK hardening | [#6](https://github.com/joelkehle/pinakes/issues/6) | later; blocked by contract decisions | issue |
| WP4 | Identity/signing + audit | [#7](https://github.com/joelkehle/pinakes/issues/7) | decision hold | [Buzz comparison](BUSFT_WP4_BUZZ_COMPARISON.md) |
| WP5 | Keystone deploy config and UCLA move | [#8](https://github.com/joelkehle/pinakes/issues/8) | done | deployment runbook |
| WP6 | Extraction proof | [#9](https://github.com/joelkehle/pinakes/issues/9) | later; blocked | issue |
| WP7 | Beelink demotion and rollback-asset disposition | [#10](https://github.com/joelkehle/pinakes/issues/10) | waiting on retention period | issue |
| Cross-cutting | Two-history consolidation | [#15](https://github.com/joelkehle/pinakes/issues/15) | planned; not approved | issue |
| Cross-cutting | Explicit namespace migration | [#16](https://github.com/joelkehle/pinakes/issues/16) | decision needed | [namespace draft](BUSFT_NAMESPACE_MIGRATION_DRAFT.md) |

Current governing constraints:

- JK and UCLA remain separate authorities.
- Do not implement WP4 or consolidate the buses without a new Joel decision.
- Do not change production identities, credentials, persisted state,
  allowlists, or consumers from these planning documents.
- Do not delete rollback assets before the approved retention period ends.

The former Pinakes push-enforcement issue
[#11](https://github.com/joelkehle/pinakes/issues/11) is closed and moved to
[manager issue #4](https://github.com/joelkehle/manager/issues/4).

All open Pinakes work is unassigned. An open issue is an invitation to discuss
or propose a slice; assignment occurs only after a contributor explicitly
accepts it.
