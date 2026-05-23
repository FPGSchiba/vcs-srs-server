# Distribution System Design

**Date:** 2026-05-23  
**Status:** Approved  
**Scope:** Voice node distribution, coalition-based sharding, global frequency node, state sync, deployment

---

## Overview

The VCS server supports three distribution modes today: Standalone, Control, and Voice. The core gap is that voice nodes in distributed mode have no client state — they cannot route audio because they do not know who is registered or on what frequency.

This spec covers the changes needed to make distributed voice fully operational for ~200 concurrent global users.

**Out of scope (deferred):** Messages, Briefings, Ship health data channels.

---

## Node Topology

| Node | Count | Exposes | Connects to | Owns |
|---|---|---|---|---|
| **Control** | 1 (central) | `:5001` gRPC clients · `:14448` gRPC voice nodes · `:8080` HTTP | — | Auth, client state, radio state, coalition list, global freq list, assignment registry |
| **Coalition Voice** | 1–N regional | `:5002` UDP only | Control `:14448` (outbound gRPC) | UDP audio for assigned coalitions/freq bands. Local mirror of server state. |
| **Global Voice** | 1 dedicated | `:5002` UDP only | Control `:14448` (outbound gRPC) | UDP audio for all global frequencies. Same binary as coalition node, started with `--global` flag. |

### Communication boundaries

```
Client ──gRPC :5001──────────────► Control
Client ──UDP  :5002──────────────► Coalition Voice Node
Client ──UDP  :5002──────────────► Global Voice Node

Coalition Voice Node ──gRPC :14448 (outbound)──► Control
Global Voice Node    ──gRPC :14448 (outbound)──► Control
```

Voice nodes are **gRPC clients only** — they dial outward to Control. They never accept connections from end-users. End-users only reach voice nodes via raw UDP for audio.

---

## Assignment Rules

### Rule 1 — Coalition sharding (primary)

Each coalition voice node is assigned one or more coalitions at registration time. All UDP audio for those coalitions is self-contained on that node. No cross-node audio forwarding is needed.

Control assigns coalitions round-robin across registered coalition nodes, using the `region` hint to prefer geographic colocation where possible.

### Rule 2 — Frequency band overflow (when nodes > coalitions)

When more voice nodes are registered than there are coalitions to assign, excess nodes receive a sub-range of a coalition's frequency space. Control tracks which frequency band lives on which node and routes clients accordingly.

### Rule 3 — Global node

The global voice node registers with `is_global = true`. It receives all global frequencies from the Control's settings state. All clients connect to the global node regardless of coalition for global-frequency transmissions.

---

## Protocol Changes

### `voicecontrolpb` — Registration

```proto
message RegisterVoiceServerRequest {
  string             server_id            = 1;
  ServerCapabilities capabilities         = 2;
  string             server_address       = 3; // public IP/hostname for UDP
  uint32             udp_port             = 4;
  bool               is_global            = 5; // NEW
  string             region               = 6; // NEW — "eu" / "us" / "apac"
  repeated string    preferred_coalitions = 7; // NEW — empty = auto-assign
}

message RegisterVoiceServerResponse {
  bool                    success              = 1;
  string                  message              = 2;
  repeated string         assigned_coalitions  = 3; // NEW
  repeated FrequencyRange assigned_freq_ranges = 4; // used for overflow case
  string                  global_voice_address = 5; // NEW — returned to all nodes
}
```

### `voicecontrolpb` — State sync stream

The existing `EstablishStream` bidi stream carries `ControlMessage` (Control → Voice) and `ControlResponse` (Voice → Control). Currently both are empty stubs.

```proto
message ControlMessage {
  oneof payload {
    ClientStateSnapshot state_snapshot = 1; // full dump sent immediately on connect
    ClientDelta         client_delta   = 2; // incremental updates
    ServerActionForward server_action  = 3; // kick/ban/mute forwarded to owning node
  }
}

message ClientStateSnapshot {
  map<string, ClientInfo> clients = 1;
  map<string, RadioInfo>  radios  = 2;
}

message ClientDelta {
  enum Type {
    JOINED       = 0;
    LEFT         = 1;
    INFO_UPDATED = 2; // name, coalition, unit ID, role changed
    RADIO_UPDATED = 3;
    MUTED        = 4;
    UNMUTED      = 5;
  }
  Type       type        = 1;
  string     client_id   = 2;
  ClientInfo client_info = 3; // populated for JOINED, INFO_UPDATED
  RadioInfo  radio_info  = 4; // populated for RADIO_UPDATED
}

message ServerActionForward {
  enum ActionType { KICK = 0; BAN = 1; MUTE = 2; UNMUTE = 3; }
  ActionType action_type      = 1;
  string     target_client_id = 2;
  string     reason           = 3;
}

// ControlResponse (Voice → Control) is reserved for future use (heartbeat/ack).
// Voice nodes send nothing back to Control in this version.
```

**State sync flow:**

1. Voice node dials Control `:14448`, calls `RegisterVoiceServer` → receives coalition assignment and global node address.
2. Voice node calls `EstablishStream`. Control immediately sends a `ClientStateSnapshot` scoped to the node's assigned coalitions (global node receives all clients).
3. Voice node populates its local `ServerState` from the snapshot — it can now route audio correctly.
4. For every subsequent client event (join, leave, radio update, mute), Control's event bus pipes a `ClientDelta` down the stream in real time.

### `srspb` — Client login response

```proto
message ServerSyncResult {
  map<string, ClientInfo> clients              = 1;
  map<string, RadioInfo>  radios               = 2;
  ServerSettings          settings             = 3;
  string                  coalition_voice_addr = 4; // NEW — UDP address for coalition audio
  string                  global_voice_addr    = 5; // NEW — UDP address, same for all clients
}
```

After login, clients open two UDP sockets: one to `coalition_voice_addr` for coalition-frequency audio, one to `global_voice_addr` for global-frequency audio.

---

## Distribution State Changes

`state/distribution.go` gains an `IsGlobal` flag:

```go
type DistributionState struct {
    sync.RWMutex
    DistributionMode uint8
    RuntimeMode      uint8
    IsGlobal         bool  // true when this node is the global voice node
}
```

`headless.go` gains a `--global` flag that sets `IsGlobal = true` when `DistributionMode == DistributionModeVoice`.

---

## Voice Node Startup — Cleanup

The current `StartVoiceServer()` path in `app/app.go` only starts the UDP server, which is correct. However the `control/server.go` code path that initialises the client-facing gRPC server must never be reached from a voice-only node. This is already the case but should be made explicit with a guard.

---

## Deployment

### Central region (`docker-compose.central.yml`)

```yaml
services:
  control:
    image: vcs-server:latest
    command: headless --mode control
    ports:
      - "5001:5001"
      - "8080:8080"
      - "14448:14448"

  voice-eu:
    image: vcs-server:latest
    command: headless --mode voice
    ports:
      - "5002:5002/udp"
    environment:
      VCS_CTRL_HOST: control
      VCS_REGION: eu
      VCS_PUBLIC_ADDR: <eu-public-ip>

  voice-global:
    image: vcs-server:latest
    command: headless --mode voice --global
    ports:
      - "5003:5002/udp"
    environment:
      VCS_CTRL_HOST: control
      VCS_PUBLIC_ADDR: <eu-public-ip>:5003
```

### Regional nodes (`docker-compose.region.yml`)

```yaml
services:
  voice-us:           # repeat for apac, changing region + addr
    image: vcs-server:latest
    command: headless --mode voice
    ports:
      - "5002:5002/udp"
    environment:
      VCS_CTRL_HOST: <central-public-ip>
      VCS_CTRL_PORT: 14448
      VCS_REGION: us
      VCS_PUBLIC_ADDR: <us-public-ip>
```

### Firewall rules

| Port | Open to |
|---|---|
| Control `:5001` | Public (clients) |
| Control `:8080` | Public (HTTP/admin) |
| Control `:14448` | Voice node IPs only (not public) |
| Voice `:5002` UDP | Public (all client IPs) |

---

## Work Items

| # | Item | File(s) | Type |
|---|---|---|---|
| 1 | Add `is_global`, `region`, `preferred_coalitions` to registration request proto | `voicecontrolpb/*.proto` | proto |
| 2 | Add `assigned_coalitions`, `global_voice_address` to registration response proto | `voicecontrolpb/*.proto` | proto |
| 3 | Add `ControlMessage` oneof: snapshot, delta, action | `voicecontrolpb/*.proto` | proto |
| 4 | Add `coalition_voice_addr` + `global_voice_addr` to `ServerSyncResult` | `srspb/*.proto` | proto |
| 5 | Inject `eventBus` into `VoiceControlServer` (needed for delta streaming) | `voiceontrol/service.go`, `control/server.go` | new dep |
| 6 | Implement coalition/freq assignment in `RegisterVoiceServer` | `voiceontrol/service.go` | stub → real |
| 7 | Implement `EstablishStream`: send snapshot + pipe event bus as deltas | `voiceontrol/service.go` | stub → real |
| 8 | Voice node client: apply incoming `ControlMessage` to local `ServerState` | `voiceontrol/client.go` | stub → real |
| 9 | Return voice addresses in `SyncClient` response | `srs/srs_service.go` | extend |
| 10 | Add `--global` flag + `IsGlobal` to distribution state and headless startup | `headless.go`, `state/distribution.go` | new |
| 11 | Verify voice node startup path never starts client gRPC server | `app/app.go`, `control/server.go` | cleanup |
| 12 | Docker Compose files (central + regional) | `deploy/` | new |
