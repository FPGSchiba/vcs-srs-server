# Phase 6.9 / SV4: Server Voice Path & Distributed Node Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Where this runs:** repo `vcs-srs-server` (`D:\Projects\vcs-srs-server`), in a **fresh git worktree** on branch `chore/cleanup-sv4`, branched from `origin/main` **after SV2's PR has merged** (planning base `dcce3ad`; SV2 not merged at planning time). Create the worktree with superpowers:using-git-worktrees. All paths are relative to the worktree root.

**Goal:** A distributed voice node advertises a reachable address, keeps exactly one self-healing control session that never gives up, keeps its registration and coalitions across blips, never silently loses a control delta, and the UDP fan-out runs on a fixed pool that preserves per-sender order.

**Architecture:** `voice.Server` reads UDP into `dispatch`, which hashes the sender ID onto one of N FIFO shard workers with bounded, drop-on-full queues; `broadcastVoice` serialises once and writes inline. `voiceontrol.VoiceControlClient` becomes one supervisor goroutine (`run`) that loops `session()` → capped jittered backoff until `Close`. The control side (`VoiceControlServer`) deregisters by identity, carries a same-ID node's coalitions over on re-registration, and feeds each node from SV2's filtered overflow-aware subscription, ending the stream on overflow.

**Tech Stack:** Go 1.27.3, `-tags headless`, grpc-go (`codes`, `status`, `test/bufconn`, `credentials/insecure`), `hash/fnv`, `log/slog`, `github.com/google/uuid`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-27-phase-6-9-sv4-voice-path-distributed-node-design.md` (this repo). Decisions D1-D8 and worker's calls W1-W11 are in its Decisions table and §12. Umbrella: client repo `docs/superpowers/specs/2026-10-27-vcs-client-phase-6-9-cleanup-umbrella-design.md` (SV4 row). SV1 contract: this repo's `docs/superpowers/specs/2026-10-27-phase-6-9-sv1-session-liveness-design.md` §11 "SV1 → SV4".

## Global Constraints

- **Code generation before anything builds** (gitignored `srspb/`, `voicecontrolpb/`, `vcsauthpb/`, `graphql/generated/`), exactly as `.github/actions/generate/action.yml`, with `protoc` 36.2 on PATH:
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
- **Client suite** (Task 11 only; client repo `D:\Projects\vcs-srs-client`, read-only): `go build -tags purego ./...`, `go vet -tags purego ./...`, `go test -race -count=1 -tags purego ./...`; in `frontend/`: `npx vitest run`, `npx tsc --noEmit`, `npm run build`.
- **No** `go.mod`/`go.sum` change, **no** proto change, **no** CI/workflow change, **no** edit to `events/*` (SV2 owns it), `state/*`, `srs/*`, `control/*`.
- **Opus geometry / codec untouched.** The server forwards `packet.Payload` byte-for-byte; only fan-out scheduling changes. `BufferSize` (1024) and the packet format stay.
- **Values (exact):** shards `min(runtime.GOMAXPROCS(0), 8)` clamped to ≥ 2; shard queue depth `256`; backoff `min(1s·2^attempt, 30s)` jittered to `[0.8·d, d]`, no attempt cap; backoff reset after a session lasting `≥ 30s`; reconnect WARN at most once per `60s`; RPC timeout `10s` (register, heartbeat), `5s` (ReportClientConnected/Disconnected); heartbeat interval `15s` (unchanged); node-feed subscription size `256`.
- **Log messages (exact):** `"Voice shard queue full, dropping packet"` (WARN, attrs `shard`, `suppressed_since_last`); `"Control node unreachable, retrying"` (WARN, attrs `attempt`, `retry_in`, `error`, `suppressed_since_last`); `"Reconnected to Control Server"` (INFO, attr `attempts`); `"Voice node feed fell behind, ending stream"` (WARN, attrs `serverId`, `dropped`). Existing messages (`"Failed to parse voice packet"`, `"Banned client attempted to connect"`, `"Rejected voice HELLO"`, …) stay byte-for-byte.
- **publicAddr refusal error text (exact prefix):** `voiceControl.publicAddr` — the three messages: `voiceControl.publicAddr is not set: a distributed voice node must advertise an address clients can reach`, `voiceControl.publicAddr %q: unspecified address (0.0.0.0 / ::) is not reachable by clients`, `voiceControl.publicAddr %q: invalid port`.
- **Lock order:** voice node: never hold `voice.Server`'s lock while calling `VoiceControlClient.Close()` (the kick handler takes it). Control side: `s.mu` → `serverState` only; never `eventBus.Publish` or `stream.Send` while holding `serverState`.
- **SV1 contract (D6):** `SetKickHandler`/`kickMu`/`kickHandler`/`kick` stay as on `main`; the handler is set once before `ConnectControlServer` and no reconnect path clears or re-sets it.
- gopls is unreliable in these repos; treat editor diagnostics as unverified until `go build`/`go vet` confirms.
- Commit after each task; messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Every task marked **GATE** needs the user's explicit approval before it starts. Decisions D1-D7 are already the user's; the gate is the user's go-ahead to execute the behaviour change, and no agent or script message counts as that approval.

## Review Focus

1. **A reconnect racing `Close()`** (admin stops the voice server while the node is mid-`registerSelf` or in backoff): `Close` must return promptly, `run` must exit, and no RPC may follow. Pinned in Task 3 (`TestCloseStopsReconnectPromptly`, `TestCloseDuringRegister`).
2. **A kick arriving while `Stop()` runs:** the kick handler takes `v.Lock` inside `run`; `Stop` must not deadlock waiting for `run`. Pinned in Task 4 (`TestStopDuringKickDoesNotDeadlock`).
3. **One sender flooding its shard:** other senders hashed to other shards keep flowing, and the read loop never blocks. Pinned in Task 5 (`TestFloodedShardDoesNotBlockOthers`).
4. **Old record's handler still running after a same-ID re-registration** (half-open stream): it must neither deregister the new record nor overwrite its stream. Pinned in Task 6 (`TestStaleStreamEndDoesNotDeregisterNewRegistration`, `TestStaleSendErrorDoesNotDeregisterNewRegistration`).
5. **IPv6 and whitespace in `publicAddr`:** `[2001:db8::1]:5002`, bare `2001:db8::1`, `" host "`. Pinned in Task 1's table and Task 6 (`TestRegisterFormatsIPv6Address`).

---

### Task 0: Worktree, SV2 precondition, generation, baseline

- [x] **Step 1:** Confirm SV2 has merged: `git fetch origin && git log --oneline origin/main | grep -i "sv2"` shows the SV2 merge, and `git show origin/main:events/bus.go` contains SV2's filtered overflow-aware subscription (design-SV2 §3.1 shape: a constructor taking a size and event names, returning a subscription with a receive channel, an overflow signal and a drop count, removed via `Unsubscribe`). Record the **actual** names. If SV2 has not merged: stop and report to the orchestrator; do not start unless the orchestrator says to begin Tasks 1-8 on a branch from current `origin/main` and rebase before Task 9.
- [x] **Step 2:** Create the worktree on `chore/cleanup-sv4` from `origin/main` (superpowers:using-git-worktrees). Record `git log -1 --format=%h`.
- [x] **Step 3:** Run the generation block. Expected: the four generated directories exist, exit 0.
- [x] **Step 4:** Run the server suite. Expected: all packages `ok`. If red, stop and report verbatim.
- [x] **Step 5:** Build the baseline binary for Task 11: `go build -tags headless -o "$TMP/vcs-server-pre-sv4.exe" .`. No commit.

---

### Task 1: SRV-11 — `resolveAdvertisedAddr` (pure)

**Files:**
- Modify: `voiceontrol/client.go` (add the helper; no caller yet)
- Test: `voiceontrol/client_test.go`

**Interfaces:**
- Produces: `func resolveAdvertisedAddr(publicAddr string, listenPort int) (host string, port int, err error)` — used by Task 3.

- [x] **Step 1: Write the failing table test** `TestResolveAdvertisedAddr`:

```go
ok := []struct{ in string; lp int; host string; port int }{
	{"127.0.0.1:5002", 5002, "127.0.0.1", 5002},
	{"vcs.example:6000", 5002, "vcs.example", 6000},
	{"203.0.113.5", 5010, "203.0.113.5", 5010},
	{" 203.0.113.5 ", 5010, "203.0.113.5", 5010},
	{"vcs.example", 5010, "vcs.example", 5010},
	{"[2001:db8::1]:5002", 0, "2001:db8::1", 5002},
	{"2001:db8::1", 5010, "2001:db8::1", 5010},
}
bad := []struct{ in string; lp int }{
	{"", 5002}, {"   ", 5002}, {"0.0.0.0:5002", 5002}, {"0.0.0.0", 5002},
	{"[::]:5002", 5002}, {"::", 5002}, {"host:0", 5002}, {"host:70000", 5002},
	{"host:abc", 5002}, {":5002", 5002}, {"host", 0}, {"host", 70000},
}
// ok rows: err == nil and (host, port) equal; bad rows: err != nil and
// strings.HasPrefix(err.Error(), "voiceControl.publicAddr").
```

- [x] **Step 2:** `go test -race -count=1 -tags headless -run TestResolveAdvertisedAddr ./voiceontrol/` — Expected: FAIL (`undefined: resolveAdvertisedAddr`).
- [x] **Step 3: Implement** per design §3 SRV-11: trim; empty → the "is not set" error; `net.ParseIP(s) != nil` or no `:` → bare host with `listenPort`; else `net.SplitHostPort`, `strconv.Atoi` port. Reject an empty host, `ip.IsUnspecified()`, and any port outside 1-65535, with the exact messages from Global Constraints.
- [x] **Step 4:** Same command — Expected: PASS. Then the server suite — green.
- [x] **Step 5: Commit** — `feat(voiceontrol): resolve the advertised voice address from publicAddr (SRV-11)`.

---

### Task 2: SRV-12 — `nextBackoff` (pure)

**GATE — needs user approval before this task** (D2: retry forever; server-behaviour).

**Files:**
- Modify: `voiceontrol/client.go`
- Test: `voiceontrol/client_test.go`

**Interfaces:**
- Produces: `func nextBackoff(attempt int, min, max time.Duration, jitter func(time.Duration) time.Duration) time.Duration` and `func defaultJitter(d time.Duration) time.Duration` (returns a value in `[0.8·d, d]`, `math/rand/v2`). Used by Task 3.

- [x] **Step 1: Write the failing test** `TestNextBackoffCappedWithJitter`:

```go
id := func(d time.Duration) time.Duration { return d }
want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
for i, w := range want {
	if got := nextBackoff(i, time.Second, 30*time.Second, id); got != w { t.Fatalf("attempt %d: %v, want %v", i, got, w) }
}
if got := nextBackoff(100, time.Second, 30*time.Second, id); got != 30*time.Second { t.Fatal("overflow at attempt 100") }
for i := 0; i < 1000; i++ {
	d := defaultJitter(30 * time.Second)
	if d < 24*time.Second || d > 30*time.Second { t.Fatalf("jitter %v outside [24s,30s]", d) }
}
```

- [x] **Step 2:** `go test -race -count=1 -tags headless -run TestNextBackoff ./voiceontrol/` — Expected: FAIL (undefined).
- [x] **Step 3: Implement.** Double from `min` with an early stop once `≥ max` (no shift overflow for large `attempt`), cap at `max`, then apply `jitter`.
- [x] **Step 4:** Same command — PASS; server suite — green.
- [x] **Step 5: Commit** — `feat(voiceontrol): capped jittered reconnect backoff (SRV-12)`.

---

### Task 3: SRV-12 — single supervisor; real registration; R-1, R-5; SV1 kick handler kept

**GATE — needs user approval before this task** (D2 retry-forever and D3 refuse-to-start become live; server-behaviour).

**Files:**
- Modify: `voiceontrol/client.go` (restructure; delete `establishConnection`, `establishStream`, `receiveMessages`, `handleReconnection`, the connectivity monitor and the fields `stopc`, `closeOnce` (old use), `cancelMonitor`, `stream`, `connectionFailed`; `connectivity` import goes)
- Test: `voiceontrol/client_test.go` (update `newTestClient` — drop `stopc`; add a bufconn fake)

**Interfaces:**
- Consumes: Task 1 `resolveAdvertisedAddr`; Task 2 `nextBackoff`, `defaultJitter`.
- Produces (used by Task 4):
  - `func NewVoiceControlClient(serverId string, settingsState *state.SettingsState, serverState *state.ServerState, isGlobal bool, version string, logger *slog.Logger) *VoiceControlClient`
  - `func (v *VoiceControlClient) ConnectControlServer(listenPort int) error` — config errors only; starts `run` and returns.
  - `func (v *VoiceControlClient) Close() error` — idempotent; cancels, waits for `run`, closes the conn; safe if `ConnectControlServer` was never called or failed.
  - `func (v *VoiceControlClient) AssignedCoalitions() []string` (copy, under `mu`).
  - `SetKickHandler`, `ReportClientConnected(uuid.UUID, *net.UDPAddr)`, `ReportClientDisconnected(uuid.UUID)` unchanged in signature (Report* gain a 5 s timeout).
  - `func (v *VoiceControlClient) UseDialer(target string, opts ...grpc.DialOption)` — exported (doc comment: "for tests and embedding; production uses the TLS settings"); sets `dialTarget`/`dialOpts`. Must be called before `ConnectControlServer`.
  - Unexported seams (set by same-package tests after `NewVoiceControlClient`): `dialTarget string`, `dialOpts []grpc.DialOption`, `heartbeatInterval`, `backoffMin`, `backoffMax`, `backoffResetAfter`, `rpcTimeout`, `reportTimeout`, `warnEvery time.Duration`, `jitter func(time.Duration) time.Duration`. Defaults: 15 s, 1 s, 30 s, 30 s, 10 s, 5 s, 60 s, `defaultJitter`.

- [x] **Step 1: Write the bufconn fake** in `client_test.go`: `type fakeControl struct` embedding `pb.UnimplementedVoiceControlServiceServer`, recording (under a mutex) every `RegisterVoiceServerRequest`, counting `EstablishStream` and `SendHeartbeat` calls; a `down atomic.Bool` (when set, `RegisterVoiceServer` returns `status.Error(codes.Unavailable, "down")`); `breakStream()` that makes the current `EstablishStream` return `status.Error(codes.Unavailable, "broken")`; `send(*pb.ControlMessage)` onto the current stream; `EstablishStream` reads the init message, sends an empty `StateSnapshot`, then blocks until broken or its context ends. Helper `newFakeClient(t, fake) *VoiceControlClient` starts a `bufconn.Listen(1<<20)` gRPC server, builds a client with `NewVoiceControlClient("node-1", settings{PublicAddr:"127.0.0.1:5002"}, emptyState(), false, "v-test", logger)`, sets `dialTarget = "passthrough:///bufnet"`, `dialOpts = {WithContextDialer(lis.DialContext), WithTransportCredentials(insecure.NewCredentials())}`, `heartbeatInterval = 20ms`, `backoffMin = 10ms`, `backoffMax = 50ms`, `jitter = identity`, and registers `t.Cleanup(c.Close)`. Polling helper `eventually(t, 2*time.Second, func() bool)`.
- [x] **Step 2: Write the failing tests** (all `-race`):
  - `TestRegisterAdvertisesConfiguredAddress`: after `ConnectControlServer(5002)`, eventually 1 register request with `ServerAddress == "127.0.0.1"`, `UdpPort == 5002`, `Capabilities.Version == "v-test"`; a second client with `PublicAddr "vcs.example"` and `ConnectControlServer(5010)` sends `vcs.example`/`5010`.
  - `TestConnectRefusesWithoutPublicAddr`: `PublicAddr ""` → `ConnectControlServer` returns an error with prefix `voiceControl.publicAddr`; after 200 ms the fake saw 0 registers and 0 streams; `Close()` returns nil.
  - `TestReconnectIsSingleFlight`: after the first stream is up, 5× (`breakStream()`, wait for the next stream) → eventually registers `== 6` and streams `== 6`, and after a further 200 ms still exactly 6 each.
  - `TestHeartbeatGoroutinesDoNotAccumulate`: after 5 breaks, reset the heartbeat counter, wait 400 ms → count `≤ 400/20 + 3` (one loop), not ~6×.
  - `TestReconnectKeepsKickHandler`: `SetKickHandler(rec.handle)` before connect; 3 breaks; `fake.send(kickMsg(id))` on the newest stream → eventually `rec.got() == [id]`.
  - `TestFirstConnectFailureIsRetried` (R-1): `fake.down` set before connect; `ConnectControlServer` returns nil; after 300 ms clear `down` → eventually 1 successful register and 1 stream.
  - `TestRetriesForeverAndWarnsRateLimited` (D2): capturing logger; `fake.down` set; `backoffMax = 1ms`; eventually ≥ 50 register attempts; exactly 1 record `"Control node unreachable, retrying"`; then set the client's warn-limiter `lastLogged` back by `warnEvery`, wait for one more attempt → 2 records, the second with `suppressed_since_last > 0`; clear `down` → eventually 1 `"Reconnected to Control Server"` INFO.
  - `TestCloseStopsReconnectPromptly`: `fake.down` set, `backoffMin = backoffMax = 30s`; after the first failed attempt, `Close()` returns within 200 ms; the register count does not change over the next 200 ms.
  - `TestCloseDuringRegister`: the fake's register blocks until its context is done; `Close()` returns within 200 ms.
  - `TestAssignCoalitionsRace`: concurrent `applyControlMessage(AssignCoalitions)` and `AssignedCoalitions()` under `-race`.
- [x] **Step 3:** `go test -race -count=1 -tags headless ./voiceontrol/` — Expected: FAIL (compile errors for the new signatures/seams).
- [x] **Step 4: Implement** the structure in design §3 SRV-12: `ConnectControlServer` → `resolveAdvertisedAddr` (store host/port), TLS load (skipped when `dialOpts` is set), `grpc.NewClient`, start `run` with a context from `context.WithCancel(context.Background())`, return nil. `session(ctx)` registers (writing `assignedCoalitions` under `mu`), opens the stream with the session context, sends the `init` `ControlResponse` exactly as today, starts one heartbeat goroutine on a ticker of `heartbeatInterval` that exits on the session context, runs `Recv` → `applyControlMessage` inline until error, then cancels and waits for the heartbeat goroutine. `run` follows the pseudo-code in design §3, with a small time-window limiter for the WARN (it can reuse the shape of `voice.rejectLimiter`, but `voiceontrol` cannot import `voice`; write a local `warnLimiter` with `lastLogged`, `suppressed`). Update `newTestClient` for the removed fields. Existing tests must stay green unchanged otherwise.
- [x] **Step 5:** `go test -race -count=1 -tags headless ./voiceontrol/` — PASS. Then `go test -race -count=20 -tags headless -run 'Reconnect|Heartbeat|Close|FirstConnect|Retries' ./voiceontrol/` — PASS, no flakes. Server suite — the only expected break is the call site in `voice/server.go:140-153` (new signatures). Adapt it minimally so the suite is green: `version` `""`, and the listen port parsed from `Listen`'s `address` with `net.SplitHostPort` + `strconv.Atoi` (Task 4 replaces both with the bound port and `SetVersion`).
- [x] **Step 6: Commit** — `fix(voiceontrol): one supervisor goroutine per control client; retry forever; advertise the real address (SRV-11, SRV-12)`.

---

### Task 4: Voice server — bind before register, R-4 close, version

**GATE — needs user approval before this task** (D3: a distributed node with a bad `publicAddr` now fails to start).

**Files:**
- Modify: `voice/server.go:115-209` (`NewServer` unchanged; `Listen`, `Stop`, new `SetVersion`), `app/server_control.go:173` (one line after `voice.NewServer`), `example.config.yaml` (`publicAddr` comment)
- Test: `voice/server_test.go`

**Interfaces:**
- Consumes: Task 3 `NewVoiceControlClient(..., version, logger)`, `ConnectControlServer(listenPort int) error`, `Close() error`, the seams.
- Consumes also: Task 3 `UseDialer(target string, opts ...grpc.DialOption)`.
- Produces: `func (v *Server) SetVersion(version string)`; unexported test hook `controlClientHook func(*voiceontrol.VoiceControlClient)` on `Server`, called in `Listen` after `NewVoiceControlClient` and before `ConnectControlServer` (tests call `UseDialer` with a bufconn dialer through it; nil in production).

- [x] **Step 1: Write the failing tests:**
  - `TestListenBindsBeforeRegistering`: occupy a UDP port; distributed-mode server with a bufconn fake (reuse a copy of Task 3's fake in `voice/` test code, or a minimal recorder) → `Listen("127.0.0.1:<taken>", stop)` returns an error and the fake saw 0 registers.
  - `TestListenRefusesBadPublicAddr`: `PublicAddr "0.0.0.0:5002"`, `Listen("127.0.0.1:0", stop)` returns an error with prefix `voiceControl.publicAddr`; the server's socket is closed (`v.conn` is nil or a second `net.ListenUDP` on the same port succeeds).
  - `TestListenAdvertisesBoundPort`: `PublicAddr "127.0.0.1"`, `Listen("127.0.0.1:0", stop)` → the fake's register has `UdpPort` == the bound port (non-zero) and `Version` == the `SetVersion` value.
  - `TestStopClosesControlClient` (R-4): after the node registered and its stream is up, `Stop()` → the fake's stream context ends (eventually) and the register count stays 1 for 200 ms.
  - `TestStopDuringKickDoesNotDeadlock`: the kick handler is entered (fake sends `kick_client` for a bound client) while another goroutine holds the test in `Stop()`; both complete within 2 s.
- [x] **Step 2:** `go test -race -count=1 -tags headless -run 'TestListen|TestStop' ./voice/` — Expected: FAIL.
- [x] **Step 3: Implement.** `Listen`: resolve + `ListenUDP` first; if distributed, build the control client (`version` from `SetVersion`), `SetKickHandler(v.DisconnectClient)`, call the hook, `ConnectControlServer(conn.LocalAddr().(*net.UDPAddr).Port)`; on error close the socket and return it. Set `conn`/`running` under the lock, start `cleanupRoutine`, and on loop exit close the control client (outside the lock). `Stop`: under the lock do today's work and copy `controlClient`; after unlocking call its `Close()`. `app/server_control.go`: `v.SetVersion(Version)` right after `voice.NewServer(...)`.
- [x] **Step 4:** Same command — PASS; server suite — green.
- [x] **Step 5: Commit** — `fix(voice): bind UDP before registering; close the control client on stop; report the real version (SRV-11, R-4)`.

---

### Task 5: SRV-13 — sharded, order-preserving fan-out

**GATE — needs user approval before this task** (D1: overload drops frames; wire-visible ordering).

**Files:**
- Modify: `voice/server.go` (read loop at `:178-208`, `handlePacket` at `:230-258`, `broadcastVoice` at `:469-481`, `Server` struct)
- Test: `voice/server_test.go`

**Interfaces:**
- Produces: `func shardFor(id uuid.UUID, n int) int`; `func (v *Server) startShards(n, depth int)` and `func (v *Server) stopShards()` (close queues, wait); `func (v *Server) dispatch(data []byte, addr *net.UDPAddr)`; `func (v *Server) handleParsedPacket(p *VCSPacket, addr *net.UDPAddr)`; `queueFullRejects rejectLimiter` field; test hook `shardHook func()` (called by a worker before each job; nil in production). `handlePacket(data, addr)` keeps its signature.

- [x] **Step 1: Write the failing tests:**
  - `TestVoiceFanOutPreservesPerSenderOrder`: bound talker + bound listener (`addClientWithSecret`, HELLO via `handleHelloPacket`, both with a radio on 243.0 MHz in `RadioClients` so `GetListeningClients` selects the listener); `startShards(4, 4096)`; push 2,000 voice frames with increasing sequence through `dispatch`; read on the listener until 2,000 or 2 s; assert received ≥ 1,900 and **inversions == 0** (`seq[i] < seq[i-1]`).
  - `TestVoiceDispatchGoroutinesBounded`: `shardHook` blocks on a channel; `base := runtime.NumGoroutine()`; `startShards(4, 256)`; 10,000 parseable voice datagrams with `uuid.New()` senders through `dispatch`; `runtime.NumGoroutine() <= base+4+4`; release the hook; `stopShards()`; eventually back to ≤ `base+2`.
  - `TestShardForStableAndInRange` *(guard)*: 1,000 random IDs, `n` ∈ {2,3,8}: in `[0,n)` and equal on a second call.
  - `TestVoiceQueueFullDropIsRateLimited`: hook-blocked single shard of depth 1; 50 datagrams from one sender → exactly 1 `"Voice shard queue full, dropping packet"` WARN; then 10 garbage datagrams → exactly 1 `"Failed to parse voice packet"` (limiters independent).
  - `TestFloodedShardDoesNotBlockOthers`: two senders chosen so `shardFor` differs; block only sender A's shard (hook checks the shard index); sender B's frames still reach the listener within 500 ms; `dispatch` never blocks (the 300 calls for A complete within 100 ms).
  - `TestBroadcastSendsIdenticalBytes` *(guard)*: three listeners receive byte-identical frames.
  - `TestListenWaitsForWorkersOnStop` *(guard)*: non-distributed `Listen("127.0.0.1:0", stop)` in a goroutine; close `stop`; `Listen` returns within 2 s and goroutines return to ≤ baseline + 2.
- [x] **Step 2:** `go test -race -count=1 -tags headless -run 'FanOut|Dispatch|ShardFor|QueueFull|Flooded|Broadcast|WaitsForWorkers' ./voice/` — Expected: FAIL (undefined / ~350 inversions).
- [x] **Step 3: Implement** per design §3 SRV-13: FNV-1a 32 (`hash/fnv`) over `id[:]`; `dispatch` parses (parse failure → existing `parseRejects` WARN, drop), non-blocking send of `{pkt, addr}` to `queues[shardFor]`, on full → `queueFullRejects` WARN; workers `for job := range q { hook; v.handleParsedPacket(job.pkt, job.addr) }`; `Listen` calls `startShards(max(2, min(GOMAXPROCS, 8)), 256)` before the loop and `stopShards()` on exit; `broadcastVoice` serialises once and writes inline (keep the per-recipient Debug and Error logs). `ReportClientConnected`/`Disconnected` stay `go`-started (Task 3 gave them timeouts).
- [x] **Step 4:** Same command — PASS; `-count=20` on `FanOut|Flooded|Dispatch` — PASS; server suite — green (the existing `TestParseFailureLogRateLimited` etc. unchanged and green).
- [x] **Step 5: Commit** — `fix(voice): sharded order-preserving fan-out with bounded drop-on-full queues (SRV-13)`.

---

### Task 6: Control — R-2 ownership-checked deregistration; `JoinHostPort`

**GATE — needs user approval before this task** (D4; distributed-mode behaviour on the control node).

**Files:**
- Modify: `voiceontrol/service.go:158-199` (address format), `:498-565` (`EstablishStream`), `:692-704` (`deregisterNode` → `deregisterNodeIf`)
- Test: `voiceontrol/service_test.go`

**Interfaces:**
- Produces: `func (s *VoiceControlServer) deregisterNodeIf(serverID string, node *registeredNode) bool` (true if deleted; clears `globalNodeAddr` only when the deleted node is global and `globalNodeAddr == node.address`). `deregisterNode` is removed (no other caller).

- [x] **Step 1: Write the failing tests** using a controllable fake server stream (`fakeNodeStream` extended with `ctx context.Context`, a `recv` channel for the init message, and an optional `sendErr`):
  - `TestStaleStreamEndDoesNotDeregisterNewRegistration`: register `n1`; run `EstablishStream(stream1)` in a goroutine; register `n1` again; run `EstablishStream(stream2)`; cancel `stream1`'s context → its handler returns; `s.nodes["n1"]` exists and its `stream == stream2`.
  - `TestStaleSendErrorDoesNotDeregisterNewRegistration`: as above, but `stream1` has `sendErr` set and an event is published (`ClientsChanged`, `ClientJoined` for a client in a coalition `n1` owns) → handler 1 returns the error; `n1` still registered with `stream2`.
  - `TestStreamEndDeregistersOwnRecord` *(guard)*: single registration; cancel → `n1` gone.
  - `TestRegisterFormatsIPv6Address`: `ServerAddress "2001:db8::1"`, `UdpPort 5002` → `GetVoiceAddressForCoalition` returns `[2001:db8::1]:5002`; IPv4 rows unchanged (`10.0.0.1:5002`).
- [x] **Step 2:** `go test -race -count=1 -tags headless -run 'Stale|StreamEndDeregisters|IPv6' ./voiceontrol/` — Expected: FAIL.
- [x] **Step 3: Implement** `deregisterNodeIf` and use it on both exit paths of `EstablishStream` with the handler's `node`; format with `net.JoinHostPort(req.ServerAddress, strconv.Itoa(int(req.UdpPort)))`.
- [x] **Step 4:** Same command — PASS; server suite — green.
- [x] **Step 5: Commit** — `fix(voiceontrol): deregister a voice node only from its own stream; format IPv6 addresses (R-2)`.

---

### Task 7: Control — R-3 same-ID re-registration keeps its coalitions

**GATE — needs user approval before this task** (D5).

**Files:**
- Modify: `voiceontrol/service.go:158-183` (`RegisterVoiceServer`)
- Test: `voiceontrol/service_test.go`

**Interfaces:**
- Consumes: Task 6.
- Produces: no new API. Behaviour: an existing non-global record for `req.ServerId` + a non-global request → new record's `coalitions` is a copy of the old one's, `assignCoalitions` is skipped, `rebalance(true)` still runs.

- [x] **Step 1: Write the failing tests:**
  - `TestReRegistrationKeepsCoalitions`: coalitions `Blue, Red, Green, Gold`; register `n1`, `n2`; record both nodes' coalitions; re-register `n1` → its response's `AssignedCoalitions` and `s.nodes["n1"].coalitions` equal the recorded set (order-insensitive), and `n2`'s are unchanged.
  - `TestReRegistrationGlobalUnaffected` *(guard)*: re-registering a global node updates `globalNodeAddr` and takes no coalitions.
  - `TestReRegistrationDoesNotAliasCoalitions` *(guard, `-race`)*: mutating the new record's slice (via `rebalance`) does not change the old record's.
- [x] **Step 2:** `go test -race -count=1 -tags headless -run 'ReRegistration' ./voiceontrol/` — Expected: FAIL on the first.
- [x] **Step 3: Implement** in `RegisterVoiceServer` under the existing `s.mu.Lock()`.
- [x] **Step 4:** Same command — PASS; server suite — green (existing `TestRegisterVoiceServer_*` unchanged).
- [x] **Step 5: Commit** — `fix(voiceontrol): a re-registering voice node keeps its coalitions (R-3)`.

---

### Task 8: (conditional) Q5 — coalition tombstone after deregistration — **RUN (Q5 = A)**

**GATE — needs user approval before this task. Run only if the user answered Q5 = A.** If Q5 = B, skip and add the gap to the PR's follow-ups; if C, stop and re-plan.

**Files:**
- Modify: `voiceontrol/service.go` (`VoiceControlServer` gets `recentlyGone map[string]goneNode` with `goneNode{coalitions []string; at time.Time}` and a `now func() time.Time` seam, default `time.Now`; `nodeGrace = 120 * time.Second`)
- Test: `voiceontrol/service_test.go`

**Interfaces:**
- Consumes: Task 6 `deregisterNodeIf` (records a tombstone for a deleted non-global node), Task 7's carry-over path (falls back to the tombstone when there is no live record).

- [x] **Step 1: Write the failing tests:**
  - `TestReRegistrationAfterDeregistrationKeepsCoalitionsWithinGrace`: register `n1`, `n2`; deregister `n1` via its stream ending; advance `now` 60 s; re-register `n1` → gets back exactly its old coalitions.
  - `TestTombstoneSkipsCoalitionsNowOwnedElsewhere`: as above, but `n3` registers in between and takes one of `n1`'s old coalitions → `n1` gets back the others only.
  - `TestTombstoneExpires`: advance 121 s → re-registration behaves as a new node; the tombstone map is empty afterwards (lazy expiry on registration).
- [x] **Step 2:** `go test -race -count=1 -tags headless -run 'Tombstone|WithinGrace' ./voiceontrol/` — FAIL.
- [x] **Step 3: Implement.** On registration: expire tombstones older than `nodeGrace`; if no live record but a tombstone for the ID, use its coalitions minus those any live non-global node owns; delete the tombstone.
- [x] **Step 4:** Same command — PASS; server suite — green.
- [x] **Step 5: Commit** — `fix(voiceontrol): a voice node returning within 120 s keeps its free coalitions (Q5)`.

---

### Task 9: D7 — node feed on SV2's filtered overflow-aware subscription; coalition-read race fixes

**GATE — needs user approval before this task** (D7; requires SV2 merged into this branch's base — Task 0 Step 1).

**Files:**
- Modify: `voiceontrol/service.go:498-565` (`EstablishStream`), `:567-600` (`buildStateSnapshot`), `:602-690` (`buildClientDelta`)
- Test: `voiceontrol/service_test.go`

**Interfaces:**
- Consumes: SV2's subscription API exactly as recorded in Task 0 Step 1 (do not re-implement it; if it lacks an overflow signal, stop and report); SV2's slow-subscriber status code (reuse SV2's exported error/code if exported, else `status.Error(<same code>, "voice node feed fell behind: reconnect to resync")`); Task 6 `deregisterNodeIf`.
- Produces: `func (s *VoiceControlServer) detachStreamIf(node *registeredNode, stream pb.VoiceControlService_EstablishStreamServer)`; `nodeFeedSize = 256`; `nodeFeedEvents = []string{events.ClientsChanged, events.RadioClientsChanged, events.ServerAction, events.CoalitionsChanged}`.

- [x] **Step 1: Write the failing tests:**
  - `TestNodeFeedIgnoresLogFlood`: node stream whose `Send` blocks after the snapshot until released; publish 500 `events.LogEntry`; release; then publish one `ClientJoined` for an owned coalition → it is delivered and the handler is still running.
  - `TestNodeFeedOverflowEndsStreamKeepsRegistration`: `Send` blocked; publish 300 `RadioClientsChanged` for a client in an owned coalition; release → handler returns an error whose `status.Code` equals SV2's slow-subscriber code; exactly 1 WARN `"Voice node feed fell behind, ending stream"`; `s.nodes["n1"]` still exists with `stream == nil`; re-register `n1` → same coalitions (Task 7).
  - `TestNodeFeedStillForwardsAndRebalances` *(guard)*: `ClientJoined`, `RadioClientsChanged`, `ServerAction` (mute) are forwarded; a `CoalitionsChanged` triggers `rebalance(false)` (a newly added coalition gets assigned).
  - `TestBuildClientDeltaCoalitionRace` (`-race`): one goroutine builds deltas for 1,000 events while another runs `rebalance(true)` under `s.mu` and a third mutates a client's coalition under `serverState.Lock()`.
- [x] **Step 2:** `go test -race -count=1 -tags headless -run 'NodeFeed|CoalitionRace' ./voiceontrol/` — Expected: FAIL (overflow test hangs/undetected → use a 5 s test timeout via `-timeout 60s`; race test reports a DATA RACE).
- [x] **Step 3: Implement.** Subscribe with SV2's API for `nodeFeedEvents`/`nodeFeedSize`, deferred `Unsubscribe`; add an overflow `select` arm → `detachStreamIf`, WARN with the drop count, return the status error (no deregistration). Keep the context-done arm (`deregisterNodeIf`) and the send-error arm. In the two builders, copy `node.isGlobal` and `node.coalitions` under `s.mu.RLock()` once per call, and copy `client.Coalition` before `serverState.RUnlock()`.
- [x] **Step 4:** Same command — PASS; `-count=20 -run NodeFeed` — PASS; server suite — green.
- [x] **Step 5: Commit** — `fix(voiceontrol): end a lagging voice-node feed instead of dropping deltas (D7, SV2 handover)`.

---

### Task 10: Flake sweep and whole-branch review

- [x] **Step 1:** `go test -race -count=20 -tags headless ./voice/ ./voiceontrol/` — Expected: `ok` for both. Any flake: fix with polling/seams (never sleeps), new commit.
- [x] **Step 2:** Full server suite — green; save the per-package `ok` lines for the PR.
- [x] **Step 3:** Run superpowers:requesting-code-review on the branch against `origin/main`. Review brief must include the Review Focus list, the lock-order rule, and "Opus payload bytes untouched". Fix findings in new commits; rerun the suite.

---

### Task 11: Cross-repo check — VCS client suite against the SV4 server

**GATE — needs user approval before this task** (cross-repo: runs the client's real-server integration tests against the SV4 build; read-only on the client repo).

No code change. Client repo `D:\Projects\vcs-srs-client` at `origin/main`, read-only. The integration tests run voice through the headless server in non-distributed mode, which exercises Task 5's shard path end to end.

- [x] **Step 1:** `go build -tags headless -o "$TMP/vcs-server-sv4.exe" .` (worktree root).
- [x] **Step 2:** In the client repo with `VCS_SERVER_BIN="$TMP/vcs-server-sv4.exe"`: `go build -tags purego ./...`, `go vet -tags purego ./...`, `go test -race -count=1 -tags purego ./...` — Expected: green, and `go test -run Integration -v ./internal/voice/` shows `--- PASS` with no `SKIP`.
- [x] **Step 3:** In `frontend/`: `npx vitest run`, `npx tsc --noEmit`, `npm run build` — Expected: green (sanity only; a failure must be reproduced against `main` before being reported).
- [x] **Step 4:** Any red Go test: rerun with `VCS_SERVER_BIN="$TMP/vcs-server-pre-sv4.exe"` (Task 0). Red on SV4 only → stop and report verbatim. No commit.

---

### Task 12: Docs, PR, manual-verification list

**GATE — needs user approval before this task** (opens a PR).

**Files:**
- Create: `docs/superpowers/specs/2026-10-27-phase-6-9-sv4-voice-path-distributed-node-design.md` (copy of the approved `design-SV4.md`; set Status to `approved` only if the user approved it; record the Q5 answer in the Decisions table)
- Create: `docs/superpowers/plans/2026-10-27-phase-6-9-sv4-voice-path-distributed-node.md` (this plan, checkboxes as executed; mark Task 8 run/skipped)

- [x] **Step 1:** Final server suite (generation + build + vet + test -race) — green.
- [x] **Step 2:** Commit the two docs — `docs(phase-6.9): SV4 voice path & distributed node design and plan`.
- [ ] **Step 3:** (not executed in this run: push/PR follow the review) Push and open the PR against `main`, title `Phase 6.9 SV4: server voice path & distributed node`. Body: summary; findings closed (SRV-11, SRV-12, SRV-13) plus R-1…R-5 and D7; the Decisions table (D1-D8); design §7 behaviour changes; "Opus geometry untouched"; dependency on SV2 (#PR of SV2); the Task 11 result; follow-ups (heartbeat-timeout eviction, `SendHeartbeat` acking unknown IDs, `clientNodes` cleanup, Q5 if B); the manual-verification list below; ending with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- [ ] **Step 4:** (not executed in this run) Report the PR URL and the CI result to the orchestrator. Do not merge.

---

## Manual verification (append to the client repo's `docs/superpowers/plans/2026-10-27-phase-6-9-manual-verification.md` via DOC)

Nothing below can be run in the environment that implements this plan.

1. **Advertised address:** two-node `dev/` compose deployment → the admin distribution view shows `127.0.0.1:5002` (eu) and `127.0.0.1:5003` (global), not `…:5002:5002`.
2. **Refusal:** a voice node with `publicAddr: ""` or `"0.0.0.0:5002"` fails to start; admin shows "Could not start Voice server" and the `voiceControl.publicAddr …` reason.
3. **Control down at start-up:** start the voice node first, the control node 2 min later → the node registers by itself within ~30 s; its log shows one `Control node unreachable, retrying` WARN per minute, then `Reconnected to Control Server`.
4. **Blip:** cut the control node's network for ~10 s → exactly one re-registration, the control node's heartbeat rate for that node unchanged afterwards, its coalitions unchanged (always if the record survived; also after deregistration if Q5 = A).
5. **Restart churn:** stop/start a distributed voice server from the admin UI five times → no stale node entries in the distribution view.
6. **Order under load:** two human listeners on one frequency with ≥ 10 active talkers hear in-order, unchopped audio; the server log shows no `Voice shard queue full` WARN at that load.
7. **Lagging feed:** (if reproducible) throttle a voice node's control link → one `Voice node feed fell behind` WARN, node reconnects, routing correct afterwards.
8. **C# peer interop:** blocked on `VNGD-SimpleRadioStandalone` PR #253.

---

## Execution record

Executed inline on `chore/cleanup-sv4` from `origin/main` @ `850d984`. Deviations from the steps above (each was a ruling during execution):

- Test files: the new tests live in focused files (`voiceontrol/advertise_test.go`, `backoff_test.go`, `session_test.go`, `service_stream_test.go`, `reregister_test.go`, `tombstone_test.go`, `nodefeed_test.go`; `voice/control_fake_test.go`, `listen_test.go`, `fanout_test.go`) rather than appended to `client_test.go` / `service_test.go` / `server_test.go`. `nextBackoff` is in `voiceontrol/backoff.go` and the shard code in `voice/shard.go`.
- Task 1: one extra refusal message, `voiceControl.publicAddr %q: missing host` (for `":5002"`); every refusal keeps the `voiceControl.publicAddr` prefix.
- Task 3: `io.EOF` on the control stream now restarts the session too (the old code stopped). A second `ConnectControlServer`, or one after `Close`, returns an error. Added `TestCloseIsIdempotentAndSafeBeforeConnect`.
- Task 5: `shardHook` is `func(shard int)` so one shard can be blocked.
- Task 6: the IPv6 test uses two single-coalition servers (in the plan's one-server shape, rebalance moves Blue to the v4 node).
- Tasks 7/8: the re-registration and tombstone tests use three nodes `a=[Red] b=[Gold] n=[Blue Green]`. In the plan's two-node layout the test passes on `main`, because the re-registering node is always the least loaded.
- Task 9: SV2's API is `SubscribeNames(size, names...)` returning a `*Subscription` with `C`, `Overflow()`, `Dropped()` and `Close()`. `srs.errFellBehind` is unexported, so the node feed uses its own error with the same code, `codes.ResourceExhausted`. The subscription is taken **before** the snapshot is built, which closes the gap between the snapshot and the subscription. An overflow is checked before every receive.
- Final review was a self-review (no subagent tool was available). It fixed one ordering race: `v.controlClient` is now published before the control supervisor starts. The race could not be reproduced as a failing test.
- Task 11: the client suite (Go and frontend) is green against the SV4 build. One run of `go test -run Integration -v ./internal/voice/` failed all seven tests at 0.12 s each. Its failure text was not captured, and it did not recur in 4 reruns against SV4 and 3 against pre-SV4.
