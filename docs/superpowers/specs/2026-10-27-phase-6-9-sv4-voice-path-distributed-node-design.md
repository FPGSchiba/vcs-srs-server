# Phase 6.9 SV4: Server voice path & distributed node (design)

**Status: approved** (Q1-Q4 resolved by user decision 2026-10-27; Q5 resolved as option A, the 120 s coalition tombstone, approved by the user with the plan). Implemented on branch `chore/cleanup-sv4` from `origin/main` @ `850d984` (SV2 merged, #230).
**Repo:** `vcs-srs-server` only. Designed against `origin/main` @ `dcce3ad` (#229, SV1 merged; SV3 and SV5 already in). Re-checked at pass 2 after `git fetch`: `origin/main` is still `dcce3ad`, so every file/line reference below is still current. SV2 had not merged at design time; it has since merged (#230, `850d984`), and SV4 is cut from that commit (§3, D7).
**Wave:** 3. Runs after SV2 (one item, D7, consumes SV2's bus API), alongside CL1 (client only).
**Findings:** SRV-11, SRV-12, SRV-13. All three were `not-checked` in the scan, so all are re-verified below. Re-verification also found five adjacent defects that SRV-11/SRV-12 cannot be fixed without (R-1 to R-5). SV2 handed over a sixth item (D7: the voice-node feed's silent event drop).
**Gate:** server-behaviour. Nothing here changes a `.proto`, `go.mod`/`go.sum` or CI.

**Baseline**, in a fresh detached worktree at `dcce3ad`, with the generated `srspb`/`vcsauthpb`/`voicecontrolpb`/`graphql/generated` copied in (proto and graphqls are identical to the checkout they came from). Go 1.27.3: `go build -tags headless ./...`, `go vet -tags headless ./...` and `go test -race -count=1 -tags headless ./...` are all green. The worktree has been removed.

---

## Decisions

| # | Topic | Decision | Source |
|---|---|---|---|
| D1 | SRV-13 dispatch model (Q1) | Sharded worker pool: N workers keyed by `hash(senderID) % N`, each with a bounded queue that **drops on overflow** with a rate-limited log. Per-sender order preserved; constant goroutine count; no per-ID state. Opus geometry/codec untouched: server fan-out scheduling only. | user decision, 2026-10-27 |
| D2 | SRV-12 give-up policy (Q2) | **Retry forever** with capped backoff 1 s → 30 s plus jitter and a rate-limited WARN. The node recovers by itself when the control node returns. | user decision, 2026-10-27 |
| D3 | SRV-11 `publicAddr` forms (Q3) | Accept `host:port` (the port wins) or bare `host` (port from `servers.voice.port`). Refuse empty, `0.0.0.0`/`::` and invalid ports with an admin error (the voice server fails to start). | user decision, 2026-10-27 |
| D4 | Control-side R-2 (Q4) | Fix: stream-end deregistration is ownership-checked (`deregisterNodeIf`). | user decision, 2026-10-27 |
| D5 | Control-side R-3 (Q4) | Fix: re-registration under the same node ID keeps its previous coalitions. | user decision, 2026-10-27 |
| D6 | SV1 kick handler | Kept: set once, not per connection, and survives every reconnect. | SV1 contract (§11 "SV1 → SV4"), restated by the user |
| D7 | Voice-node event feed | Switch `voiceontrol/service.go`'s `Subscribe("*")` to SV2's overflow-aware **filtered** subscription. On overflow the node feed **ends**, so the node reconnects and re-snapshots. The dead node is **deregistered** like any other ended stream; its coalitions carry over through D8's 120 s tombstone. SV4 therefore branches from (or rebases onto) `origin/main` **after SV2 merges**. | user decision in SV2's Q4, 2026-10-27; deregister-on-overflow user decision, 2026-10-28 |
| D8 | R-3 when the control node deregistered first (Q5) | **A: bounded tombstone.** A non-global node deregistered by its own stream is remembered for 120 s; a same-ID re-registration in that window gets back those of its coalitions no live node owns. See §11 for the limit found during implementation. | user decision (plan Task 8 approved), 2026-10-27 |
| W* | Worker's calls | §12. | settled here |

---

## 1. Intent

- A distributed voice node tells the control node an address that clients can actually reach.
- A voice node that loses its control stream has exactly one reconnect in flight. It leaks nothing per reconnect, retries until it succeeds, and is still registered with its coalitions after it reconnects.
- The UDP voice path stops creating a goroutine per datagram and per recipient. Frames from one sender leave the server in the order they arrived.
- A voice node's control feed is either complete or explicitly ended (and resynced); it never silently loses a client or radio delta.

The codec is not touched. The server forwards opaque Opus payloads. SV4 changes only how they are scheduled when they are fanned out (see §7, Opus geometry).

---

## 2. Re-verification (Step 1)

### SRV-13: **holds** (reproduced)

- `voice/server.go:204-206`: each datagram is copied, then `go v.handlePacket(pkt, remoteAddr)`. That is one goroutine per packet, with no bound.
- `voice/server.go:469-481`: `broadcastVoice` starts one goroutine per recipient, and each calls `packet.SerializePacket()` again (it is serialised N times).
- **Reproduction:** a throwaway test in the detached worktree, since deleted. A bound talker and a bound listener share 243.0 MHz. 2,000 frames go through `handlePacket` **sequentially** from one goroutine, so only the per-recipient goroutine can reorder them. The listener received them out of order: **352, 375 and 387 sequence inversions** across 3 runs under `-race`. One run also lost 281 frames to socket overflow.
- That test sends a burst, not the real 20 ms cadence. At the real cadence, two frames from one sender are 20 ms apart, so reordering needs scheduler stalls. It is rare, but under load it happens, and the per-packet goroutine adds a second source of it.
- The client already reorders by 24-bit sequence (`vcs-srs-client internal/voice/jitter.go:106-134`). Server-side reordering therefore costs jitter-buffer budget, plus concealment when a frame is late, rather than producing garbled audio. The C# peer's handling is unknown.

### SRV-11: **holds-with-correction**. Worse than the finding says.

- `voiceontrol/client.go:162-174`: `UdpPort: 5002` is hardcoded, `host` falls back to `"0.0.0.0"`, and `Capabilities.Version: "0.1.0"` is hardcoded.
- **Correction:** `state/settings.go:104` documents `PublicAddr` as `// public UDP host:port of this voice node`, and the shipped `dev/config/voice-eu.yaml` / `voice-global.yaml` set `publicAddr: "127.0.0.1:5002"` / `"127.0.0.1:5003"`. `registerSelf` sends that whole string as `ServerAddress`, and the control node then builds `fmt.Sprintf("%s:%d", req.ServerAddress, req.UdpPort)` (`voiceontrol/service.go:161`). The advertised address is therefore **`127.0.0.1:5002:5002`** (and `127.0.0.1:5003:5002` for the global node, whose real port is 5003), malformed for every configuration that follows the documented format. Only a bare-host `publicAddr` produces a valid address, and even then the port is hardcoded.
- The configured listen port is `Servers.Voice.Port` (`app/server_control.go:168-169`, passed to `voice.Server.Listen`).
- The real version is `app.Version = "v0.1.0"` (`app/app.go:19`). `voiceontrol` cannot import `app`, because `app` → `voice` → `voiceontrol` would be an import cycle. Nothing on the control side reads `Capabilities` today (grep: only `client.go:169`).
- The finding's `files_touched` lists `voice/server.go`. That holds: `Listen` connects the control client **before** binding UDP (`voice/server.go:134-164`), so today a node registers even if its port then fails to bind.

### SRV-12: **holds-with-correction**. Holds as stated; the correction adds defects without which a fixed client still ends up unregistered.

As stated, at `voiceontrol/client.go` on `origin/main`:
- `go v.handleReconnection()` is started from three places with no guard: the connectivity monitor on `Idle` (`:142-145`), `establishStream` on `Unavailable`/`DeadlineExceeded` (`:193-195`), and `receiveMessages` on any `Recv` error (`:257-259`). One stream failure typically triggers two of them (the `Recv` error, and the channel going `Idle` when the transport closes), so two loops each re-register and each open a stream.
- Each successful `establishStream` starts `go v.heartbeatLoop()` (`:212`), which exits only on `stopc` (`:224-226`). So heartbeats multiply by one per reconnect for the life of the process.
- Unguarded shared fields, written from the reconnect goroutines and read from others: `v.stream` (`:210` / `:253`), `v.cancelMonitor` (`:129-133`), `v.connectionFailed` (`:214`) and `v.assignedCoalitions` (`:181`, `:288`).
- `handleReconnection` sleeps with `time.Sleep` (`:408`), so it ignores `stopc` for up to 128 s. After 20 attempts (about 30 minutes of backoff) it calls `Close()` (`:400-403`) and stops for good. The node then keeps serving UDP with frozen routing state, and nothing tells the operator.

Correction (re-verification found these; each one would defeat a client-only fix):
- **R-1** `ConnectControlServer` (`:119-125`): if the first `establishConnection` fails, it **closes `v.conn` and returns `nil`**. A reconnect loop started by `establishStream` then retries on a closed `ClientConn` 20 times and gives up. A node whose control node was down at start-up never joins. The voice server reports itself running.
- **R-2** `voiceontrol/service.go:525-529` + `:692-704`: the control node's `EstablishStream` handler calls `deregisterNode(serverID)` by **ID** when its stream's context ends. If the node re-registers (`RegisterVoiceServer` overwrites `s.nodes[id]`, `:174`) and opens a new stream before the old handler's context ends, the old handler then **deletes the new registration**. After that the node's heartbeats are silently acknowledged for an unknown ID (`:109-118` returns `Acknowledged: true` either way), and `GetVoiceAddressForCoalition` stops returning it. The send-error path (`:559-561`) has the same defect.
- **R-3** `voiceontrol/service.go:204-219` + `:172`: `RegisterVoiceServer` on re-registration runs `assignCoalitions` while the node's **own old record** is still in `s.nodes`. Its own coalitions therefore count as "taken", and it is given a different free coalition. Its old ones are then handed out by `rebalance(true)` step 1 to the least-loaded node, with no `CoalitionReassigned` event. A reconnect can therefore move coalitions between nodes, under clients that are bound to the old node.
- **R-4** `voice/server.go:513-533`: `Stop()` never closes `controlClient`. Every stop/start of a distributed voice server leaks a control client, including its heartbeat loop and stream. Each start also mints a fresh `serverId` (`voice/server.go:123`), so the old registration stays live on the control node with heartbeats until the process exits.
- **R-5** `voiceontrol/client.go:448-457` / `:75-76`: `ConnectControlServer` re-creates `stopc`, but `closeOnce` is never reset, so a second `Close` on a reused client would not close the new `stopc`. Today this is only reachable through R-4's restart pattern. It is fixed for free by the restructure in §3.

### D7 (from SV2's Q4): **holds**

`voiceontrol/service.go:522` subscribes with `eventBus.Subscribe("*")`: a 10-slot buffer (`events/bus.go:86`) that also receives every `LogEntry`, with `safeSend` dropping on full (`bus.go:11-15`). A dropped `ClientsChanged`/`RadioClientsChanged`/`ServerAction` delta makes the node's routing state diverge with nothing logged. A node re-snapshots on every new stream (`service.go:517-520`; `client.go:296-326` replaces `Clients`/`RadioClients` wholesale), so ending the feed self-heals once the node reconnects (which SRV-12's fix makes reliable).

Also in that handler, read while fixing D7 (same function, kept in scope as race fixes, W11): `buildStateSnapshot` / `buildClientDelta` read `node.coalitions` without `s.mu` while `rebalance`/`tryFlushPendingMoves` (run from **every** node's handler) rewrite it under `s.mu`; and `buildClientDelta` reads `client.Coalition` from a live `ServerState` pointer after `serverState.RUnlock()` (`:653-656`).

---

## 3. Approach per finding

### SRV-13: bounded, order-preserving fan-out (D1)

- The read loop keeps: deadline, `ReadFromUDP`, `dropIfBanned`, copy. It then calls `dispatch(data, addr)`, which parses the packet (the existing `ParsePacket`; parse failures logged through the existing `parseRejects` limiter and dropped) and picks shard `shardFor(senderID, N)` = FNV-1a 32 over the 16 ID bytes, `% N`. N is fixed at `Listen`: `min(runtime.GOMAXPROCS(0), 8)`, at least 2 (W1).
- Each shard is one goroutine draining a bounded channel of 256 jobs (W1). On a full queue the job is **dropped** (non-blocking send), and the drop is logged through a new fixed-key limiter `queueFullRejects` (WARN `"Voice shard queue full, dropping packet"`, with `shard` and `suppressed_since_last`), following the file's existing rule never to key by address.
- Per-sender order is preserved because one sender always maps to the same shard, which runs FIFO.
- The goroutine count is `1 + N + cleanup`, whatever an attacker sends. Spoofed sender IDs cannot create state, because the shard index is a hash of the ID and there is no per-ID map.
- `broadcastVoice` serialises **once** and then calls `WriteToUDP` synchronously for each recipient. Concurrent `WriteToUDP` on one `*net.UDPConn` from several shards is safe.
- When the read loop exits (`stopChan`), it closes every shard queue (the read loop is the only sender) and waits for the workers before `Listen` returns, so tests cannot leak goroutines.
- `handlePacket(data, addr)` keeps its signature (the existing tests call it directly, `server_test.go:815,831,887`): it is `isRunning` check + `ParsePacket` + `handleParsedPacket(pkt, addr)`. Workers call `handleParsedPacket`.
- `handleHelloPacket` still does `go v.controlClient.ReportClientConnected(...)`, and `DisconnectClient` the same for `ReportClientDisconnected`. They stay asynchronous (gRPC calls would block a shard) but now carry a 5 s timeout context (W3). Once per session, not per frame.

Latency: a frame is now forwarded inline on its shard. Sequential `WriteToUDP` to K listeners costs roughly microseconds × K, well inside the 20 ms frame period for any realistic K. One talker's fan-out can delay another talker on the same shard by that amount; that is why N ≥ 2.

### SRV-11: advertise the real address; refuse without one (D3)

- New pure helper in `voiceontrol`: `resolveAdvertisedAddr(publicAddr string, listenPort int) (host string, port int, err error)`.
  - Trim whitespace. Empty → error `voiceControl.publicAddr is not set: a distributed voice node must advertise an address clients can reach`.
  - If the whole string parses as an IP (`net.ParseIP`, so a bare IPv6 like `2001:db8::1` is a bare host) or contains no `:` → bare host, port = `listenPort`.
  - Otherwise `net.SplitHostPort` (handles `[2001:db8::1]:5002`); the port must parse as an integer in 1-65535 and **wins** over `listenPort` (NAT/Docker mapping).
  - Refuse an empty host, an unspecified IP (`0.0.0.0`, `::`), and a port outside 1-65535 (including a bare host with `listenPort` 0 / out of range).
- `ConnectControlServer(listenPort int) error` validates this **before** dialing (and before the TLS files are loaded). A config error returns an error; network errors never do (D2). `voice.Server.Listen` returns that error; `app.startVoiceServer` already turns it into `VoiceStatus.Error` and the admin notification "Could not start Voice server" (`app/server_control.go:181-188`). The control node is never contacted.
- `Listen` binds UDP **before** connecting the control client, so a node never advertises a port it failed to bind. If the control connect returns an error, `Listen` closes the socket and returns the error. The listen port comes from the bound socket (`conn.LocalAddr().(*net.UDPAddr).Port`), so `port: 0` works in tests.
- `Capabilities.Version`: `voice.Server` gains `SetVersion(string)`; `app.startVoiceServer` calls `v.SetVersion(Version)` after `voice.NewServer`; `voiceontrol.NewVoiceControlClient` gains a `version string` parameter (W4). No consumer reads it today; it stops lying.
- Control side: `RegisterVoiceServer` formats with `net.JoinHostPort(req.ServerAddress, strconv.Itoa(int(req.UdpPort)))` so an IPv6 host formats as `[2001:db8::1]:5002` (W5). The wire is unchanged: `ServerAddress` and `UdpPort` are existing fields. A **pre-SV4 node** still sends `host:port` in `ServerAddress` and gets an equally malformed address (`[127.0.0.1:5002]:5002` instead of `127.0.0.1:5002:5002`); mixed versions are no worse than today.

### SRV-12: one supervisor goroutine per control-client lifetime (D2, D6)

Restructure `VoiceControlClient` around one owning goroutine:

```
ConnectControlServer(listenPort):  resolveAdvertisedAddr (SRV-11) → load TLS → grpc.NewClient (lazy, no network)
                                   → start run(ctx) once → return nil.  (Config errors return an error; network errors never do.)
run(ctx):  for attempt := 0; ; {
              started := now
              err := v.session(ctx)   // sessCtx := WithCancel(ctx); registerSelf(sessCtx, timeout) → EstablishStream(sessCtx)
                                      // → send init → heartbeat goroutine(sessCtx) → recv loop inline → returns on Recv error;
                                      // cancel(sessCtx) and wait for the heartbeat goroutine before returning
              if ctx.Err() != nil { return }
              if session lasted ≥ backoffResetAfter { attempt = 0 }
              warn (rate-limited, D2) with attempt count and err
              wait nextBackoff(attempt) via select on ctx.Done() / timer (never time.Sleep); attempt++
           }
Close():   cancel ctx (sync.Once), wait for run to exit, conn.Close(). Idempotent; safe before ConnectControlServer.
```

- **Single flight by construction.** Only `run` reconnects. The connectivity-state monitor (`:135-148`) is deleted: grpc's `ClientConn` redials the transport by itself, and the stream `Recv` error is the only signal that the *session* needs redoing. `handleReconnection`, `establishConnection`, `stopc`, `closeOnce` (the old one), `cancelMonitor`, `stream` and `connectionFailed` fields go away.
- **Backoff (D2):** `nextBackoff(attempt)` = `min(1s · 2^attempt, 30s)`, then jitter **downwards** to `[0.8·d, d]`, so it never exceeds 30 s (W2). No attempt cap. `backoffResetAfter` = 30 s (W2).
- **Rate-limited WARN (D2):** each failed session or failed attempt logs WARN `"Control node unreachable, retrying"` with `attempt`, `retry_in`, `error`, at most once per 60 s, carrying `suppressed_since_last` (W2). A session that registers after ≥1 failure logs INFO `"Reconnected to Control Server"` with the attempt count.
- **Per-connection context.** Heartbeat and receive both run under `sessCtx`. `RegisterVoiceServer`/`SendHeartbeat` use `sessCtx`-derived contexts with a 10 s timeout (W3).
- **Mutex-guarded shared fields.** `assignedCoalitions` sits behind `mu` with `AssignedCoalitions() []string` (a copy). `client` and `conn` are set once in `ConnectControlServer` before `run` starts and never reassigned.
- **SV1 kick handler (D6):** `kickMu`/`kickHandler`/`SetKickHandler`/`kick` stay exactly as on `main`: set once before `ConnectControlServer`, never touched by `session`, so they survive every reconnect. `applyControlMessage` is unchanged except that `AssignCoalitions` writes under `mu`; it now runs on the supervisor goroutine.
- **R-1** is fixed by the shape: the first session is attempted inside `run`; a failure is retried like any other, on the still-open `ClientConn`.
- **R-5** is fixed by the shape: one client, one `run`, one `Once`.
- **R-4:** `voice.Server.Stop()` takes the `controlClient` pointer under `v.Lock()`, unlocks, then calls `Close()` (Close waits for `run`, and `run` may be inside the kick handler, which takes `v.Lock` via `DisconnectClient`). `Listen` also closes it when its read loop exits on `stopChan` (`app.stopVoiceServer` closes `stopChan` before `Stop()`). Close is idempotent. Closing cancels the stream, so the control node's handler sees its context end and deregisters the node (R-2's ownership check passes: it is the current record).
- **Snapshot on reconnect:** unchanged; each new stream gets a full `StateSnapshot`.
- **Test seams (W6):** unexported fields on `VoiceControlClient`: `dialTarget string` + `dialOpts []grpc.DialOption` (when set, they replace the address and the TLS credentials, so tests use `bufconn` with insecure credentials and production keeps the TLS path), `heartbeatInterval` (15 s), `backoffMin`/`backoffMax` (1 s / 30 s), `rpcTimeout` (10 s), `warnEvery` (60 s), `jitter func(time.Duration) time.Duration`. One exported setter, `UseDialer(target string, opts ...grpc.DialOption)`, lets `voice` package tests point a node at a bufconn fake.

### Control side (`voiceontrol/service.go`)

- **R-2 (D4):** `EstablishStream` already holds the `*registeredNode` it attached to. Stream-end and send-error deregistration become `deregisterNodeIf(serverID string, node *registeredNode)`, deleting only if `s.nodes[serverID] == node` (and clearing `globalNodeAddr` only then). Mirrors SV1's `removeClientIf`.
- **R-3 (D5):** in `RegisterVoiceServer`, if `s.nodes[req.ServerId]` exists, is non-global and the request is non-global, the new record takes a **copy** of the old record's coalitions, the old record is not counted as "taken" (it is replaced, not consulted), `assignCoalitions` is not run, and `rebalance(true)` still runs (it is a no-op for a node at or under target). `pendingMoves` naming it stay valid (same ID). This applies only while the old record is still present; the case where the control node deregistered first is covered by D8's 120 s tombstone (Q5 resolved as A).
- **D7 (from SV2):** `EstablishStream` replaces `Subscribe("*")` with SV2's filtered, overflow-aware subscription for exactly `ClientsChanged`, `RadioClientsChanged`, `ServerAction`, `CoalitionsChanged` (logs, notifications, admin and voice-node events no longer occupy the buffer), size 256 (W7). On overflow the handler: deregisters the node through the ownership-checked `deregisterNodeIf` (R-2), exactly as a dead stream does, so the D8 tombstone reserves its coalitions for 120 s and the node's same-ID re-registration in that window gets them back (user decision 2026-10-28, replacing the earlier detach-only design, which kept a dead node registered with nothing to evict it), logs one WARN `"Voice node feed fell behind, ending stream"` with `serverId` and the drop count, and returns a status error with the same code SV2 merged for its slow-subscriber status (`codes.ResourceExhausted` if SV2's recommendation stood; the node does not branch on the code — every `Recv` error restarts the session). The exact SV2 API names are read from the merged `events/bus.go` (design-SV2 §3.1 shape: `SubscribeNames(size, names...) *Subscription` with `C`, `Overflow()`, `Dropped()`, removed via `Unsubscribe`).
- **Race fixes in the same handler (W11):** `buildStateSnapshot`/`buildClientDelta` take a copy of `node.coalitions` (and `node.isGlobal`) under `s.mu.RLock()` instead of reading the live slice; `buildClientDelta` copies `client.Coalition` before `serverState.RUnlock()`. No behaviour change.

---

## 4. Files

| File | Change |
|---|---|
| `voice/server.go` | `dispatch` + shard workers + `shardFor`; `handleParsedPacket`; `broadcastVoice` serialises once, sends inline; `queueFullRejects`; `Listen` binds before connecting control and passes the bound port; `Stop`/`Listen`-exit close the control client; `SetVersion` |
| `voice/server_test.go` | tests below |
| `voiceontrol/client.go` | supervisor restructure; `resolveAdvertisedAddr`; `nextBackoff`; registration with real host/port/version; seams; `AssignedCoalitions()`; Report* timeouts |
| `voiceontrol/client_test.go` | tests below (bufconn fake `VoiceControlService`); `newTestClient` updated for removed fields |
| `voiceontrol/service.go` | `net.JoinHostPort`; `deregisterNodeIf`; same-ID coalition carry-over; SV2 filtered feed + overflow end; coalition-read race fixes |
| `voiceontrol/service_test.go` | tests below |
| `app/server_control.go` | one line: `v.SetVersion(Version)` |
| `example.config.yaml` | comment on `publicAddr`: "required in voice distribution mode; `host:port` or bare `host` (port from `servers.voice.port`); `0.0.0.0` is refused" |

The umbrella lists four files. `app/server_control.go` and the config example are one-liners that follow from SRV-11. Not touched: any `.proto`, `go.mod`/`go.sum`, `.github/workflows/*`, `events/*` (SV2's), `state/*`, `srs/*`, `control/*`, `dev/config/*` (already `host:port`).

---

## 5. Tests first (each one red on `origin/main` unless marked *(guard)*)

**voice (SRV-13, R-4, SRV-11 ordering)**
1. `TestVoiceFanOutPreservesPerSenderOrder`: bound talker + listener on one frequency; 2,000 frames through `dispatch`; zero sequence inversions (counted on frames received). On `main` (through `handlePacket`) about 350.
2. `TestVoiceDispatchGoroutinesBounded`: 10,000 datagrams from 10,000 random sender IDs with the workers blocked via a test hook; `runtime.NumGoroutine()` ≤ baseline + N + 4; overflow dropped.
3. `TestShardForStableAndInRange` *(guard)*.
4. `TestVoiceQueueFullDropIsRateLimited`: one WARN per window through `queueFullRejects`, independent of `parseRejects`.
5. `TestBroadcastSendsIdenticalBytes` *(guard)*: three listeners receive byte-identical frames (serialise-once is checked in review; there is no cheap counter).
6. `TestListenWaitsForWorkersOnStop` *(guard)*.
7. `TestStopClosesControlClient` (R-4).
8. `TestListenBindsBeforeRegistering`: port taken → bind error, fake control saw no `RegisterVoiceServer`.
9. `TestListenRefusesBadPublicAddr`: distributed mode, `publicAddr` `0.0.0.0:5002` → `Listen` returns an error and the UDP socket is closed (port re-bindable).

**voiceontrol client (SRV-11, SRV-12, D2, D6)**
10. `TestResolveAdvertisedAddr` (table, D3): `"127.0.0.1:5002"`,5002 → (127.0.0.1,5002); `"vcs.example:6000"`,5002 → 6000; `"203.0.113.5"`,5010 → 5010; `" 203.0.113.5 "`,5010 → 5010; `"[2001:db8::1]:5002"` → (2001:db8::1,5002); `"2001:db8::1"`,5010 → 5010; errors: `""`, `"  "`, `"0.0.0.0:5002"`, `"0.0.0.0"`, `"[::]:5002"`, `"::"`, `"host:0"`, `"host:70000"`, `"host:abc"`, `":5002"`, `"host"` with listen port 0.
11. `TestRegisterAdvertisesConfiguredAddress`: fake sees `ServerAddress=127.0.0.1`, `UdpPort=<listen port>`, `Version=<passed>`.
12. `TestConnectRefusesWithoutPublicAddr`: error, fake sees nothing.
13. `TestReconnectIsSingleFlight`: 5 stream breaks → exactly 6 `RegisterVoiceServer` and 6 `EstablishStream`.
14. `TestHeartbeatGoroutinesDoNotAccumulate`.
15. `TestReconnectKeepsKickHandler` (D6).
16. `TestFirstConnectFailureIsRetried` (R-1).
17. `TestCloseStopsReconnectPromptly`: `Close()` during a 30 s backoff returns within 200 ms; no further RPCs.
18. `TestNextBackoffCappedWithJitter` (D2): 1 s, 2 s, 4 s … capped at 30 s, jitter within `[0.8d, d]`, attempt 100 does not overflow.
19. `TestRetriesForeverAndWarnsRateLimited` (D2): 50 failed attempts → still retrying, exactly 1 WARN in the window with `suppressed_since_last` on the next.
20. `-race` covers the guarded `assignedCoalitions` (`TestAssignCoalitionsRace`).

**voiceontrol service (R-2, R-3, D7)**
21. `TestStaleStreamEndDoesNotDeregisterNewRegistration` (R-2).
22. `TestStaleSendErrorDoesNotDeregisterNewRegistration` (R-2, send path).
23. `TestReRegistrationKeepsCoalitions` (R-3).
24. `TestRegisterFormatsIPv6Address` (W5).
25. `TestNodeFeedIgnoresLogFlood` (D7): 500 `LogEntry` publishes with `Send` stalled do not end the feed.
26. `TestNodeFeedOverflowDeregistersNodeCoalitionsReturnWithinGrace` (D7, D8): stalled `Send` + > 256 relevant events → handler returns the overflow status; one WARN; `s.nodes[id]` is gone and `recentlyGone[id]` holds its coalitions; a same-ID re-registration 119 s later gets them back.
27. `TestBuildClientDeltaCoalitionRace` (W11, `-race`): rebalance concurrent with delta building.

Seams: §3 W6. No new dependency: `bufconn` is already used (`control/session_e2e_test.go:39`); no `goleak`; goroutine checks poll `runtime.NumGoroutine` to a deadline.

Verification: `go build -tags headless ./...`, `go vet -tags headless ./...`, `go test -race -count=1 -tags headless ./...`, plus `-count=20` on the reconnect, ordering and feed tests.

---

## 6. Out of scope

- Client-side distributed-mode work (Phase 9): consuming coalition/global voice addresses, reacting to node moves.
- Codec, Opus geometry, packet format, `BufferSize`, the client jitter buffer.
- Deregistered-node clean-up on the control side: `clientNodes` entries, coalition hand-off when a node dies for good, heartbeat-timeout eviction. `SendHeartbeat` acknowledging unknown IDs (`service.go:109-118`) is noted but not changed. Recorded as follow-ups for the ROADMAP's distributed-mode phase.
- SV2's own bus/stream/roster work (SV4 only consumes SV2's subscription API).
- Making `dropIfBanned` cheaper (a linear scan under `serverState.RLock` per datagram; noted only).
- Proto changes, including `ServerCapabilities` semantics.

---

## 7. User / wire / operator-visible changes

| Change | Who sees it |
|---|---|
| Voice frames from one sender leave the server in arrival order. Under overload a shard **drops** frames (rate-limited WARN) instead of spawning goroutines (D1). | Every voice client (VCS and the C# peer); less jitter-buffer concealment |
| A distributed voice node advertises `publicAddr`'s host and the real UDP port, not `host:5002:5002` / `0.0.0.0:5002` / a hardcoded `5002` (D3). | Clients via the voice address in `SyncClient` (Phase 9 client); operators in the admin distribution view |
| A distributed voice node with no usable `publicAddr` **fails to start** with `VoiceStatus.Error` and the admin notification (D3). | Operators |
| Registration reports the real server version instead of `"0.1.0"`. | Nobody reads it today |
| Reconnect: one loop; no heartbeat multiplication; retries if the control node was down at start-up (R-1); **retries forever**, WARN at most once a minute (D2). | Operators (logs); control-side heartbeat rate |
| Control node: a blipping node is not deregistered by its own old stream (D4) and keeps its coalitions, whether its record is still present (D5) or was deregistered within the last 120 s (D8). | Clients on that node; operators |
| Control node: a node whose feed falls behind gets its stream ended and resyncs, instead of silently diverging (D7). | Operators (one WARN); voice routing correctness |
| Stopping a distributed voice server ends its control registration (R-4). | Operators (distribution view) |

**Opus geometry (CLAUDE.md):** not touched. The server never decodes; it forwards `packet.Payload` bytes unchanged at the sender's cadence (20 ms). Only fan-out scheduling changes (inline per shard instead of a goroutine per recipient; header serialised once). No frame is re-timed, merged or split.

**C# peer:** it shares this server, so it also gets in-order delivery and the overload-drop policy. Interop is blocked on PR #253; it goes on the checklist.

---

## 8. Cross-track contracts

- **SV1 → SV4 (kept, D6):** `SetKickHandler` stays a field set once before `ConnectControlServer`, under `kickMu`; no reconnect path clears or re-sets it (test 15). `applyControlMessage`'s `KickClient` and `LEFT` handling is unchanged and still called outside the state lock, now on the supervisor goroutine. `DropVoiceSession` (`service.go:734-765`) is unchanged; with R-2/D7 it may briefly target a node whose stream is being replaced or detached (a nil stream is skipped; a send error is logged only, as SV1 designed, and the next stream's snapshot lacks the removed client).
- **SV2 → SV4 (D7):** SV4 consumes SV2's filtered overflow-aware subscription and SV2's slow-subscriber status code. SV4 does not edit `events/*`. Ordering: **SV4's branch is cut from (or rebased onto) `origin/main` after SV2's PR merges.** Tasks that do not touch the feed may be implemented earlier on a branch from `dcce3ad` only if the orchestrator says so; the feed task then waits for the rebase. SV2's publish-waits-for-state-events change is safe with `rebalance` publishing under `s.mu` (SV2 design §3.2).
- **Lock order:** voice node: the kick handler (`DisconnectClient`) takes `v.Lock`; `Stop()` never holds `v.Lock` while waiting on the control client. Control side: `s.mu` then `serverState`, as SV1 established. `deregisterNodeIf` and the overflow detach take only `s.mu`.
- **CL1:** none. Only packet ordering changes on the client wire; the client's jitter buffer tolerates either.
- **UP (Wave 4):** touches `go.mod` and workflows, none of SV4's files.
- **DOC / manual checklist:** §13 below.

---

## 9. Approval-gate items

- **server-behaviour:** the overload drop policy (D1, decided); refusing to start without a usable `publicAddr` (D3, decided); retry-forever (D2, decided); control-side re-registration semantics (D4/D5, decided); ending a lagging node feed (D7, decided in SV2 Q4); Q5 (resolved as A, D8). Each is still marked GATE in the plan so the user approves the task before it runs.
- **cross-repo / cross-track:** the dependency on SV2's merged API (D7).
- No dependency, proto, CI or file-deletion changes. `bufconn` and `hash/fnv` are already available.

---

## 10. Risks

- **S1 Shard head-of-line blocking.** A slow `WriteToUDP` blocks its shard. Mitigations: N ≥ 2; the bounded queue drops rather than blocking the read loop; frames are small. A per-send deadline would need `SetWriteDeadline` on the shared conn (affects every shard), so it is not done.
- **S2 HELLO on the shard.** Constant-time compare plus a map write; its gRPC report stays asynchronous with a timeout.
- **S3 Reconnect test flakiness.** All timing through injected intervals/backoff, polling to a deadline, `-count=20` before the PR.
- **S4 D5 only covers a live record.** If the control node sees the old stream end and deregisters before the node re-registers (likely when the transport itself died, since both ends notice at similar times and the node waits ≥0.8 s of backoff), the node is new to the control node and goes through `assignCoalitions` + `rebalance` as today. Closed by D8 (Q5 = A): a same-ID re-registration within 120 s gets back the coalitions no live node owns (limit in §11).
- **S5 Go stdlib UDP concurrency.** Concurrent `WriteToUDP` on one `UDPConn` is safe; today's code already relies on it.
- **S6 SV2 slips.** D7 cannot land before SV2. If SV2 is delayed, SV4 can merge without the feed task only with the user's approval (it would leave the lossy feed in place).
- **S7 Retry forever is quiet by design.** An operator relying on the old "gives up after ~30 min" ERROR now sees a WARN per minute instead. Accepted by D2.
- **S8 No field verification.** Nothing here can be heard in this environment; reorder evidence is loopback + burst.

---

## 11. Open questions

**Q1-Q4: resolved** (see Decisions D1-D5).

**Q5 (resolved: A).** Should D5's coalition carry-over also cover a node that the control node already deregistered?

*Implementation note:* option A's "the coalitions stay unowned meanwhile" holds only while nothing else happens. Any intervening registration (its `rebalance(true)` step 1) or a `CoalitionsChanged` (`rebalance(false)`) hands unowned coalitions to the least-loaded node, after which the tombstone cannot reclaim them. Changing that would change `rebalance`, which is out of scope; recorded as a distributed-mode follow-up.

Why it matters: when a node's transport dies, the control node's stream context usually ends at about the same time as the node's `Recv` fails, and the node then waits ≥0.8 s before re-registering. So in the most common blip the old record is **already gone** when the node returns, D5 does not apply, and the node's coalitions can still move (`assignCoalitions` gives it one free coalition; `rebalance(true)` hands the rest to the least-loaded node). D5 as decided protects only fast re-registrations (the overflow case, D7, now deregisters and relies on D8 as well).
- **A (recommended): a bounded tombstone.** On ownership-checked deregistration of a non-global node, remember its coalitions under its server ID for 120 s (the SV1 client grace). A same-ID re-registration within that time gets back those coalitions that no other node owns at that moment. Bounded by the number of nodes (IDs come from the mTLS-authenticated control channel), expired lazily on the next registration. The coalitions stay unowned meanwhile, exactly as they are today after a deregistration.
- B: D5 literally (live record only); record the gap as a distributed-mode follow-up.
- C: stop deleting nodes on stream end at all (detach only). Needs heartbeat-timeout eviction, which is out of scope, so a dead node would keep its coalitions forever.

## 12. Worker's calls (settled here, not asked)

- W1 shard count `min(GOMAXPROCS, 8)`, ≥ 2; queue depth 256; FNV-1a 32 over the ID bytes.
- W2 backoff `min(1s·2^n, 30s)` jittered down to `[0.8d, d]`; reset after a session lasting ≥ 30 s; WARN at most once per 60 s with `suppressed_since_last`.
- W3 RPC timeouts: 10 s for register and heartbeat; 5 s for the Report* calls.
- W4 version threading via `voice.Server.SetVersion` + a `version` parameter on `NewVoiceControlClient`.
- W5 `net.JoinHostPort` on the control side.
- W6 test seams in `voiceontrol` (dial target/options, interval, backoff, timeout, warn window, jitter).
- W7 node-feed subscription size 256, filtered to the four forwarded/acted-on event names.
- W8 `Listen` binds UDP before the control connect and passes the bound port.
- W9 delete the connectivity-state monitor (redundant with the `Recv` error).
- W10 keep `handlePacket`'s signature for the existing tests; goroutine checks with `runtime.NumGoroutine`, no `goleak`.
- W11 copy `node.coalitions`/`client.Coalition` under their locks in `buildStateSnapshot`/`buildClientDelta` (race fixes in the function D7 already edits).

## 13. Manual verification

Nothing below can be run in the environment that implements this.

1. Two-node distributed deployment (`dev/` compose): the admin distribution view shows each node at `127.0.0.1:5002` / `127.0.0.1:5003`, not `…:5002:5002`.
2. A voice node with `publicAddr: ""` or `"0.0.0.0"` fails to start, with the admin notification "Could not start Voice server" and the reason in `VoiceStatus.Error`.
3. Start a voice node while the control node is down; start the control node 2 min later → the voice node registers by itself within ~30 s, with one WARN a minute in between.
4. Kill and restore the control node's network for ~10 s → the voice node re-registers **once** (one "Reconnected" INFO), heartbeat rate on the control node unchanged, its coalitions unchanged (and per Q5 if A).
5. Stop and start a distributed voice server from the admin UI five times → the distribution view never shows stale node entries.
6. Two human listeners on one frequency with a busy server (≥ 10 talkers) hear in-order, unchopped audio.
7. C# peer interop (blocked on `VNGD-SimpleRadioStandalone` PR #253).
