# Phase 6.9 / SV2: Server State & Event Correctness Implementation Plan

> **Execution status (2026-10-27):** Tasks 0-6 and Task 7 Steps 1-3 executed on `chore/cleanup-sv2`. Task 7 Steps 4-5 (push, open the PR) are left to the reviewer step that follows. Deviations are recorded in the design doc's "Implementation notes (added at execution)".

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Where this runs:** repo `vcs-srs-server` (`D:\Projects\vcs-srs-server`), in a **fresh git worktree** on branch `chore/cleanup-sv2`, cut from `origin/main` (`dcce3ad` at planning time; contains SV1 #229, SV3 #226, SV5 #225). Create the worktree with superpowers:using-git-worktrees. All paths below are relative to the worktree root.

**Goal:** The SRS update stream is either correct or explicitly ended (`ResourceExhausted`, so the client resyncs). Event snapshots never share memory with live state. The `SyncClient` roster is complete and has no nil-GUID side effect. `UpdateClientInfo` validates, then applies atomically; it works for guests and never moves a guest across coalitions.

**Architecture:** The `events` bus gains an overflow-aware, name-filtered `Subscription`. `Publish` waits for queue space for state events and stays lossy (counted) only for `LogEntry`. `SubscribeToUpdates` uses that subscription and ends its own stream on overflow, honouring SV1's identity and grace rules. `state` gains clone helpers, used at every snapshot site in `srs` and `app`. SyncClient and UpdateClientInfo are restructured around single critical sections.

**Tech Stack:** Go 1.27.3, `-tags headless`, grpc-go (`codes`, `status`), `log/slog`, `github.com/google/uuid`, `sync/atomic`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-27-phase-6-9-sv2-state-event-correctness-design.md`. This repo commits it in Task 7; the draft is `design-SV2.md` in the orchestrator's scratchpad. Decisions D1-D11 and contract rows C13-C16 there are binding. Umbrella: client repo `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md` §3 SV2.

## Global Constraints

- **Code generation before anything builds** (gitignored `srspb/`, `voicecontrolpb/`, `vcsauthpb/`, `graphql/generated/`), exactly as `.github/actions/generate/action.yml` does it, with `protoc` 36.2 on PATH:
  ```bash
  go install google.golang.org/protobuf/cmd/protoc-gen-go@$(go list -m -f '{{.Version}}' google.golang.org/protobuf)
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
  mkdir -p srspb voicecontrolpb vcsauthpb
  protoc --go_out=./srspb --go_opt=paths=source_relative --go-grpc_out=./srspb --go-grpc_opt=paths=source_relative srs.proto
  protoc --go_out=./voicecontrolpb --go_opt=paths=source_relative --go-grpc_out=./voicecontrolpb --go-grpc_opt=paths=source_relative control.proto
  protoc --go_out=./vcsauthpb --go_opt=paths=source_relative --go-grpc_out=./vcsauthpb --go-grpc_opt=paths=source_relative vcs-auth-plugin.proto
  go run github.com/99designs/gqlgen generate
  ```
- **Server suite** (every task ends green on it): `go build -tags headless ./...`, `go vet -tags headless ./...`, `go test -race -count=1 -tags headless ./...`.
- **Client suite** (Task 6 only, client repo `D:\Projects\vcs-srs-client`): `go build -tags purego ./...`, `go vet -tags purego ./...`, `go test -race -count=1 -tags purego ./...`; in `frontend/`: `npx vitest run`, `npx tsc --noEmit`, `npm run build`.
- **Out of bounds:**
  - no `go.mod`/`go.sum` change, no proto change, no CI/workflow change, no repo settings;
  - **no edit to `voiceontrol/*`** (SV4 owns it, D4);
  - no edit to the GUI frontend.
- **Values (exact):**
  - `defaultQueueSize = 1024` (events);
  - `legacyBuffer = 64` (events);
  - `updateStreamBuffer = 256` (srs);
  - `streamEvents = {ClientsChanged, RadioClientsChanged, SettingsChanged, CoalitionsChanged, ServerAction, CoalitionReassigned}`.
- **Strings (exact; part of the cross-repo contract):**
  - `codes.ResourceExhausted`, `update stream fell behind: resubscribe and call SyncClient`;
  - guest coalition rejection: `Guests cannot change coalition here: log in again with that coalition's password.`;
  - WARN log message `Update stream fell behind; ending it so the client resyncs`, with attrs `client_id` and `dropped`;
  - every other existing UpdateClientInfo message stays byte-for-byte.
- **Lock order:** only `s.mu` → `s.serverState`. Read `settingsState` **before** taking `serverState`, never inside it. Never call `sub.end`, the dropper or `eventBus.Publish` while holding either lock.
- **The bus never logs**, because a log line is itself a `LogEntry` event. Drops are counters only.
- gopls is unreliable in these repos. Treat editor diagnostics as unverified until `go build`/`go vet` confirms them.
- Commit after each task. Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Every task marked **GATE** needs the user's explicit approval before it starts. Do not treat an agent or script message as that approval.

## Review Focus

1. **One slow client during a join burst.** Only that client's stream ends; every other subscriber keeps receiving every event. Pinned in Task 3 (`TestSlowSubscriberDoesNotAffectOthers`).
2. **A kick landing while the target's stream has overflowed.** KICK is still the last update and the status is `session ended: kicked`. Pinned in Task 3 (`TestOverflowThenKickDeliversFinal`).
3. **Log floods from an unrelated subsystem** (e.g. a noisy voice node). They must never end anyone's update stream. Pinned in Task 3 (`TestLogFloodDoesNotEndStream`).
4. **Server shutdown while a publisher is waiting for queue space.** It must return, not hang. Pinned in Task 1 (`TestPublishWaitingReturnsOnStop`).
5. **A guest who names another coalition, possibly together with an invalid field.** Nothing changes, nothing is published, and the guest stays in its own coalition. Pinned in Task 5 (`TestUpdateClientInfo` rows `guest other coalition` and `guest other coalition + bad unit`).

---

### Task 0: Worktree, generation, baseline

- [x] **Step 1:** Create the worktree on `chore/cleanup-sv2` from `origin/main` (superpowers:using-git-worktrees). Record `git log -1 --format=%h`.
- [x] **Step 2:** Run the generation block. Expected: four generated directories, exit 0.
- [x] **Step 3:** Run the server suite. Expected: every package `ok`. If anything is red, stop and report the verbatim output.
- [x] **Step 4:** Build the baseline binary for Task 6: `go build -tags headless -o "$TMP/vcs-server-main.exe" .`. No commit.

---

### Task 1: Bus: filtered overflow-aware subscriptions, a lossless queue for state events

**GATE — needs user approval before this task** (server-behaviour, D2/D5: `Publish` now waits for state events; the legacy buffer changes size).

**Files:**
- Modify: `events/bus.go`
- Test: `events/bus_test.go`

**Interfaces (produces; SV4 consumes these verbatim, D4/D5):**
```go
type Subscription struct { C <-chan Event /* unexported: ch, bus, overflow chan struct{}, overflowOnce, overflowed atomic.Bool, dropped atomic.Uint64, closeOnce */ }
func (eb *EventBus) SubscribeNames(size int, names ...string) *Subscription
func (s *Subscription) Overflow() <-chan struct{}   // closed once, on the first drop
func (s *Subscription) Dropped() uint64
func (s *Subscription) Close()                       // idempotent; unregisters and closes C; safe after Stop
type Stats struct { LogDropped, LegacyDropped, Overflows uint64 }
func (eb *EventBus) Stats() Stats
func newEventBus(queueSize int) *EventBus             // unexported; does NOT start the dispatcher
```
Existing `NewEventBus`, `Subscribe(name) chan Event`, `Unsubscribe(ch)`, `Publish` and `Stop` keep their signatures. `NewEventBus()` = `newEventBus(defaultQueueSize)` plus `go eb.startPublisher()`.

- [x] **Step 1: Write the failing tests** in `events/bus_test.go`:
  - `TestSubscribeNamesFilters`: `SubscribeNames(8, "a", "b")`. Publish `a`, `c`, `b`. `C` yields exactly `a` then `b`, and nothing else within 100 ms.
  - `TestSubscribeNamesWildcardNoDuplicates`: `SubscribeNames(8, "*", "a", "a")`. Publish `a`. Exactly one event within 100 ms.
  - `TestSubscriptionOverflow`: `SubscribeNames(2, "a")`. Publish 5 `a` without reading. `Overflow()` closes within 1 s. Poll until `Dropped() == 3`. Then `len(C) == 2` and `Stats().Overflows == 1`. Publish 1 more `a`: `Dropped()` reaches 4 and `len(C)` stays 2.
  - `TestLegacySubscribeDropsAndCounts`: `Subscribe("x")`. Publish 70 `x` unread. Poll until `Stats().LegacyDropped == 6`; `len(ch) == 64`.
  - `TestPublishWaitsForQueueSpace`: `eb := newEventBus(4)` (not started); `t.Cleanup(eb.Stop)`. `ch := eb.Subscribe("last")`. Fill with 4 `Publish(Event{Name:"fill"})`. In a goroutine, `Publish(Event{Name:"last"})` then close `done`. Assert `done` is **not** closed after 100 ms. `go eb.startPublisher()`. `done` closes within 1 s, and `ch` yields `last`.
  - `TestPublishLogEntryDropsWhenFull`: `newEventBus(1)` (not started). Publish a `fill`, then `Publish(Event{Name: LogEntry})`. It returns immediately (the call is synchronous; assert it completes within 100 ms via a goroutine and `done`). `Stats().LogDropped == 1`.
  - `TestPublishWaitingReturnsOnStop`: `newEventBus(1)` (not started), filled. A goroutine `Publish`es a `fill` (it blocks). `eb.Stop()`. The goroutine returns within 1 s.
  - `TestSubscriptionCloseIdempotent`: `Close()` twice; `C` is closed. A second subscription: `eb.Stop()`, then `C` closes within 1 s, then `Close()` does not panic.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./events/ -v`. Expected: compile FAIL (`SubscribeNames`, `newEventBus`, `Stats` undefined).
- [x] **Step 3: Implement** per design §3.1:
  - `subscribers map[string][]*subscriber`, where `subscriber{ch chan Event; sub *Subscription}`;
  - dispatch rules (legacy: drop and count; filtered: overflow-once, then skip and count; the recover guard stays);
  - `Publish`: `LogEntry` non-blocking and counted; any other event `select { case eb.queue <- e: case <-eb.stop: }`;
  - `closeSubscribers` and `Unsubscribe` handle both kinds;
  - `SubscribeNames`: dedupe the names; if `"*"` is present, register under `"*"` only;
  - counters via `sync/atomic`;
  - **no logging**;
  - update `Publish`'s doc comment to state the new semantics.
- [x] **Step 4: Run** the events tests and the server suite. Expected: PASS / green. Existing `events` tests pass unchanged.
- [x] **Step 5: Commit**: `feat(events): overflow-aware filtered subscriptions; never drop state events at the queue (SRV-08)`

---

### Task 2: SRV-09: deep-copied snapshots

Not gated: there is no wire- or user-visible change, only race removal.

**Files:**
- Create: `state/clone.go`
- Modify: `srs/srs_service.go` (UpdateRadioInfo `:351-374` only), `srs/session.go:120-129`, `srs/auth_service.go:543-547` and `:956-960`, `app/clients.go:23-43,250-347`
- Test: `state/state_test.go`, `srs/srs_service_test.go`, `app/clients_test.go`

**Interfaces (produces):**
```go
func (c *ClientState) Clone() *ClientState                        // nil -> nil
func (r *RadioState) Clone() *RadioState                          // nil -> nil; fresh Radios slice, preserving nil-ness
func (s *ServerState) CloneClientsLocked() map[uuid.UUID]*ClientState // caller holds s (R or W); nil entries skipped
func (s *ServerState) CloneRadiosLocked() map[uuid.UUID]*RadioState
```

- [x] **Step 1: Write the failing tests:**
  - `state`: `TestRadioStateCloneIndependent`. Clone `{Radios: []Radio{{ID:1, Frequency:100}}, Muted:false}`, mutate the source's `Radios[0].Frequency = 200` and `Muted = true`, and assert the clone still has 100/false. Also: a nil `Radios` stays nil, and `(*RadioState)(nil).Clone() == nil`.
  - `state`: `TestCloneClientsLockedSkipsNilAndCopies`. Three entries, one of them nil. The result has 2 entries, each with a different pointer from the source, and mutating the source's `Name` leaves the clone unchanged.
  - `srs`: `TestUpdateRadioInfoSnapshotIndependent`. Subscribe to `RadioClientsChanged`, then `UpdateRadioInfo(ctx(id), &pb.RadioInfo{Radios: []*pb.Radio{{Id:1, Frequency:251}}})` and receive the event. Then, under `serverState.Lock`, set the live `RadioClients[id].Muted = true` and `Radios[0].Frequency = 1`. The event's `Radios[id]` still has `Muted == false` and `Frequency == 251`.
  - `srs`: `TestJoinSnapshotIndependent`. Call `GuestLogin` (reuse the `auth_service_test.go` setup used by the existing guest-login tests) and receive the `ClientJoined` event. Then mutate the live `Clients[guid].Name`; the event's copy is unchanged.
  - `app`: `TestClientGettersReturnCopies`. Against a `VCSApplication` built as in the existing `app/clients_test.go` tests, `GetRadioClientMap()[id].Radios[0].Frequency = 1` and `GetRadioClients()` values do not alter `ServerState.RadioClients[id]`. The same check covers `GetClientMap` (Name).
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./state/ ./srs/ ./app/ -run 'Clone|SnapshotIndependent|GettersReturnCopies' -v`. Expected: compile FAIL (no `Clone`); after a stub, the slice assertions FAIL.
- [x] **Step 3: Implement:**
  - `state/clone.go`;
  - UpdateRadioInfo: take `radioSnap := CloneRadiosLocked()` inside the existing write section and delete the separate RLock block;
  - `removeClientIf`: use `CloneClientsLocked()`;
  - GuestLogin and UnitSelect: `CloneClientsLocked()` under their existing RLock;
  - `app`:
    - `GetClients` and `GetRadioClients`: `RLock`, values from `*v.Clone()`, skipping nil entries;
    - `GetClientMap` and `GetRadioClientMap`: `CloneClientsLocked` / `CloneRadiosLocked`;
    - MuteClient and UnmuteClient: take `CloneRadiosLocked()` before the write-section `Unlock` and delete the separate RLock block.
- [x] **Step 4: Run** the tests, then the server suite. Expected: PASS / green.
- [x] **Step 5: Commit**: `fix(state,srs,app): deep-copy client and radio snapshots (SRV-09)`

---

### Task 3: SRV-08: a slow subscriber's stream ends with ResourceExhausted

**GATE — needs user approval before this task** (wire-visible, D1: new stream status; the session is kept).

**Files:**
- Modify: `srs/srs_service.go` (`SubscribeToUpdates :388-497`), `srs/session.go` (the `errFellBehind` var beside `errSuperseded`)
- Create: `srs/slow_subscriber_test.go`
- Modify test: `srs/session_test.go` (`TestRemoveClientFinalLastUnderBusTraffic :319-374`)

**Interfaces:**
- Consumes: Task 1's `SubscribeNames`, `Subscription.{C, Overflow, Dropped, Close}` and `Stats`.
- Produces: `errFellBehind = status.Error(codes.ResourceExhausted, "update stream fell behind: resubscribe and call SyncClient")`, plus `const updateStreamBuffer = 256` and `var streamEvents = []string{…}` (Global Constraints).

- [x] **Step 1: Write the failing tests** in `srs/slow_subscriber_test.go`.

  Helpers:
  - `stalledStream` embeds `*fakeUpdateStream`, adds `release chan struct{}` and `entered chan struct{}` (cap 1), and its `Send` signals `entered` without blocking, waits for `<-release`, then delegates.
  - `startStalledSubscriber(t, s, id) (*stalledStream, <-chan error)` mirrors `startSubscriber`.
  - `publishRadio(s, id)` publishes one `RadioClientsChanged` for `id`.
  - `waitOverflows(t, bus, n)` polls `bus.Stats().Overflows >= n` for 2 s.

  Tests:
  - `TestSlowSubscriberEndsWithResourceExhausted`. `withDropper(s)`; `left := watchClientLeft(...)`.
    1. Publish 1 event and wait for `entered`. Publish 300 more, then `waitOverflows(1)`. `close(release)`.
    2. The result has `status.Code == codes.ResourceExhausted` and the exact message.
    3. `assertPresent`; `registeredSub(id) == nil`; `detachedSince(id)` is ok; `left(300ms) == 0`; dropper calls are empty.
    4. `startSubscriber(t, s, id)` again, after which `detachedSince(id)` is not ok.
  - `TestLogFloodDoesNotEndStream`. A stalled subscriber, with `entered` reached through 1 radio event.
    1. `sentinel := s.eventBus.Subscribe(events.SettingsChanged)`.
    2. Publish 500 `Event{Name: events.LogEntry}`, then 1 `SettingsChanged`, and receive it on `sentinel` (FIFO: every log entry has now been dispatched).
    3. `Stats().Overflows == 0`. `close(release)`.
    4. After 200 ms, `registeredSub(id)` is still non-nil and no result has arrived.
  - `TestSlowSubscriberDoesNotAffectOthers`. Client A is stalled; client B uses `startSubscriber`.
    1. While A is stalled, publish 300 radio events for B's id in 6 batches of 50, polling `len(B.sentCopy())` up to the running total (2 s) after each batch, so that B, a fast reader, can never legitimately overflow while A (stalled, 256 slots) must.
    2. A ends `ResourceExhausted` after `close(release)`.
    3. B's stream received all 300 (poll `len(sentCopy()) == 300`, 2 s) and B is still registered.
    4. Note: B's `fakeUpdateStream.sentCh` cap 64 is non-blocking, so count via `sentCopy`.
  - `TestOverflowThenKickDeliversFinal`. A stalled subscriber.
    1. Overflow it (`waitOverflows(1)`) **before** releasing.
    2. `final := ServerActionUpdate(events.ActionKick, id, "bye")`; `s.RemoveClient(id, ReasonKicked, final)`. `close(release)`.
    3. `assertSessionEnded(err, "kicked")`; the last sent update `== final`, sent exactly once.
- [x] **Step 2: Re-pace the existing `TestRemoveClientFinalLastUnderBusTraffic`** so it cannot overflow the 256-slot subscription: its publisher goroutine publishes one event, then waits on `stream.sentCh` (or `stop`) before publishing the next. Every assertion stays as it is.
- [x] **Step 3: Run** `go test -race -count=1 -tags headless ./srs/ -run 'SlowSubscriber|LogFlood|OverflowThenKick|FinalLastUnderBusTraffic' -v`. Expected, with Task 1 in place but the handler still on legacy `Subscribe("*")`: `TestSlowSubscriberEndsWithResourceExhausted` and `TestOverflowThenKickDeliversFinal` FAIL in `waitOverflows` (legacy drops never count as overflows); `TestSlowSubscriberDoesNotAffectOthers` FAILs (B's 64-slot legacy channel drops events, and A never ends); `TestLogFloodDoesNotEndStream` and the re-paced `TestRemoveClientFinalLastUnderBusTraffic` PASS (regression guards).
- [x] **Step 4: Implement** per design §3.2:
  - replace `Subscribe("*")`/`Unsubscribe` with `evs := SubscribeNames(updateStreamBuffer, streamEvents...)` and `defer evs.Close()`;
  - add `case <-evs.Overflow(): return fellBehind()` and a non-blocking `Overflow()` check at the top of the event branch;
  - `fellBehind` is a closure in the handler: `sub.ended()` → `finish()`; under `s.mu`, if `streams[id] == sub`, delete it and set `detachedAt[id] = s.now()`; if it was not ours, `<-sub.done` then `finish()`; otherwise `sub.end(nil, errFellBehind)`, the WARN with `dropped = evs.Dropped()`, and `finish()`.
  - **Never** call `RemoveClient`.
- [x] **Step 5: Run** those tests 5× (`-count=5`), then the server suite. Expected: PASS / green, no flake.
- [x] **Step 6: Commit**: `fix(srs): end a slow subscriber's update stream with ResourceExhausted instead of dropping events (SRV-08)`

---

### Task 4: SRV-15: complete SyncClient roster, no nil-GUID broadcast, one read lock

**GATE — needs user approval before this task** (wire-visible: the roster gains `unit_id`/`role_id`; the `CLIENT_INFO_UPDATE` broadcast stops; C14/C15).

**Files:**
- Modify: `srs/srs_service.go` (`SyncClient :111-176`)
- Test: `srs/srs_service_test.go`

- [x] **Step 1: Write the failing tests:**
  - `TestSyncClientRosterHasUnitAndRole`. `AddClient(id, {Name:"A", UnitId:"AB", Coalition:"Blue", Role: utils.MemberRole})` . `SyncClient(clientCtx(id))`. `Clients[id.String()].UnitId == "AB"`, `RoleId == uint32(utils.MemberRole)`, and `Radios[id.String()]` is present.
  - `TestSyncClientPublishesNothing`. `ch := s.eventBus.Subscribe(events.ClientsChanged)`. `SyncClient`. No event within 300 ms.
  - `TestSyncClientConcurrentWithPing`. 50 goroutines each run `SyncClient` and `Ping` for the same id while one goroutine runs `UpdateClientInfo` (valid no-op). It must be `-race` clean.
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./srs/ -run 'SyncClient' -v`. Expected: the first two FAIL; the third reports DATA RACE.
- [x] **Step 3: Implement** per design §3.4:
  - build `srsClients` and `srsRadios` from `s.serverState.Clients` / `RadioClients` inside one `RLock`, adding `UnitId` and `RoleId`; a radio is skipped when its client is missing or nil;
  - the write-locked caller section stays as it is (`LastUpdate`, coalition);
  - delete the publish block (`:152-161`) and the `GetAllClients`/`GetAllRadios` calls;
  - keep `GetAllClients`/`GetAllRadios` in `state` if anything else uses them (`git grep`); otherwise leave them.
- [x] **Step 4: Run** the tests, then the server suite (`control/session_e2e_test.go` exercises SyncClient end to end). Expected: PASS / green.
- [x] **Step 5: Commit**: `fix(srs): SyncClient roster carries UnitId/RoleId and stops broadcasting a nil-GUID update (SRV-15)`

---

### Task 5: SRV-16: UpdateClientInfo validates, then applies atomically; the guest coalition rule

**GATE — needs user approval before this task** (wire-visible, D3/D6/D7: guests succeed; failures change nothing; guests cannot change coalition; C16).

**Files:**
- Modify: `srs/srs_service.go` (`UpdateClientInfo :248-328`), `srs/utils.go` (`canSwapRoles :106-124`)
- Test: `srs/srs_service_test.go`, `srs/utils_test.go`

- [x] **Step 1: Write the failing tests:**
  - `TestCanSwapRoles`, a table over `(current, requested) → want`:
    - `(Guest, Guest) → true`;
    - `(Member, Member) → true`;
    - `(Admin, Member) → true`;
    - `(Member, Officer) → false`;
    - `(Member, Guest) → false`;
    - `(Admin, Guest) → false`.

    Use `utils.GuestRole`/`MemberRole`/`OfficerRole`/`AdminRole` (`utils/auth.go:30-33`); there are no magic numbers.
  - `TestUpdateClientInfo`, a table. Settings coalitions are `Blue` and `Red`; each row seeds one client via `AddClient` with `{Name:"Old", UnitId:"AB", Coalition:"Blue", Role:<row>}` and subscribes to `ClientsChanged` before the call. Per row, check `Success`, whether the message contains the expected substring, the resulting fields, and the number of `ClientInfoUpdated` events for the caller's id within 300 ms (0 or 1, with `ce.ClientID == id` and `ce.Clients[id].Name` equal to the new name).

    | row | role | request (name, unit, coalition, role) | Success | fields after | events |
    |---|---|---|---|---|---|
    | guest no-op rename | Guest | New, CD, Blue, Guest | true | New/CD/Blue/Guest | 1 |
    | guest other coalition | Guest | New, CD, Red, Guest | false, contains `log in again with that coalition's password` | Old/AB/Blue unchanged | 0 |
    | guest other coalition + bad unit | Guest | New, x, Red, Guest | false, contains both the unit message and the coalition message | unchanged | 0 |
    | guest unknown coalition | Guest | New, CD, Nope, Guest | false, guest message | unchanged | 0 |
    | member to Red | Member | New, CD, Red, Member | true | New/CD/Red | 1 |
    | member unknown coalition | Member | New, CD, Nope, Member | false, `Coalition not found, please select an existing coalition.` | unchanged | 0 |
    | member upgrade | Member | New, CD, Blue, Officer | false, `You cannot swap roles.` | unchanged | 0 |
    | admin downgrade | Admin | New, CD, Blue, Member | true | role Member | 1 |
    | bad name only | Member | "", CD, Blue, Member | false | unchanged (unit **not** changed) | 0 |
  - `TestUpdateClientInfoKeepsRemovedCoalition` (D7). The client has coalition `Gone`, which is absent from settings. Role Guest, request `New, CD, Gone, Guest` → success.
  - The existing `TestRPCsRefreshLastUpdate` (UpdateClientInfo row) must stay green (D6).
- [x] **Step 2: Run** `go test -race -count=1 -tags headless ./srs/ -run 'CanSwapRoles|UpdateClientInfo|RPCsRefreshLastUpdate' -v`. Expected: FAIL on the guest rows (today they always fail and partially apply), the "unchanged" rows, and `(Guest, Guest)`.
- [x] **Step 3: Implement** per design §3.5:
  - `canSwapRoles` with the new order;
  - `UpdateClientInfo`:
    1. `DoesCoalitionExist` runs before the lock;
    2. one `serverState.Lock` section does the lookup, the `LastUpdate` refresh, validation in the order name → unit → coalition → role, and then, on success, applies all fields and takes `CloneClientsLocked()`;
    3. unlock, then publish on success only;
    4. the error-joining format and the existing "client not found" bodies stay byte-for-byte;
    5. move the `Info` "Updated client info" log to the success path.
- [x] **Step 4: Run** the tests, then the server suite. Expected: PASS / green.
- [x] **Step 5: Commit**: `fix(srs): UpdateClientInfo validates then applies atomically; guests keep their coalition (SRV-16)`

---

### Task 6: Cross-repo check: the client suite against the SV2 server

**GATE — needs user approval before this task** (cross-repo: runs the VCS client's real-server integration tests against the SV2 build; read-only on the client repo).

No code change. Use the client repo at `origin/main` (`54ab0fb` or later) read-only.

- [x] **Step 1:** In the worktree root: `go build -tags headless -o "$TMP/vcs-server-sv2.exe" .`.
- [x] **Step 2:** In the client repo, with `VCS_SERVER_BIN="$TMP/vcs-server-sv2.exe"`, run `go build -tags purego ./...`, `go vet -tags purego ./...` and `go test -race -count=1 -tags purego ./...`. Expected: green, and the `internal/voice` integration tests **ran** (`go test -race -count=1 -tags purego -run Integration -v ./internal/voice/` shows `--- PASS` and no `SKIP`).
- [x] **Step 3:** In `frontend/`: `npx vitest run`, `npx tsc --noEmit`, `npm run build`. Expected: green. There is no frontend change, so a failure is pre-existing and must be reproduced on the client's `main` before it is reported.
- [x] **Step 4:** If any Go test is red, rerun it with `VCS_SERVER_BIN` set to Task 0's `vcs-server-main.exe`. Red on SV2 only means a regression or a CL1 dependency. Stop and report the verbatim output, naming the C13-C16 row it touches. No commit.

---

### Task 7: Docs, PR, manual-verification list

**GATE — needs user approval before this task** (opens a PR; the PR body carries the CL1 contract rows and the C# note reference).

**Files:**
- Create: `docs/superpowers/specs/2026-10-27-phase-6-9-sv2-state-event-correctness-design.md`. It is a copy of `design-SV2.md`; change the status line to `approved` only if the user approved it, and add an "Implementation notes (added at execution)" section for any deviation.
- Create: `docs/superpowers/plans/2026-10-27-phase-6-9-sv2-state-event-correctness.md` (this plan, with its checkboxes as executed).

- [x] **Step 1:** Run the final server suite (generation + build + vet + `test -race`). Expected: green. Keep the per-package `ok` lines for the PR body.
- [x] **Step 2:** Commit the two docs: `docs(phase-6.9): SV2 state and event correctness design and plan`.
- [x] **Step 3:** Run superpowers:requesting-code-review on the branch. Fix findings in new commits and rerun the suite.
- [ ] **Step 4:** Push and open the PR against `main`, titled `Phase 6.9 SV2: server state & event correctness`. The body contains:
  - a summary;
  - the findings closed: SRV-08, SRV-09, SRV-15, SRV-16, and SRV-20's share;
  - the user decisions D1-D4;
  - the §7 behaviour changes;
  - the C13-C16 contract table verbatim;
  - "**SV4 must merge after this PR** and consume `SubscribeNames`/`Subscription` for the voice-node stream (D4)";
  - "Note for the C# team: see `note-csharp-serveraction.md`, section *Addendum (SV2)* (handed to the maintainers separately; no issue filed in VNGD-SimpleRadioStandalone)";
  - the Task 6 result;
  - the per-package `ok` lines;
  - the manual-verification list below;
  - the closing line `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- [ ] **Step 5:** Report the PR URL and the CI result to the orchestrator. Do not merge.

---

## Manual verification (append to the client repo's `docs/superpowers/plans/2026-10-27-phase-6-9-manual-verification.md` via DOC)

None of this can be run in the environment that implements this plan.

1. **Slow VCS client resync** (after CL1 implements C13). Against a real server, stall one VCS client's stream (debugger breakpoint or network shaping) while about 300 roster or radio changes happen. The server logs `Update stream fell behind…`. The client resubscribes and `SyncClient`s without a re-login. Its roster then matches the admin GUI exactly, with no ghosts of clients that left during the stall. Other clients are unaffected.
2. **Before CL1 lands:** the same stall makes the VCS client show control lost, and a manual Reconnect recovers with the same session. Record which happened.
3. **SyncClient roster:** a VCS client joining a server where others already are shows their FFID / unit labels immediately (C14).
4. **Admin GUI:** the client list no longer flickers or refetches on every client's sync. The mute / unmute and kick flows still update the list.
5. **Burst:** 10+ clients join at once. Nobody's stream ends, and every client sees every join.
6. **C# guest `UpdateClientInfo`** (blocked on PR #253): a name or unit change succeeds and every client sees `CLIENT_INFO_UPDATE`. A coalition change is refused with the "log in again" message, and nothing changes.
7. **C# peer on `RESOURCE_EXHAUSTED`** (blocked on PR #253): it resubscribes. Note whether its roster stays diverged (expected until the C# team applies the addendum).
8. **Distributed mode:** after SV4 merges on top of SV2, the voice-node stream's overflow behaviour is SV4's checklist item, not this one.
