---
summary: "Decision-hold comparison of current Pinakes HMAC identity and audit mechanisms with Buzz and Nostr signing patterns."
read_when:
  - Discussing, planning, or reviewing Pinakes issue #7 or WP4.
  - Considering signed envelopes, asymmetric agent identity, credential rotation, or tamper-evident audit.
status: accepted
---

# BUSFT WP4: Pinakes and Buzz Comparison Memo

- Issue: [#7](https://github.com/joelkehle/pinakes/issues/7)
- Status: Recommendation accepted; WP4 implementation remains unauthorized ([maintainer ruling](https://github.com/joelkehle/pinakes/issues/7#issuecomment-5095983376))
- Prepared: 2026-07-25
- Maintainer: Joel Kehle
- Executor: Unassigned

## Executive recommendation

Do not transplant Buzz or its Nostr event model into Pinakes now.

Retain the current Pinakes HMAC authentication boundary while the two
authorities stabilize. If Joel later approves WP4, first add a metadata-only,
tamper-evident audit contract around the existing identity lifecycle. Consider
asymmetric, per-agent signed envelopes only as a separately versioned protocol
after a threat model and credential-migration design.

This memo does not authorize WP4 implementation, bus consolidation, identity
rotation, or credential changes.

## What Pinakes has today

Pinakes currently provides:

- allowlist-gated agent registration;
- passport-style registration metadata;
- persisted per-agent symmetric HMAC secrets;
- proof of the current secret before secret rotation;
- HMAC signatures over protected HTTP requests;
- operator bearer tokens for inject/observe access;
- server-authoritative scope assignment from identity namespaces;
- explicit server-authoritative grants for `shared.*`; and
- process-log records for scope denials.

Important limits:

- an HMAC secret is shared between the agent and bus, so either can produce a
  valid signature;
- a signed request is verified at ingress but is not retained as a
  self-verifying signed message object;
- denial logs are ordinary process logs, not a durable tamper-evident audit
  chain; and
- authentication, authorization, delivery durability, and audit are separate
  concerns even if one protocol touches all four.

## What Buzz contributes

[Buzz](https://github.com/block/buzz) is a larger agent-team workspace built
around Nostr events and relays, not a drop-in identity module for an HTTP
message bus.

Its documented architecture uses:

- a canonical Nostr event whose ID is a hash of the event content;
- a per-signer public key and Schnorr signature;
- relay verification that the authenticated public key, event public key,
  content hash, and signature agree;
- NIP-42 challenge authentication for WebSocket connections;
- NIP-98 signed authentication for HTTP;
- idempotent event insertion keyed by event ID;
- separate channel-membership authorization;
- explicitly ephemeral Nostr event kinds that are not stored; and
- a separate append-only audit service with per-community SHA-256 hash chains.

Primary references:

- [Buzz architecture](https://github.com/block/buzz/blob/main/ARCHITECTURE.md)
- [Buzz agent vision](https://github.com/block/buzz/blob/main/VISION_AGENT.md)
- [NIP-01: basic Nostr protocol](https://github.com/nostr-protocol/nips/blob/master/01.md)
- [NIP-42: relay authentication](https://github.com/nostr-protocol/nips/blob/master/42.md)
- [NIP-98: HTTP authentication](https://github.com/nostr-protocol/nips/blob/master/98.md)

Buzz's audit path is deliberately separate and asynchronous. Its architecture
states that an audit failure does not reject an otherwise accepted event.
That is a useful availability choice, but it means the audit chain detects
missing/tampered history rather than preventing the underlying action.

## Side-by-side

| Concern | Pinakes now | Buzz pattern | Relevance to Pinakes |
| --- | --- | --- | --- |
| Agent credential | symmetric per-agent secret | asymmetric signing key | asymmetric keys reduce verifier impersonation but add key custody and rotation work |
| Request proof | HMAC over HTTP payload | signed canonical event | signed envelopes can remain verifiable after transport |
| Identity | allowlisted string ID plus passport metadata | public key is cryptographic identity | Pinakes still needs human-readable stable IDs and scope policy |
| Authorization | server-derived scope plus shared grants | relay/channel membership checks | keep authorization server-authoritative; signatures do not grant access |
| Duplicate handling | durable `(from, to, request_id)` receipt in SQLite | event hash and idempotent insert | durable Pinakes idempotency belongs to #5 regardless of WP4 |
| Delivery | direct request/response/inform lifecycle | relay event distribution | Buzz does not replace Pinakes's direct-message lifecycle contract |
| Transient feed | bounded in-memory observe events | ephemeral event kinds | strong conceptual match: explicitly mark events that are not durable |
| Audit | ordinary denial logs | separate hash-chained append-only log | worth borrowing only after audit contents, retention, and failure policy are decided |
| Topology | two separately operated authorities today | relay/community architecture | no topology conclusion follows from the cryptographic design |

## Three choices

### A. Retain HMAC; formalize lifecycle and audit

Potential future scope:

- preserve current agent secrets and request signing;
- define issuance, rotation, revocation, and collision procedures;
- write metadata-only audit records for registration, secret rotation, scope
  denial, grant use, and selected operator actions;
- hash-chain those audit records; and
- exclude message bodies, credentials, and private metadata.

Advantages: smallest migration, compatible with current consumers, separates
audit from delivery.

Limit: the bus shares every HMAC secret and can forge an agent signature. The
audit proves the stored chain has not been silently edited; it does not provide
third-party non-repudiation.

### B. Add versioned asymmetric signed envelopes

Potential future scope:

- each agent owns a private signing key;
- Pinakes stores public keys and signed credential-rotation statements;
- canonical direct-message envelopes carry a content hash and signature;
- HMAC remains temporarily supported during migration; and
- authorization still comes from Pinakes scopes and grants.

Advantages: durable origin verification and reduced trust in the relay.

Costs: key custody, recovery, revocation, clock/replay rules, canonicalization,
consumer migration, dual-protocol operation, and careful treatment of existing
message history.

This should be a new protocol version, not an invisible WP4 patch.

### C. Adopt Buzz/Nostr as the substrate

This would replace substantial parts of Pinakes: message representation,
authentication, relay APIs, storage, subscriptions, and likely client SDKs.
It is a product/platform migration, not an identity-registry improvement.

Recommendation: reject for WP4 unless Joel separately decides to replace
Pinakes rather than evolve it.

## Recommended decision sequence

1. Complete #5's direct-message durability contract independently of WP4.
2. Keep observer events explicitly transient.
3. Define the threat model:
   - Is the bus operator trusted to speak for agents?
   - Is post-hoc third-party signature verification required?
   - Must audit gaps block writes or only alert?
4. Define metadata-only audit contents, retention, access, and external
   anchoring requirements.
5. Define credential issuance, rotation, revocation, loss recovery, and
   authority separation.
6. Choose A or B. Do not begin implementation until Joel records the choice.

## Decisions Joel would need to make

- HMAC trust is sufficient, or agents require asymmetric signing.
- Audit is best-effort detection or a fail-closed write dependency.
- Audit records remain local, replicate elsewhere, or periodically anchor to an
  independent store.
- Retention and authorized readers for audit metadata.
- Which events are durable audit facts and which are transient observations.
- Whether credentials are independent per authority even if an implementation
  registers on both.

None of these decisions requires or implies one bus.
