# Voice Path Authentication Design

**Date:** 2026-09-24
**Status:** Approved
**Scope:** UDP voice path session authentication — HELLO secret validation, source-address binding, secret generation and distribution

---

## Overview

The UDP voice path performs no authentication. `voice/server.go` `handleHelloPacket` accepts any HELLO whose sender UUID exists in server state, then unconditionally rebinds that session's address. The sender UUID travels in cleartext at bytes 11–26 of every voice packet on an unencrypted socket (`voice/protocol.go` `ParsePacket`), so observing a single packet is sufficient to take over a session.

This spec introduces a per-session voice secret: generated on the control path, delivered over authenticated TLS gRPC, presented in the HELLO payload, and validated with a constant-time comparison. It also establishes the trust model for the remaining packet types.

**In scope:** HELLO authentication, source-address binding for VOICE/KEEPALIVE/BYE, secret generation and storage, secret distribution over both live control paths, distributed-mode propagation, rate-limited rejection logging.

**Out of scope (deferred, recorded in Known Gaps):** transport encryption, per-packet authentication, DoS hardening of the receive loop.

**Hard constraint:** no client has shipped against this protocol. `currentVersion` stays `1` and the secret is mandatory, with no optional or legacy path.

---

## Threat Model

### What is fixed

| Attack | Mechanism today | Closed by |
|---|---|---|
| **Session hijack via a sniffed VOICE packet** | Attacker sniffs a UUID from any VOICE packet (no secret travels there), sends HELLO with just that UUID, `handleHelloPacket` rebinds the session to the attacker's address. Victim silently stops receiving audio. | Secret validation on HELLO — closes hijack via a UUID sniffed from ordinary voice traffic. Hijack via a captured HELLO itself remains open; see [What is not fixed](#what-is-not-fixed). |
| **Impersonation** | Attacker sends VOICE packets with a sniffed UUID from any address. `handleVoicePacket` checks only that the session exists, never the source address — and is not even passed one. | Source-address binding check |
| **Disconnect DoS** | Attacker sends a 27-byte BYE with a sniffed UUID. `handleGoodbyePacket` calls `DisconnectClient` with no address or existence check. | Source-address binding check |
| **Keepalive reflection** | Attacker spoofs the source address as the victim's and sends KEEPALIVE; the server sends its ACK to the packet source, so the ACK lands on the victim. | ACK sent to the bound address |
| **HELLO reflection** | Same, via HELLO ACK. | Secret validation (no ACK without a valid secret) |

Neither reflection primitive carries bandwidth amplification — responses are smaller than requests — so they are poor flooding tools. They are listed because they are removed, not because they were severe.

### What is not fixed

**Replay of a captured HELLO.** A HELLO carries the session ID and the secret together, in cleartext, and `handleHelloPacket` performs no freshness check — no nonce, no timestamp, no sequence validation, only a raw comparison of the secret bytes. An attacker who captures one HELLO can replay it verbatim from their own address: the secret is genuine, so validation passes, and the session rebinds to the attacker. The secret is generated exactly once, in `state.ServerState.AddClient` (`state/server.go:140`), and is never rotated or expired for the life of the session — so a captured HELLO stays valid for as long as the session lasts.

What this spec's mechanism actually buys, stated precisely so it is neither overclaimed nor underclaimed: before, the UUID needed for a hijack traveled in cleartext on every VOICE packet, and voice packets flow continuously, so sniffing any packet at any time was enough. After, only HELLO carries the secret, and a client sends HELLO once per session — at connect, and again only on reconnect after a NAT change or server restart. An attacker who starts sniffing mid-session has missed the HELLO and cannot hijack until the client re-HELLOs. The window narrows from "any packet, any time" to "one specific packet, at session start". That is a real and useful improvement. It is not closure. Closing this needs transport encryption, so the HELLO payload itself cannot be observed.

**Eavesdropping.** The payload remains cleartext. A passive observer on the network path still hears all traffic on frequencies their position lets them see. This requires transport encryption (DTLS/SRTP) and is not addressed here.

**Source-address spoofing.** The model binds a session to an address, so an attacker who can both sniff the UUID *and* forge the victim's source address — without needing to receive replies — can still inject VOICE or BYE packets. On a shared LAN this is achievable. The bar rises from "observe one packet" to "observe one packet and forge addresses blind", which is a real improvement and not a wall.

**Availability.** See Known Gaps.

---

## Trust Model

> **The secret authenticates the establishment of a binding. The binding authenticates everything else.**

- **HELLO** carries the secret and is the *only* packet type that may create or change a session's address binding.
- **VOICE, KEEPALIVE, BYE** carry no secret and must arrive from the currently bound address, or they are dropped.

### Why KEEPALIVE does not carry the secret

This was considered explicitly and decided against, for a reason that runs opposite to a defense-in-depth reflex:

1. **It cannot hijack.** `handleKeepalivePacket` never rebinds — it updates `LastSeen` and latency on an existing entry. Rebinding is the attack, and KEEPALIVE does not rebind.
2. **It would increase exposure.** The secret is long-lived and the wire is cleartext. Putting it in every keepalive would hand a passive sniffer the credential every few seconds instead of once at session start. Fewer appearances on the wire is strictly safer here.
3. **The address check already makes a spoofed keepalive inert.** With the binding check, an off-path attacker gains nothing; an on-path attacker who can forge addresses would also be able to replay a sniffed secret.

A test pins the invariant that KEEPALIVE must never modify `client.Addr`, so a future change cannot silently turn it into a rebind path.

---

## Secret Lifecycle

### Generation and storage

`state.ClientState` gains a `VoiceSecret string` field. It is generated inside `state.ServerState.AddClient`, not at the call sites.

```go
// state/server.go
const VoiceSecretBytes = 32 // 256 bits; encodes to 43 base64url chars

func generateVoiceSecret() string {
    b := make([]byte, VoiceSecretBytes)
    rand.Read(b) // crypto/rand; cannot fail on Go 1.24+
    return base64.RawURLEncoding.EncodeToString(b)
}
```

Generating inside `AddClient` makes "every client in `ServerState` has a voice secret" an invariant of the type rather than a convention that two handlers must remember. `AddClient` has exactly two callers, both in `srs/auth_service.go` — guest login (:329) and unit select (:752) — and both need it. `crypto/rand.Read` cannot fail on Go 1.25, so no error return and no signature change.

### Why not reuse the control-path secret

Reuse is not a trade-off; it is impossible. `srs/auth_service.go:760` does `delete(s.authenticatingClients, clientGuid)` on unit-select success — the control secret is destroyed at the exact moment the client begins to exist in `ServerState`. Its five-minute expiry never comes into play. Deriving from it fails for the same reason: the source value is gone.

### Why not diceware

The control path uses five diceware words joined with `-`. Nothing human ever reads the voice secret, so readability buys nothing, and a fixed length keeps both the constant-time comparison and the wire format trivial. 32 bytes from `crypto/rand` is the right primitive.

### Lifetime

The secret lives as long as the client's entry in `ServerState`. It is not rotated, and it is unchanged by a coalition rebalance — all voice nodes replicate the same state, so one secret works on any node the client is redirected to.

---

## Protocol Changes

### `srspb` — delivery to the client

Two paths reach a client today. Both are TLS gRPC (`control/server.go:158`) and both already identify the caller via `clientIDFromContext`.

```protobuf
message ServerSyncResult {
  map<string, ClientInfo> clients              = 1;
  map<string, RadioInfo>  radios               = 2;
  ServerSettings          settings             = 3;
  string                  coalition_voice_addr = 4;
  string                  global_voice_addr    = 5;
  string                  voice_secret         = 6; // presented in the HELLO payload
}

message VoiceAddressUpdate {
  string coalition_voice_addr = 1;
  string global_voice_addr    = 2;
  string voice_secret         = 3; // re-sent so a redirect is self-contained
}
```

Populated at `srs/srs_service.go:142` (`SyncClient`) and `srs/srs_service.go:402` (the `CoalitionReassigned` branch of `SubscribeToUpdates`). The secret does not change across a rebalance; re-sending it on `VoiceAddressUpdate` is redundancy for the client's benefit, not rotation.

### `srspb` — annotating unreachable messages

`ServerInitializationResponse`, `VoiceHostDetails` and `DistributionUpdate` already declare secret fields, but all three messages are unreachable: `ServerInitializationResponse` is neither the request nor the response of any RPC in `srs.proto`, `DistributionUpdate` is declared as `ServerUpdate.voice_hosts = 5` but `buildServerUpdate` never emits that variant, and `VoiceHostDetails` is never constructed outside generated code.

They are left unimplemented and annotated, so nobody wires a client against a field the server never fills:

```protobuf
// UNIMPLEMENTED: not reachable from any RPC. The server never constructs this message.
// The live voice secret is ServerSyncResult.voice_secret.
message ServerInitializationResponse { ... }

message VoiceHostDetails {
  ...
  // UNIMPLEMENTED: never populated. Use ServerSyncResult.voice_secret.
  optional string secret = 4;
}

// UNIMPLEMENTED: never emitted; ServerUpdate.voice_hosts is never set.
message DistributionUpdate { ... }
```

### `voicecontrolpb` — distributed mode

```protobuf
message VoiceClientInfo {
  string name         = 1;
  string coalition    = 2;
  string unit_id      = 3;
  uint32 role         = 4;
  string voice_secret = 5;
}
```

This is load-bearing, not optional. In distributed mode the voice node is a separate process whose `ServerState` is populated from the control stream, never through `AddClient`. Two consequences:

1. Without this field a distributed voice node has no secret to compare against and every HELLO fails.
2. `voiceontrol/client.go:299` (`applyDelta`, `INFO_UPDATED`) constructs a **whole new** `ClientState`. A secret not carried in the delta is wiped the first time a player changes unit, and every subsequent HELLO fails. The existing `Muted`-preservation special-case at client.go:317 exists for exactly this reason; carrying the field is preferable to adding a second preservation hack.

Producers: `voiceontrol/service.go:576` (`buildStateSnapshot`) and `:626` (`buildClientDelta`).
Consumers: `voiceontrol/client.go:271` (`applySnapshot`) and `:299` (`applyDelta`).

The voice↔control channel is TLS (`voiceontrol/client.go:72`), so the secret is confidential in transit.

---

## Voice Wire Format

The HELLO payload is empty today and has no other consumer, so it is available.

**HELLO payload:** the UTF-8 bytes of the 43-character base64url secret, at offset 0, fixed length. Bytes beyond offset 43 are reserved and ignored.

No length prefix: there is no variability to encode, and a fixed length means there is no attacker-controlled length field to validate.

```go
// voice/protocol.go
const VoiceSecretLen = 43 // base64url(32 bytes), unpadded

func NewVCSHelloPacket(clientId uuid.UUID, secret string) *VCSPacket

// HelloSecret returns the secret carried in a HELLO payload.
// ok is false if the payload is shorter than VoiceSecretLen.
func (p *VCSPacket) HelloSecret() (secret string, ok bool)
```

`currentVersion` stays `1`. No client has shipped against version 1, so there is no compatibility window to maintain and no optional mode to support.

---

## Server Changes

### `state` — secret lookup

```go
// GetVoiceSecret returns the client's voice secret.
// ok is false if the client is unknown.
func (s *ServerState) GetVoiceSecret(clientGuid uuid.UUID) (string, bool)
```

This subsumes the `DoesClientExist` call in `handleHelloPacket`. `DoesClientExist` itself stays — it has other callers.

### `voice` — HELLO validation

```go
func (v *Server) handleHelloPacket(packet *VCSPacket, addr *net.UDPAddr) {
    expected, known := v.serverState.GetVoiceSecret(packet.SenderID)
    if !known {
        v.rejectHello("unknown client", packet.SenderID, addr)
        return
    }
    if expected == "" {
        v.rejectHello("no secret on record", packet.SenderID, addr)
        return
    }
    presented, ok := packet.HelloSecret()
    if !ok {
        v.rejectHello("missing or short secret", packet.SenderID, addr)
        return
    }
    if subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
        v.rejectHello("invalid secret", packet.SenderID, addr)
        return
    }
    // bind, report, ACK
}
```

Ordering notes:

- The empty-secret check is defensive, covering a voice node that received a delta from an older control server. Without it, an empty stored secret would match an empty presented one.
- `subtle.ConstantTimeCompare` returns 0 on a length mismatch; `HelloSecret` already rejects short payloads, so the comparison operates on equal, fixed lengths.
- On **any** failure: no rebind, no ACK, no `ReportClientConnected`, no state change whatsoever.

### `voice` — source-address binding

```go
// isBoundAddr reports whether addr is the address currently bound to clientID.
func (v *Server) isBoundAddr(clientID uuid.UUID, addr *net.UDPAddr) bool
```

Comparison is `client.Addr.IP.Equal(addr.IP) && client.Addr.Port == addr.Port`, not `addr.String()`, so an IPv4-mapped-IPv6 representation difference cannot produce a spurious mismatch.

`handlePacket` threads the source address into `handleVoicePacket`, `handleTestFrequencyPacket` and `handleGoodbyePacket`, which do not receive it today. All four non-HELLO handlers drop packets failing the check.

### `voice` — keepalive ACK destination

`handleKeepalivePacket` currently sends its ACK to the packet source (`server.go:213`) rather than to `client.Addr`. Changed to `client.Addr`.

**Behaviour change:** a client whose NAT remaps must re-HELLO to recover, rather than silently rebinding via keepalive. This is correct under this trust model — HELLO is the only rebind path by design — but it is a change and is called out in the PR.

### `voice` — nil connection guard

`handleHelloPacket` has no `v.conn == nil` guard, unlike `handleKeepalivePacket` (`server.go:203`). One is added, matching the existing pattern.

### `voice` — rate-limited rejection logging

A single global token bucket on `Server`, deliberately **not** a per-source map:

```go
type rejectLimiter struct {
    mu         sync.Mutex
    lastLogged time.Time
    suppressed int
}

const rejectLogWindow = 30 * time.Second
```

The first rejection in a window logs at `Warn` with the reason, sender ID and source address. Further rejections in the window increment `suppressed`. The next log after the window carries the suppressed count.

Per-source keying was rejected: UDP source addresses are spoofable, so a per-IP map is itself a memory-exhaustion vector — an attacker cycling forged source addresses would grow it without bound — and per-IP attribution is unreliable for the same reason. A flood costs one log line per 30 seconds and no per-packet allocation.

---

## Testing

`voice/server_test.go` constructs servers directly and calls handlers; new tests follow that pattern. Because the success path writes an ACK, tests bind a real `127.0.0.1:0` socket rather than relying on the nil-conn guard, so ACK assertions are genuine. Asserting the *absence* of an ACK uses a short read deadline and expects a timeout.

| Test | Asserts |
|---|---|
| `TestHelloValidSecretBinds` | Correct secret binds the address and an ACK is sent |
| `TestHelloWrongSecretDoesNotBind` | No binding is created |
| `TestHelloWrongSecretDoesNotOverwriteBinding` | Victim bound to addr A; attacker HELLO from addr B with a wrong secret leaves the binding at A and sends no ACK to B |
| `TestHelloMissingSecretRejected` | Empty payload is rejected |
| `TestHelloShortSecretRejected` | Truncated payload is rejected |
| `TestHelloUnknownClientRejected` | Pre-existing `DoesClientExist` rejection still holds |
| `TestHelloEmptyStoredSecretRejected` | An empty stored secret matches nothing |
| `TestVoiceFromUnboundAddressDropped` | Impersonation is blocked |
| `TestByeFromUnboundAddressDoesNotDisconnect` | Spoofed BYE is inert |
| `TestKeepaliveFromUnboundAddressIgnored` | Spoofed keepalive does not refresh `LastSeen` |
| `TestKeepaliveNeverRebindsAddress` | Pins the invariant that KEEPALIVE cannot rebind |
| `TestKeepaliveAckGoesToBoundAddress` | ACK destination is `client.Addr`, not the packet source |
| `TestHelloSecretRoundTrip` (protocol) | `NewVCSHelloPacket` → serialize → parse → `HelloSecret` |
| `TestAddClientGeneratesUniqueVoiceSecret` (state) | Secret is non-empty, correct length, and differs per client |
| `TestSnapshotAndDeltaCarryVoiceSecret` (voiceontrol) | Secret survives snapshot and `INFO_UPDATED` delta |

**Mutation verification.** Every test must be shown to fail before it is trusted. For each, the specific guard it covers is reverted, the test is run and observed failing, and the guard is restored. A test that passes against the unfixed code is not evidence.

---

## Documentation

`README.md` "Voice Communication Protocol" is updated: the HELLO packet-type description, the header-structure payload row, the communication-flow steps, and a new subsection stating the trust model and what it does and does not defend against.

---

## Known Gaps

Recorded here and in the PR so this change is not read as "voice is now secure".

1. **No transport encryption.** Payloads are cleartext; passive eavesdropping is unaffected. Requires DTLS/SRTP.
2. **Source-address spoofing.** An on-path attacker who forges the victim's address can still inject VOICE and BYE. Would require per-packet authentication, which is better subsumed by transport encryption than built separately.
3. **DoS exposure in the receive loop**, deferred to its own PR with benchmarks:
   - `server.go:130` spawns a goroutine and allocates per datagram, unbounded and with no backpressure.
   - `broadcastVoice` (`server.go:280`) spawns a goroutine per listening client per voice packet.
   - `server.go:118` linear-scans `BannedClients` under `RLock` for every datagram, before parsing, contending with handler goroutines on the same lock.

   This change does remove both reflection primitives and adds log-flood protection, but does not address the above.
4. **Client side is unimplemented.** Phase 5 of `vcs-srs-client` has not started. Until it ships, no client sends a secret and every HELLO is rejected — the protocol change is not live.

---

## Build and CI Notes

### Pre-existing headless build break (fixed in a separate commit)

`go test -tags headless ./...` — the command CI runs — has been failing on both `main` and `develop`. The `test.yml` workflow has failed on all of its last 8 runs, going back to at least July 2026.

Cause: the `services` package is GUI-only. Its sole importer, `main.go`, is `//go:build !headless`, as is `app/app_gui.go`, which supplies the `guiApp` embedded struct holding the `App *application.App` field. In headless builds `app/app_headless.go` substitutes an empty `guiApp`, so the eight `c.App.App` / `s.App.App` references in `services/coalitions.go` and `services/settings.go` do not compile. The `services` files were simply never given the build constraint their importer already has.

Fix: add `//go:build !headless` to the five files in `services/`, matching the existing pattern. Verified — the full headless suite passes and the GUI build is unaffected. This lands as its own commit (`fix: tag services package as GUI-only`), described separately in the PR body, because it is unrelated to voice authentication and should be reviewable on its own.

### Local toolchain note (no repository change)

`GOROOT` is exported in the local shell environment, pinned to a module-cache toolchain path, while `go` on `PATH` is a different install. This breaks standard-library resolution with misleading `package X is not in std` errors. Go commands in this work run as `env -u GOROOT go ...` with `GOCACHE` redirected to a writable directory. This is a local environment issue only — CI is unaffected and no repository change is warranted.
