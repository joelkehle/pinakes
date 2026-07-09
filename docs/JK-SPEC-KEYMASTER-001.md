# JK-SPEC-KEYMASTER-001 — Credential Minting Agent ("Keymaster")

- ID: JK-SPEC-KEYMASTER-001
- Version: v0.2 (full draft; supersedes v0.1 stub)
- Status: Draft for review — blocked on BUSFT WP4 for KM-OQ1 only; all other sections implementable-as-written once BUSFT lands
- Author: Claude (drafted), Joel Kehle (owner)
- Date: 2026-07-08
- Related: JK-SPEC-BUSFT-001 (transport + identity substrate; BF-8 names keymaster as future token issuer), JK-SPEC-FAULTTOL-001 (FT-9.6 references this spec; layers 9–10), JK-SPEC-INTERNPM-001 (worker/identity model)

## 1. Motivation

During the July 2026 Beelink outage recovery, Joel manually minted and stored provider tokens six times in eight days (Hetzner, Cloudflare Workers, Cloudflare front-door, Backblaze B2, GitHub GHCR pending, Anthropic key for intern inference pending) — each a dashboard round-trip of scope-clicking and Infisical pasting, each gated on Joel's availability, several performed from a phone while traveling.

The manual loop produces the anti-pattern FAULTTOL FT-9.5 exists to fix: broad, long-lived keys hoarded on single machines, because minting narrow ones costs too much human friction. A keymaster hoists the human step: Joel hands it one root credential per provider, once; thereafter agents request scoped credentials over the bus. What it kills is the recurring loop; what it enables is least-privilege as the default rather than the aspiration.

The doorman's real function is memory: not just holding keys but remembering who was given one, for what, and until when.

## 2. Concept

A Pinakes bus agent that is the sole holder of provider root credentials, exposing credential operations as bus calls on `shared.keymaster.*` topics:

- mint(provider, scope_spec, ttl, purpose) → creates a narrow credential at the provider, stores it in Infisical, returns the Infisical reference. The raw secret never transits the bus (KM-OQ2 resolved: reference-only).
- rotate(ref) → mints successor, updates Infisical in place, revokes predecessor after grace period.
- revoke(ref) → immediate provider-side revocation + Infisical tombstone.
- list(filter) / audit(filter) → ledger queries.

## 3. Requirements

Custody
- KM-1 The keymaster is the only holder of provider root credentials. Roots live in a segregated Infisical folder (/keymaster-roots) readable solely by the keymaster's machine identity; no other identity, human dashboards excepted, has read.
- KM-2 Every minted credential is written to Infisical under the requester's scope path with metadata: requester identity, provider, scope granted, purpose string, mint time, TTL, parent-root fingerprint.
- KM-3 The break-glass export (FAULTTOL FT-9.3) covers /keymaster-roots. The keymaster does not replace break-glass; it is covered by it (v0.1 OQ5 resolved: separate).

Operations
- KM-4 Mint requests carry a bus identity (BUSFT BF-5/BF-8) plus a purpose string. Both are recorded. Requests without purpose are rejected.
- KM-5 Approval tiers: (a) routine — narrow scope, TTL ≤ 30d, provider on the pre-approved list → auto-mint; (b) elevated — broad scope, long/no TTL, or new provider → push approval to Joel's ntfy topic, block on explicit yes, expire the request after 24h unanswered. Tier thresholds are config, reviewed quarterly.
- KM-6 Audit ledger is append-only SQLite, Litestream-replicated (BUSFT BF-2 pattern, same B2 bucket family). Every mint/rotate/revoke/deny is a row. The ledger is the security artifact.
- KM-7 Standing-credential review: a scheduled job lists credentials past 80% of TTL or older than 90 days with no rotate, and posts a digest to ntfy. Rotation stays manual in v1 (no auto-rotation schedules).

Placement & availability
- KM-8 Runs on keystone as a compose service per FAULTTOL FT-6.x conventions; tailnet-only, no public exposure ever (stricter than BUSFT BF-7: no front-door exception).
- KM-9 Keymaster downtime must not break running systems: consumers hold already-minted credentials; only new mints/rotations wait. No service may take a runtime dependency on the keymaster being up.

Coverage
- KM-10 Coverage table maintained in-repo. Initial: Cloudflare (root token with API-Tokens:Edit — mints scoped tokens via API), Backblaze B2 (master key writeKeys → per-bucket keys), GitHub (fine-grained PAT API — verify current capabilities at implementation), Anthropic/OpenAI (workspace/project API key management APIs — verify). Known gap: Hetzner (console-only token creation as of writing) — stays manual, documented.
- KM-11 Onboarding ceremony per provider: a documented checklist Joel executes once — create root, deposit in /keymaster-roots, record fingerprint, run a mint-and-revoke smoke test.

Blast radius
- KM-12 Compromise playbook authored before v1.0 (FAULTTOL FT-11.1 discipline: drills, not intentions): per-provider root revocation steps, re-onboarding order, ledger forensics query for "everything minted since T".
- KM-13 The keymaster's own bus identity and machine identity are rotatable without re-onboarding provider roots.

## 4. Non-goals (v1)

- Not a secrets injector (Infisical's job) and not an SSO/identity provider.
- No auto-rotation schedules (KM-7 digests only).
- No cross-scope minting: a `ucla` identity cannot request against personal-scope providers, regardless of approval tier (BUSFT B1 inherited).

## 5. Open questions

- KM-OQ1 (blocking, external): identity binding strength — inherits whatever BUSFT WP4 ships for BF-8. The keymaster trusts bus identity exactly as far as WP4's token registry makes trustworthy. Revisit this spec's KM-4/KM-5 once WP4 merges.
- KM-OQ2 RESOLVED: Infisical-reference-only over the bus; raw secrets never transit.
- KM-OQ3 folded into KM-11 (onboarding ceremony is now a requirement, not a question).
- KM-OQ4 folded into KM-12 (compromise playbook is now a requirement).
- KM-OQ5 RESOLVED: break-glass stays separate; keymaster roots are covered by it (KM-3).
- KM-OQ6 (new): should elevated-tier approvals (KM-5b) eventually accept approval from Claude-in-chat acting on Joel's standing directives, or remain human-only? Default: human-only until the bus identity story matures.

## 6. Sequencing

Prerequisite: BUSFT WP1 + WP4 merged and deployed (namespaces + identity registry). Then: KM implementation is a candidate for the same delegation model (intern + agent assistance) EXCEPT KM-1/KM-11 root custody, which is Joel-plus-fleet only — an intern never touches /keymaster-roots, its Infisical permissions, or the onboarding ceremony.
