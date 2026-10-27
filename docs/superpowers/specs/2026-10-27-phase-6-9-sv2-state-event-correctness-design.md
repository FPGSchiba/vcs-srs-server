**Status: approved** (user approval of SV2, 2026-10-27; open questions Q1-Q4 resolved by user decision 2026-10-27)

# Phase 6.9 / SV2: Server state & event correctness

**Repo:** `vcs-srs-server`. Branch `chore/cleanup-sv2`, cut from `origin/main` (`dcce3ad` at design time: #229 SV1, with SV3 #226 and SV5 #225 merged earlier). `origin/main` was fetched again for pass 2 and was still `dcce3ad`, so every anchor below holds.
**Umbrella:** client repo `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md`, §3 SV2, §10.
**Builds on SV1 (merged):** `subscription` / `sub.end` / `streams` / `detachedAt` (`srs/session.go`), the stream loop in `SubscribeToUpdates` (`srs/srs_service.go:388-497`), and the SV1 → CL1 contract table (server `docs/superpowers/specs/2026-10-27-phase-6-9-sv1-session-liveness-design.md` §11, rows C1-C12). SV2 adds rows C13-C17 (§9).

## Decisions

| # | Decision | Value | By |
|---|---|---|---|
| D1 | Status a slow subscriber's stream ends with (Q1) | **`codes.ResourceExhausted`, message `update stream fell behind: resubscribe and call SyncClient`.** The session is kept. The handler never calls `RemoveClient`. It unregisters its own subscription by identity under `s.mu` and sets `detachedAt[id] = s.now()`, so the SV1 120 s grace applies. | **user decision** (2026-10-27) |
| D2 | Buffer sizes and queue policy (Q2) | The SRS stream subscription has **256** slots and receives only stream-relevant events, so `LogEntry` and the other non-stream events are filtered out. The bus queue grows 256 → **1024**. State events are **never** dropped at the queue: `Publish` waits for space. `LogEntry` stays lossy at the queue, and its drops are counted. The legacy `Subscribe` buffer grows 10 → **64**. | **user decision** (2026-10-27) |
| D3 | May `UpdateClientInfo` change a guest's coalition? (Q3) | **No.** A guest must send its current coalition. Any other value is rejected with `Guests cannot change coalition here: log in again with that coalition's password.` A non-guest may pick any existing coalition, as `UnitSelect` allows. The call validates every field first, applies them atomically, then publishes. | **user decision** (2026-10-27) |
| D4 | Voice-node (`voiceontrol`) `"*"` subscriber, which has the same silent-drop bug (Q4) | **SV4 fixes it** with SV2's overflow-aware filtered subscription (D5). SV2 does not touch `voiceontrol/*`. SV2 must expose the API cleanly, and **SV4 merges after SV2**. | **user decision** (2026-10-27) |
| D5 | Bus API for overflow-aware subscriptions | `(*EventBus).SubscribeNames(size int, names ...string) *Subscription`. A `Subscription` carries `C <-chan Event`, `Overflow() <-chan struct{}` (closed once, on the first drop), `Dropped() uint64` and `Close()` (idempotent; unregisters and closes `C`). Duplicate names are collapsed. If `"*"` is among the names, the subscription is registered **only** under `"*"`, so no event is delivered twice. An overflowed subscription receives nothing more; each skipped event is counted. `(*EventBus).Stats() Stats{LogDropped, LegacyDropped, Overflows uint64}` exists for tests and diagnostics. The bus has no logger, and **nothing logs a drop**, because a log line is itself a `LogEntry` event and would feed back into the bus. | worker's call (the shape SV4 consumes) |
| D6 | `LastUpdate` on a failed `UpdateClientInfo` | **Still refreshed**, with no event published. It records liveness, not a state change (SV1 D9). The existing `TestRPCsRefreshLastUpdate` pins it. | worker's call |
| D7 | Coalition rule details | Sending an **unchanged** coalition is always accepted, for guests and non-guests alike, even when an admin has since removed that coalition from the settings. Otherwise a client in a removed coalition could never update its name. A **change** requires a non-guest caller *and* a coalition for which `DoesCoalitionExist` holds. | worker's call (implements D3) |
| D8 | `SyncClient`'s nil-GUID `ClientInfoUpdated` publish | **Deleted**, not repaired. A SyncClient changes no roster field (only the caller's `LastUpdate`), so there is nothing to announce. | worker's call |
| D9 | How fast an overflowed stream ends | The event branch checks `Overflow()` (non-blocking) before forwarding, so the handler does not first drain the buffer to a client that has already fallen behind. | worker's call |
| D10 | Overflow racing a server removal | If, under `s.mu`, the handler finds that its subscription is no longer registered, someone else (a removal or a newer subscriber) owns the ending. It then waits for `sub.done` and returns `finish()`, so a KICK or BAN final update is still delivered. If the overflow unregisters **first**, a later `RemoveClient` finds no subscription. The client then sees `ResourceExhausted` and, on resubscribe, C7 (`Unauthenticated`). The KICK reason is lost only in that narrow race, which is accepted. | worker's call |
| D11 | Snapshot helpers | `(*ClientState).Clone`, `(*RadioState).Clone` (fresh `Radios` slice) and `(*ServerState).CloneClientsLocked` / `CloneRadiosLocked` (the caller holds the lock; nil entries are skipped). They are used at every event-snapshot site and in the `app` getters. | worker's call |

## 1. Intent

The SRS update stream sends only deltas. Three things can leave a client's roster different from the server's with nothing logged:

- events dropped for a slow subscriber;
- a roster snapshot without `UnitId`/`RoleId`;
- data races on shared state pointers.

SV2 makes the stream either correct or explicitly ended, so the client knows to resync. It also makes `UpdateClientInfo` all-or-nothing, makes it usable by guests, and stops it from moving a guest across coalitions.

## 2. Re-verification

Pass 1 re-checked every finding on `dcce3ad`, both by reading the code and with `-race` probe tests in a throwaway worktree. Pass 2 confirmed `origin/main` is unchanged (`dcce3ad`). It also re-read every anchor used by the plan: `events/bus.go`, `srs/srs_service.go:111-176,248-379,388-497`, `srs/session.go:95-148`, `srs/utils.go:106-124`, `srs/auth_service.go:543-547,956-960`, `app/clients.go:23-43,250-347` and `state/server.go:14-183`.

| ID | Status | Evidence on dcce3ad |
|---|---|---|
| SRV-08 | **holds-with-correction** | There are two drop sites. `events/bus.go:11-15`: `safeSend`'s `default:` branch drops one subscriber's event. `events/bus.go:116-123`: `Publish` drops at the 256-slot queue, so **every** subscriber loses that event. The subscriber buffer is 10 slots (`bus.go:86`). `SubscribeToUpdates` subscribes to `"*"` (`srs_service.go:400`) and blocks in `stream.Send`. **Correction:** `"*"` also delivers every `LogEntry`, because `appLogger` fans out to `logging.BusHandler` (`logging/bus_handler.go:62`). Log lines therefore fill the slots, and the stream then throws them away. Probe: with `Send` stalled, 50 `SettingsChanged` events were published and 11 delivered; the stream stayed open with no log line. |
| SRV-09 | **holds-with-correction** | Shallow snapshots (`clientsSnap[k] = v`) are taken in SyncClient (`srs_service.go:153-157`), UpdateClientInfo (`:315-318`), UpdateRadioInfo (`:366-370`, `RadioState`), GuestLogin (`auth_service.go:543-547`) and UnitSelect (`:956-960`). Writes happen in place under the lock (`srs_service.go:170,201,269-301` and `app/clients.go:266,305`). Readers read without the lock: `buildServerUpdate`, `voiceontrol.buildClientDelta`, and the Wails GUI marshalling `event.Data`. `-race` probes found 2 data races: an UpdateRadioInfo snapshot against a MuteClient-shaped `Muted` write, and a SyncClient snapshot against `Ping`'s `LastUpdate` write. **Correction:** SyncClient also reads `client.State.*` without the lock, through the pointers that `GetAllClients`/`GetAllRadios` return. `removeClientIf` (`session.go:120-129`) and `app/clients.go` already take struct copies, but those copies still share the `Radios` slices. |
| SRV-15 | **holds** | `srs_service.go:125-129` builds the roster without `UnitId`/`RoleId`. `:158-161` publishes `ClientInfoUpdated` with a zero `ClientID`. VCS drops it (`internal/control/stream.go:37`), and so does `voiceontrol.buildClientDelta`. The GUI `ClientList.tsx` re-fetches on it, so every SyncClient also triggers a pointless GUI refetch. |
| SRV-16 | **holds-with-correction** | `canSwapRoles` returns false whenever the requested role is `GuestRole` (`srs/utils.go:106-110`), so a guest's call always fails. Name, UnitId and Coalition are written *before* the role check (`srs_service.go:264-298`). **Correction (security-relevant):** a guest that names any existing coalition is moved into it today, while being told the call failed. That bypasses the coalition password, which is exactly the isolation SV3 enforces on voice. Callers: the C# peer (`VcsClientSyncHandler.cs:489-512`); the VCS client never calls it. |

No finding is refuted or already fixed.

## 3. Design

### 3.1 SRV-08: the bus (`events/bus.go`)

- **Internals.** `subscribers map[string][]*subscriber`, where `subscriber{ch chan Event; sub *Subscription}` and `sub == nil` marks a legacy channel. `NewEventBus()` delegates to an unexported `newEventBus(queueSize int) *EventBus` that does not start the dispatcher, then starts it. This lets tests hold a full queue deterministically.
- **Constants.** `defaultQueueSize = 1024` and `legacyBuffer = 64`.
- **`dispatch`.**
  - A legacy subscriber gets a non-blocking send. If its channel is full, the event is dropped and `LegacyDropped` is incremented.
  - A filtered subscriber that has already overflowed gets nothing; `Dropped` is incremented.
  - Otherwise a filtered subscriber gets a non-blocking send. If its channel is full, the subscription is marked overflowed (`Overflow()` is closed once, `Overflows` is incremented) and `Dropped` is incremented.
  - The recover-on-closed-channel guard stays.
- **`Publish`.**
  - `LogEntry`: non-blocking. If the queue is full, the entry is dropped and `LogDropped` is incremented.
  - Every other event: `select { case eb.queue <- e: case <-eb.stop: }`.
  - **This cannot deadlock.** The dispatcher only takes `eb.mu` and never blocks on a subscriber. No publisher holds `eb.mu`, the dispatcher never publishes, and `Stop` releases every waiting publisher. A publisher that holds its own lock while it waits is therefore safe; `voiceontrol` rebalancing does this while holding `s.mu`.
- **Shutdown.** `closeSubscribers` (on `Stop`) and `Unsubscribe` handle both subscriber kinds. `Subscription.Close` is idempotent and safe after `Stop`.

### 3.2 SRV-08: the SRS stream (`srs/srs_service.go` `SubscribeToUpdates`)

- **Subscription.** `evs := s.eventBus.SubscribeNames(updateStreamBuffer, streamEvents...)` with `defer evs.Close()`. `updateStreamBuffer = 256`. `streamEvents` = `ClientsChanged`, `RadioClientsChanged`, `SettingsChanged`, `CoalitionsChanged`, `ServerAction` and `CoalitionReassigned`, which are exactly the events `buildServerUpdate` and the CoalitionReassigned branch use.
- **New status.** `errFellBehind = status.Error(codes.ResourceExhausted, "update stream fell behind: resubscribe and call SyncClient")`.
- **The loop gains `case <-evs.Overflow():`.** The event branch also checks it, without blocking, before it forwards (D9). Both lead to `fellBehind()`:
  1. If `sub.ended()`, return `finish()`.
  2. Under `s.mu`: if `s.streams[id] == sub`, delete the entry, set `detachedAt[id] = s.now()` and record `mine = true`.
  3. If not `mine`, wait for `<-sub.done` and return `finish()` (D10).
  4. Otherwise call `sub.end(nil, errFellBehind)`, log one WARN `Update stream fell behind; ending it so the client resyncs` with `client_id` and `dropped`, and return `finish()`.
- **Never `RemoveClient`.** No `ClientLeft` is published and no voice binding is dropped.

### 3.3 SRV-09: deep copies (`state`, `srs`, `app`)

- **New file `state/clone.go`.** It holds `(*ClientState).Clone() *ClientState` and `(*RadioState).Clone() *RadioState`. The `RadioState` clone gets a fresh `Radios` slice that keeps the source's nil-ness. It also holds `(*ServerState).CloneClientsLocked() map[uuid.UUID]*ClientState` and `CloneRadiosLocked() map[uuid.UUID]*RadioState`. The caller holds the lock (read or write); nil entries are skipped.
- **Each event-snapshot site is taken under the same lock as its write, then the lock is released, then the event is published:**
  - UpdateRadioInfo (moved into its existing write section);
  - UpdateClientInfo (§3.5);
  - GuestLogin and UnitSelect (`auth_service.go`);
  - `removeClientIf` (it now uses the helper);
  - `app` MuteClient and UnmuteClient (their snapshots move into the write section).
- **`app` getters.** `GetClients`, `GetRadioClients`, `GetClientMap` and `GetRadioClientMap` return clones; the first two now take `RLock` instead of `Lock`.

### 3.4 SRV-15: SyncClient (`srs/srs_service.go`)

- The whole roster and radio set are built under **one** `serverState.RLock`. Roster entries carry `UnitId` and `RoleId: uint32(Role)`, as in `buildServerUpdate`. Radios still require a matching client.
- After that read, a write lock refreshes the caller's `LastUpdate` and reads its coalition, as today.
- The `ClientInfoUpdated` publish is removed (D8).

### 3.5 SRV-16: UpdateClientInfo (`srs/srs_service.go`, `srs/utils.go`)

1. `coalitionExists := s.settingsState.DoesCoalitionExist(req.Coalition)` runs **before** `serverState` is locked. This keeps settings and server-state locks from nesting.
2. Under `serverState.Lock`:
   1. Look up the client. If it is missing, unlock and return the existing "may already have been disconnected" body.
   2. Refresh `LastUpdate` (D6).
   3. Validate, collecting messages in this order:
      - name: `checkUsername`, existing message;
      - unit: `checkUnitId`, existing message;
      - coalition (D3/D7):
        - an unchanged coalition is accepted;
        - a guest sending any other coalition is rejected with `Guests cannot change coalition here: log in again with that coalition's password.`;
        - a non-guest naming a coalition that does not exist is rejected with the existing message `Coalition not found, please select an existing coalition.`;
      - role: `canSwapRoles`, existing message.
   4. If any check failed, unlock and return `Success:false`, with the messages joined in the existing format. Nothing else changes and nothing is published.
   5. Otherwise apply all four fields, take `CloneClientsLocked()`, and unlock.
3. Publish `ClientsChanged{ClientInfoUpdated, ClientID: caller}`.

**`canSwapRoles(client, roleId)`** becomes:

- `roleId == client.Role` → true (a guest's no-op);
- `roleId == GuestRole` → false;
- otherwise `client.Role > roleId`.

Roles are informational only. The JWT's role governs authorisation, and SV2 leaves it unchanged.

## 4. Files

- `events/bus.go`, `events/bus_test.go`.
- `state/clone.go` (new), `state/state_test.go`.
- `srs/srs_service.go`, `srs/srs_service_test.go`, `srs/session.go`, `srs/session_test.go`, `srs/slow_subscriber_test.go` (new), `srs/utils.go`, `srs/utils_test.go`, `srs/auth_service.go` (the two snapshot sites only).
- `app/clients.go`, `app/clients_test.go`.

There is no proto, workflow, `go.mod` or `voiceontrol/*` change, and no file is deleted. `srs/auth_service.go` and `state/*` are not in the umbrella's SV2 file list, but SRV-09 needs them. No other track edits them in Wave 3.

## 5. Tests (TDD, `-race`, `-tags headless`)

The plan pins the exact names. In outline:

- **`events`:**
  - a filtered subscription receives only its names;
  - mixing `"*"` with other names never delivers an event twice;
  - overflow fires once, counts drops, and delivers nothing afterwards;
  - legacy subscriptions drop and count;
  - `Publish` waits on a full queue and then delivers;
  - a `LogEntry` on a full queue is dropped and counted;
  - a waiting `Publish` returns on `Stop`;
  - `Close` is idempotent, including after `Stop`.
- **`state`:** clones are independent, `Radios` included.
- **`srs` slow subscriber:**
  - a stalled `Send` with more than 256 events ends the stream with `ResourceExhausted` and the exact message;
  - the session is kept, `detachedAt` is set, and there is no `ClientLeft` and no voice drop;
  - a resubscribe clears `detachedAt`;
  - a flood of 500 `LogEntry` events does not end a stalled stream;
  - one slow subscriber does not affect another;
  - an overflow plus a KICK still delivers the KICK last with `session ended: kicked`;
  - `TestRemoveClientFinalLastUnderBusTraffic` is re-paced so that it cannot overflow.
- **`srs` SRV-09:** a snapshot read while the live state is mutated is unaffected.
- **`srs` SRV-15:** the roster carries `UnitId`/`RoleId`; SyncClient publishes nothing.
- **`srs` SRV-16:**
  - table test: a guest no-op succeeds and publishes exactly one `CLIENT_INFO_UPDATE`;
  - any invalid field changes nothing and publishes nothing;
  - the guest coalition rule;
  - a non-guest may change to an existing coalition but not to an unknown one;
  - an unchanged coalition that has since been removed is accepted;
  - role rules;
  - the `canSwapRoles` unit table.
- **`app`:** the getters return independent copies.

## 6. Out of scope

- The VCS client's reaction to `ResourceExhausted`, and replacing the roster on resync (→ CL1, contract C13).
- C# peer changes (a note only).
- `voiceontrol`'s `"*"` subscriber and its unlocked `client.Coalition` read (→ SV4, D4).
- The GUI's `"*"` subscriber, which stays legacy and lossy but now has 64 slots, and the `ServerControl.tsx` client-count bug (→ DOC/SV7).
- Shrinking event payloads, changing the role in the token, and PROTO_GAPS (no proto change).

## 7. Wire- and user-visible behaviour changes (all gated)

1. A subscriber that falls behind has its update stream ended with `ResourceExhausted` `update stream fell behind: resubscribe and call SyncClient`. Today it silently misses events. The session is kept for the 120 s grace.
2. The `SyncClient` roster now includes `unit_id`/`role_id` for every client.
3. The nil-GUID `CLIENT_INFO_UPDATE` broadcast after each `SyncClient` stops. The admin GUI client list no longer refetches on every client's SyncClient.
4. Guests can call `UpdateClientInfo` successfully. A failed call changes nothing; today it partially applies, including a coalition move.
5. A guest can no longer change coalition through `UpdateClientInfo`. That move "worked" today only through the partial-apply bug. A non-guest changing to an existing coalition works as before.
6. Under burst load, `Publish` of a state event now waits for queue space instead of dropping the event for everyone.

## 8. Approval-gate items

- **Server behaviour / wire-visible:** all of §7. D1 and D3 are wire decisions; both were decided by the user.
- **Cross-repo:**
  - the CL1 contract rows C13-C17 (§9);
  - the C# team note addendum in `note-csharp-serveraction.md`, section "Addendum (SV2): the update stream can now end with RESOURCE_EXHAUSTED" (heads-up only; nothing is filed upstream);
  - the run of the client suite against the SV2 build;
  - the SV4 merge order (D4).
- **Not touched:** dependencies, proto, CI/CD, repo settings, deleted files.

## 9. Cross-track contracts

### SV2 → CL1 (appended to SV1 §11's table; CL1 codes against C1-C17)

| # | Situation | What the VCS client sees | Code | Message (informational; branch on the code) |
|---|---|---|---|---|
| C13 | A live update stream fell behind (its 256-slot buffer overflowed because the client did not drain it fast enough) | stream status, no final update | `ResourceExhausted` | `update stream fell behind: resubscribe and call SyncClient`. **Not a session loss.** The client resubscribes with the same token, then calls `SyncClient`, then applies the returned clients and radios as a **replacement** of its roster, not a merge, so clients that left during the gap disappear. No "session expired"; whether a transient "reconnecting" indicator appears is CL1's call. The C9 grace runs from the stream end, and Pings keep the session alive. It must stay distinct from C6 (`Aborted`, do **not** resubscribe) and from C5/C7 (`Unauthenticated`, log in again). If the resubscribe gets C7, the session was removed meanwhile, which is the normal C7 handling. |
| C14 | `SyncClient` roster | each `ClientInfo` carries `unit_id` and `role_id` (as `CLIENT_JOINED` / `CLIENT_INFO_UPDATE` already do) | — | CL1's FFID label can rely on it for players who were already present. |
| C15 | `SyncClient` side effects | no `CLIENT_INFO_UPDATE` is broadcast to anyone (it carried a nil GUID; VCS already dropped it) | — | — |
| C16 | `UpdateClientInfo` (VCS does not call it) | `Success:false` changes nothing; a guest must send its current coalition and role; success broadcasts `CLIENT_INFO_UPDATE` with the caller's GUID | — | — |
| C17 | `SubscribeToUpdates` subscription acknowledgement | response headers (empty metadata) as soon as the subscription is registered, before any update | — | SubscribeToUpdates sends response headers once the subscription is registered; an event published after the client observes the headers is delivered on that stream. On resubscribe (C13) the client waits for the headers before calling `SyncClient`, so no event falls between the snapshot and the stream. |

The VCS client merges the roster today (`internal/control/client.go:51-56` only upserts), so C13's "replacement" is new CL1 work.

### Other tracks

- **SV1 → SV2 (honoured):** the overflow path unregisters by identity under `s.mu`, sets `detachedAt`, calls `sub.end(nil, errFellBehind)` and never calls `RemoveClient`. The lock order stays `s.mu` → `serverState`. The `ClientLeft` snapshot uses the deep copy.
- **SV2 → SV4 (D4):** SV4 consumes `SubscribeNames` / `Subscription.{C, Overflow, Dropped, Close}` / `Stats` exactly as defined in D5. On `Overflow()`, the node stream ends, which makes the node reconnect and re-snapshot. **SV4 merges after SV2** (or rebases onto it). The two tracks share no files.
- **SV2 → DOC:** the GUI `ServerControl.tsx` client-count bug, the C# note addendum, and the manual checklist (§10 of the plan).

## 10. Risks

- **R1 Resync storms.** A burst while one client is on a slow link forces that client to resync, possibly repeatedly. Mitigations: the event filter, the 256 slots, and a cheap resync. Only the slow client is affected (tested).
- **R2 `Publish` now waits for state events.** The wait is bounded by dispatcher speed, because the dispatcher never blocks. A deadlock would need the dispatcher to publish, which it does not. The `Stop` path is tested.
- **R3 The C# peer** keeps a diverged roster after C13 until its handler calls `SyncClient` (see the note addendum). This cannot be tested here (PR #253).
- **R4 D3** stops guest coalition hopping, which the C# peer may rely on. That only ever worked through the partial-apply bug.
- **R5 The KICK reason is lost** in the narrow race where an overflow wins over a kick (D10). The client still ends up at C7.
- **R6 Lock-order regressions** from taking snapshots under the same lock as the write. `-race` covers them, and no new nesting is added (settings are read before `serverState`).

## Implementation notes (added at execution)

Executed on `chore/cleanup-sv2` from `dcce3ad`. Deviations and observations:

- **`TestRemoveClientFinalLastUnderBusTraffic` re-pace.** As planned, the publisher goroutine waits on `stream.sentCh` after each publish. Because that makes the publisher the owner of `sentCh`, the test's "wait for traffic before the kick" now polls `len(stream.sentCopy()) > 0` instead of reading `sentCh` (two readers would race for one item). Every assertion is unchanged.
- **RED for `TestSlowSubscriberDoesNotAffectOthers`** failed in `waitOverflows` (the legacy subscription never reports an overflow), not on B's event count as the plan predicted: with 50-event batches B's 64-slot legacy channel kept up. It was still red for the right reason.
- **`dispatch` and an event literally named `"*"`.** The old dispatcher delivered such an event twice to `"*"` subscribers; the new one delivers it once. Nothing publishes an event named `"*"`.
- **`GetAllRadios`** (`state/server.go`) has no caller after SV2's SyncClient change. It is left in place, as the plan says; `GetAllClients` is still used by `voice/server.go`.
- **`app` getters** `GetClients` / `GetRadioClients` now skip nil entries (previously a nil entry would have panicked on dereference).
- **Local tooling:** generation ran with `protoc` 36.0 (CI pins 36.2); the generated code built and the suite was green.
- **Cross-repo check (plan Task 6):** the client at `origin/main` `54ab0fb` (detached worktree, removed afterwards) was green against the SV2 server binary: Go build / vet / `test -race` (`-tags purego`) 25 packages `ok`; `internal/voice` integration tests ran (7 `--- PASS`, no `SKIP`); frontend `vitest` 50 files / 580 tests, `tsc --noEmit` and `npm run build` clean.

### Review follow-ups (independent review, before the PR)

- **D10 branch test.** `TestOverflowAfterUnregisterWaitsForRemover` (`srs/slow_subscriber_test.go`) reaches the branch where the handler observes its overflow after a remover has unregistered the subscription but before it has ended it. It asserts the handler waits, does not set `detachedAt`, and returns the remover's status with the final update delivered last and once. The code already behaved this way, so the test passed on first run; it was checked by mutation (replacing the wait on `sub.done` with ending the subscription itself makes it fail with "handler returned ... ResourceExhausted ... before the remover ended the subscription").
- **§3.3 snapshot sites in `GuestLogin` and `UnitSelect`.** These two RPCs keep a separate read lock for their `ClientJoined` snapshot, taken after `AddClient` (which locks internally) rather than inside the write section. The snapshot is a deep copy, so it is race-free; it can include changes made between the write and the snapshot, which only makes the roster newer. Behaviour is the same as before SV2. Moving the copy into the write section would mean changing `state.AddClient`'s signature, which is out of proportion for no observable difference.
- **`UpdateClientInfo` logging.** The INFO line "Updated client info" is now logged only when the update is applied (before SV2 it was also logged on a rejected update). The validation warnings are logged while `serverState` is held; the event-bus side of that is non-blocking (`LogEntry` publish never waits), so the only added hold time is the synchronous slog handlers. Accepted as is.

### Follow-up: subscription ack (C17)

- **Why.** CL1's Linux CI exposed a race in the C13 resync: the client resubscribed and then called `SyncClient`, but nothing told it when the new subscription existed. An event published between the `SyncClient` snapshot and the server registering the subscription was in neither, so it was lost. `SubscribeToUpdates` now calls `stream.SendHeader(metadata.MD{})` once both the event-bus subscription and the session subscription are registered and any older subscriber has been superseded. A `SendHeader` error (dead stream) is logged at WARN and returned as the handler error; the deferred cleanup then starts the C9 grace as for any client-side stream end.
- **Tests.** `TestSubscribeToUpdatesSendsHeadersOnceRegistered` and `TestSubscribeToUpdatesDeliversEventPublishedAfterHeaders` (`srs/subscribe_ack_test.go`) run a real gRPC server over `bufconn` (part of the existing `google.golang.org/grpc` module, no new dependency). Both failed before the change (no headers within 2s) and pass after; `-count=20 -race` green. `fakeUpdateStream` gained a no-op `SendHeader` because its embedded `grpc.ServerStream` is nil.
- **Clients that ignore headers** (e.g. the C# peer) are unaffected: gRPC clients receive headers implicitly and the stream's messages are unchanged. This is from reasoning, not tested (PR #253 cannot authenticate against this server).
