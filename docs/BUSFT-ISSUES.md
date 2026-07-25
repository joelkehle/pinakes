# BUSFT Work Package Issue Index

Maintainer index for **JK-SPEC-BUSFT-001 — Fault-Tolerant Pinakes Bus**
(spec: [docs/JK-SPEC-BUSFT-001.md](JK-SPEC-BUSFT-001.md)).

Current operating order (2026-07-25): release and deploy WP1 compatibility
support through v0.4.0 → move the still-separate UCLA authority to Keystone
(WP5) → decide WP2 recovery semantics → complete the explicit-namespace
prerequisite (#16) before any approved consolidation rehearsal (#15). WP4 is
held for a Joel/Codex Buzz design discussion after both separate authorities
are stable on Keystone; no WP4 implementation or rewrite starts before that
discussion. WP3 and WP6 follow the topology and contract decisions.

| WP / prerequisite | Name | Issue | Status |
| --- | --- | --- | --- |
| WP1 | Namespace + scope enforcement | [#4](https://github.com/joelkehle/pinakes/issues/4) | code merged; v0.4.0 release/deployment rollout |
| WP2 | Recovery semantics, replication, and restore proof | [#5](https://github.com/joelkehle/pinakes/issues/5) | waiting for durability decision |
| WP3 | Client SDK hardening | [#6](https://github.com/joelkehle/pinakes/issues/6) | later; follows consolidation contract |
| WP4 | Identity/token registry + audit log | [#7](https://github.com/joelkehle/pinakes/issues/7) | decision hold; no implementation before Buzz discussion |
| WP5 | Move UCLA authority to Keystone | [#8](https://github.com/joelkehle/pinakes/issues/8) | current |
| WP6 | Extraction procedure | [#9](https://github.com/joelkehle/pinakes/issues/9) | later; follows topology decision and consolidation |
| WP7 | Demote UCLA authority on Beelink | [#10](https://github.com/joelkehle/pinakes/issues/10) | waiting for WP5 acceptance and soak |
| Prerequisite | Explicit namespace migration | [#16](https://github.com/joelkehle/pinakes/issues/16) | required before consolidation |
| Planned | Two-history bus consolidation | [#15](https://github.com/joelkehle/pinakes/issues/15) | planning only; depends on #16 and an explicit topology decision |

The former Pinakes push-enforcement issue
[#11](https://github.com/joelkehle/pinakes/issues/11) is closed and moved to
[manager issue #4](https://github.com/joelkehle/manager/issues/4).

All open Pinakes work is unassigned. An open issue is an invitation to discuss
or propose a slice; assignment occurs only after a contributor explicitly
accepts it.
