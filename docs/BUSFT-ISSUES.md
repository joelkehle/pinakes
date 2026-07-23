# BUSFT Work Package Issue Index

Delegation index for **JK-SPEC-BUSFT-001 — Fault-Tolerant Pinakes Bus**
(spec: [docs/JK-SPEC-BUSFT-001.md](JK-SPEC-BUSFT-001.md)).

Current operating order (2026-07-23): deploy WP1 in explicit compatibility
mode → move the still-separate UCLA authority to Keystone → complete the
narrowed WP2 replication/recovery work → rehearse and execute the single-bus
import. WP4 is held for a Joel/Codex Buzz design discussion after both separate
authorities are stable on Keystone; no WP4 implementation or rewrite starts
before that discussion. WP3 and WP6 follow the target-topology decision.

| WP  | Name                                | Issue | URL                                                | Status |
| --- | ----------------------------------- | ----- | -------------------------------------------------- | ------ |
| WP1 | Namespace + scope enforcement       | #4    | https://github.com/joelkehle/pinakes/issues/4      | code merged; deployment pending |
| WP2 | SQLite persistence + Litestream     | #5    | https://github.com/joelkehle/pinakes/issues/5      | narrow to Litestream + executed recovery drill |
| WP3 | Client SDK hardening                | #6    | https://github.com/joelkehle/pinakes/issues/6      | open; relays remove it from the authority-move critical path |
| WP4 | Identity/token registry + audit log | #7    | https://github.com/joelkehle/pinakes/issues/7      | hold for Buzz discussion; no implementation |
| WP5 | keystone deploy config              | #8    | https://github.com/joelkehle/pinakes/issues/8      | largely implemented in `shared/keystone-infra`; reconcile/close |
| WP6 | Extraction procedure document       | #9    | https://github.com/joelkehle/pinakes/issues/9      | open after unified-topology decision |
| WP7 | Beelink demotion runbook            | #10   | https://github.com/joelkehle/pinakes/issues/10     | partially complete; JK demoted, UCLA pending |

Beelink is online. WP7's old availability blocker is resolved: JK authority is
already on Keystone behind a Beelink compatibility relay; UCLA remains
authoritative on Beelink until its separately approved quiet-window cutover.
