# Voice Path Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Authenticate the UDP voice path so that observing a voice packet no longer lets an attacker hijack, impersonate, or disconnect a session.

**Architecture:** A per-session secret is generated when a client enters `ServerState`, delivered to the client over the authenticated TLS gRPC control path, and presented in the HELLO packet payload where it is validated with a constant-time comparison. HELLO becomes the only packet type that may establish or change a session's UDP address binding; every other packet type must arrive from the address already bound.

**Tech Stack:** Go 1.25, gRPC/protobuf (protoc + protoc-gen-go), `crypto/rand`, `crypto/subtle`, standard `testing`.

**Spec:** `docs/superpowers/specs/2026-09-24-voice-path-authentication-design.md`

## Global Constraints

- **Go toolchain invocation:** `GOROOT` is pinned in the local shell to a module-cache toolchain and breaks std resolution. Every Go command in this plan MUST run as `env -u GOROOT GOCACHE="$TMPDIR/go-build" go ...`. Create `$TMPDIR/go-build` once with `mkdir -p "$TMPDIR/go-build"`.
- **Protocol version stays `1`.** `currentVersion` in `voice/protocol.go` is not incremented. No client has shipped, so the secret is mandatory with no optional or legacy path.
- **Secret size:** exactly 32 bytes from `crypto/rand`, encoded with `base64.RawURLEncoding`, producing exactly 43 characters. `VoiceSecretBytes = 32` (package `state`), `VoiceSecretLen = 43` (package `voice`).
- **Comparison:** secret comparison uses `crypto/subtle.ConstantTimeCompare`, never `==`.
- **On any HELLO validation failure:** no address rebind, no ACK, no `ReportClientConnected`, no state mutation of any kind.
- **Address comparison:** `IP.Equal` plus port equality. Never `addr.String()`.
- **Test command (repo-wide):** `env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./...` — this is what CI runs.
- **Commit trailer:** every commit ends with `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.
- **Do not touch `~/Projects/vanguard/vcs-srs-client`.** The client side is a separate session.

## Review Focus

Input classes the spec implies that no single task's happy path exercises. Each has a test assigned to the task that owns the code.

1. **A `ClientState` created without `AddClient` has an empty `VoiceSecret`** — distributed voice nodes build state from control-stream deltas, and an older control server sends none. An empty stored secret must match nothing, not match an empty presented secret. → Task 7, `TestHelloEmptyStoredSecretRejected`.
2. **A HELLO payload longer than `VoiceSecretLen`** — bytes past offset 43 are reserved for future use and must be ignored, not rejected, or the protocol cannot be extended. → Task 3, `TestHelloSecretIgnoresTrailingBytes`.
3. **IPv4-mapped-IPv6 address representations** — the same peer can present as `127.0.0.1` or `::ffff:127.0.0.1` depending on socket family, and a string comparison would spuriously reject a legitimate client. → Task 8, `TestIsBoundAddrMatchesIPv4MappedIPv6`.
4. **A secret containing `-` and `_`** — `base64.RawURLEncoding` emits both, and they must survive the proto string field and the UDP payload round-trip unaltered. → Task 3, `TestHelloSecretRoundTripURLSafeChars`.
5. **Concurrent HELLO and cleanup on the same session** — `cleanup` deletes entries on a ticker while handler goroutines run per packet; the new lookup path must not race. → Task 7, `TestHelloRaceWithCleanup`, run under `-race`.

---

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `services/*.go` (5 files) | Modify | Add `//go:build !headless`; GUI-only package |
| `state/server.go` | Modify | `ClientState.VoiceSecret`, secret generation in `AddClient`, `GetVoiceSecret` |
| `state/state_test.go` | Modify | Secret generation tests |
| `voice/protocol.go` | Modify | `VoiceSecretLen`, `NewVCSHelloPacket`, `HelloSecret` |
| `voice/protocol_test.go` | Create | Packet round-trip and payload parsing tests |
| `srs.proto` | Modify | `voice_secret` on the two live messages; annotate the three dead ones |
| `control.proto` | Modify | `VoiceClientInfo.voice_secret` |
| `srs/srs_service.go` | Modify | Populate `voice_secret` on `SyncClient` and `VoiceAddressUpdate` |
| `srs/srs_service_test.go` | Modify | Delivery tests |
| `voiceontrol/service.go` | Modify | Emit secret in snapshot and delta |
| `voiceontrol/client.go` | Modify | Apply secret from snapshot and delta |
| `voiceontrol/client_test.go` | Modify | Propagation tests |
| `voice/server.go` | Modify | HELLO validation, rate-limited rejection logging, address binding checks |
| `voice/server_test.go` | Modify | Authentication and binding tests |
| `README.md` | Modify | Protocol documentation and trust model |

---

### Task 1: Tag the services package as GUI-only

Unrelated to voice authentication, but `go test -tags headless ./...` does not compile today, so nothing below can be verified until this lands. Keep it as its own commit.

**Files:**
- Modify: `services/clients.go`, `services/coalitions.go`, `services/notification.go`, `services/server_control.go`, `services/settings.go`

**Interfaces:**
- Consumes: nothing
- Produces: a compiling headless build, which every later task's verification depends on

- [ ] **Step 1: Confirm the failure exists**

```bash
mkdir -p "$TMPDIR/go-build"
cd /Users/schiba/Projects/vanguard/vngd-srs-server
env -u GOROOT GOCACHE="$TMPDIR/go-build" go build -tags headless ./... 2>&1 | grep -v "^ld: warning"
```

Expected: 8 errors of the form `services/coalitions.go:50:8: c.App.App undefined (type *app.VCSApplication has no field or method App)`.

- [ ] **Step 2: Add the build constraint to all five files**

Each file currently starts with `package services`. Prepend the constraint and a blank line, so `services/clients.go` begins:

```go
//go:build !headless

package services
```

Apply the identical two-line prefix to all five files:

```bash
cd /Users/schiba/Projects/vanguard/vngd-srs-server
for f in services/*.go; do
  printf '//go:build !headless\n\n' | cat - "$f" > "$f.tmp" && mv "$f.tmp" "$f"
done
head -1 services/*.go
```

- [ ] **Step 3: Verify the headless build now succeeds**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go build -tags headless ./... 2>&1 | grep -v "^ld: warning" | grep -v "writing stat cache"
```

Expected: no output.

- [ ] **Step 4: Verify the GUI build is unaffected**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go build ./... 2>&1 | grep -v "^ld: warning" | grep -v "writing stat cache"
rm -f vcs-srs-server
```

Expected: no output. (`go build ./...` writes a binary for the root main package; delete it.)

- [ ] **Step 5: Verify the full headless suite passes**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./... 2>&1 | grep -v "^ld: warning" | tail -20
```

Expected: every package `ok` or `[no test files]`. No `FAIL`.

- [ ] **Step 6: Commit**

```bash
git add services/
git commit -m "$(cat <<'MSG'
fix: tag services package as GUI-only

The services package holds Wails GUI bindings and dereferences
guiApp.App, which app/app_headless.go replaces with an empty struct.
Its only importer, main.go, is already //go:build !headless, as is
app/app_gui.go. The services files were never given the matching
constraint, so `go test -tags headless ./...` — the command CI runs —
has failed to compile on both main and develop.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
MSG
)"
```

---

### Task 2: Generate and store the voice secret in state

**Files:**
- Modify: `state/server.go` (imports; `ClientState` at :20-27; `AddClient` at :113-124; new functions)
- Modify: `state/state_test.go` (currently contains only `package state`)

**Interfaces:**
- Consumes: nothing
- Produces:
  - `state.VoiceSecretBytes` — untyped int constant, `32`
  - `state.ClientState.VoiceSecret` — `string` field
  - `(*state.ServerState).GetVoiceSecret(clientGuid uuid.UUID) (string, bool)`

- [ ] **Step 1: Write the failing tests**

Replace the entire contents of `state/state_test.go` with:

```go
package state

import (
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
)

func newTestServerState() *ServerState {
	return &ServerState{
		Clients:      make(map[uuid.UUID]*ClientState),
		RadioClients: make(map[uuid.UUID]*RadioState),
	}
}

func TestAddClientGeneratesVoiceSecret(t *testing.T) {
	s := newTestServerState()
	id := uuid.New()
	s.AddClient(id, &ClientState{Name: "Alice", Coalition: "Blue"})

	s.RLock()
	defer s.RUnlock()
	client, exists := s.Clients[id]
	if !exists {
		t.Fatal("client not added")
	}
	if client.VoiceSecret == "" {
		t.Fatal("AddClient must generate a voice secret")
	}
	if len(client.VoiceSecret) != 43 {
		t.Fatalf("expected a 43-char secret, got %d chars: %q", len(client.VoiceSecret), client.VoiceSecret)
	}
	raw, err := base64.RawURLEncoding.DecodeString(client.VoiceSecret)
	if err != nil {
		t.Fatalf("secret is not valid base64url: %v", err)
	}
	if len(raw) != VoiceSecretBytes {
		t.Fatalf("expected %d decoded bytes, got %d", VoiceSecretBytes, len(raw))
	}
}

func TestAddClientGeneratesUniqueVoiceSecrets(t *testing.T) {
	s := newTestServerState()
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id := uuid.New()
		s.AddClient(id, &ClientState{Name: "Pilot"})
		s.RLock()
		secret := s.Clients[id].VoiceSecret
		s.RUnlock()
		if _, dup := seen[secret]; dup {
			t.Fatalf("duplicate voice secret generated: %q", secret)
		}
		seen[secret] = struct{}{}
	}
}

func TestGetVoiceSecretReturnsStoredSecret(t *testing.T) {
	s := newTestServerState()
	id := uuid.New()
	s.AddClient(id, &ClientState{Name: "Alice"})

	s.RLock()
	want := s.Clients[id].VoiceSecret
	s.RUnlock()

	got, ok := s.GetVoiceSecret(id)
	if !ok {
		t.Fatal("expected ok for a known client")
	}
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestGetVoiceSecretUnknownClient(t *testing.T) {
	s := newTestServerState()
	if _, ok := s.GetVoiceSecret(uuid.New()); ok {
		t.Fatal("expected ok=false for an unknown client")
	}
}

func TestAddClientOnNilMapsDoesNotPanic(t *testing.T) {
	s := &ServerState{}
	s.AddClient(uuid.New(), &ClientState{Name: "Alice"})
	s.RLock()
	defer s.RUnlock()
	if len(s.Clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(s.Clients))
	}
	if len(s.RadioClients) != 1 {
		t.Fatalf("expected 1 radio client, got %d", len(s.RadioClients))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./state/ -run 'VoiceSecret|AddClientOnNilMaps' -v 2>&1 | grep -v "^ld: warning"
```

Expected: compile failure — `undefined: VoiceSecretBytes`, `client.VoiceSecret undefined`, `s.GetVoiceSecret undefined`.

- [ ] **Step 3: Add the field, the generator, and the lookup**

In `state/server.go`, extend the import block to include `crypto/rand` and `encoding/base64`:

```go
import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)
```

Add the `VoiceSecret` field to `ClientState`:

```go
type ClientState struct {
	Name               string
	UnitId             string
	Coalition          string
	Role               uint8
	LastUpdate         time.Time
	LatencyToControlMs int64  // measured RTT to the SRS/control gRPC node
	VoiceSecret        string // per-session secret presented in the voice HELLO packet
}
```

Add the constant and generator immediately above `AddClient`:

```go
// VoiceSecretBytes is the number of random bytes behind a voice secret.
// base64.RawURLEncoding turns 32 bytes into a 43-character string.
const VoiceSecretBytes = 32

// generateVoiceSecret returns a fresh per-session voice secret.
// crypto/rand.Read cannot fail on Go 1.24+, so there is no error to return.
func generateVoiceSecret() string {
	b := make([]byte, VoiceSecretBytes)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
```

Replace `AddClient` so it generates the secret and stops panicking on a nil `RadioClients` map:

```go
func (s *ServerState) AddClient(clientGuid uuid.UUID, client *ClientState) {
	s.Lock()
	defer s.Unlock()
	if s.Clients == nil {
		s.Clients = make(map[uuid.UUID]*ClientState)
	}
	if s.RadioClients == nil {
		s.RadioClients = make(map[uuid.UUID]*RadioState)
	}
	client.LastUpdate = time.Now()
	// Generated here rather than at the call sites so that every client in
	// ServerState is guaranteed to have one.
	client.VoiceSecret = generateVoiceSecret()
	s.Clients[clientGuid] = client
	s.RadioClients[clientGuid] = &RadioState{
		Radios: []Radio{},
		Muted:  false,
	}
}
```

Add `GetVoiceSecret` immediately after `DoesClientExist`:

```go
// GetVoiceSecret returns the client's voice secret. ok is false if the client
// is unknown. A known client with an empty secret returns ("", true); callers
// on the voice path must reject that case rather than compare against it.
func (s *ServerState) GetVoiceSecret(clientGuid uuid.UUID) (string, bool) {
	s.RLock()
	defer s.RUnlock()
	client, exists := s.Clients[clientGuid]
	if !exists {
		return "", false
	}
	return client.VoiceSecret, true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./state/ -v 2>&1 | grep -v "^ld: warning" | tail -25
```

Expected: PASS for all five new tests plus the existing settings tests.

- [ ] **Step 5: Mutation-verify the generation**

Temporarily comment out the `client.VoiceSecret = generateVoiceSecret()` line in `AddClient`, re-run Step 4, and confirm `TestAddClientGeneratesVoiceSecret` FAILS with "AddClient must generate a voice secret". Restore the line and confirm the tests pass again. A test that cannot fail is not evidence.

- [ ] **Step 6: Commit**

```bash
git add state/server.go state/state_test.go
git commit -m "$(cat <<'MSG'
feat: generate a per-session voice secret in ServerState

Adds ClientState.VoiceSecret, generated inside AddClient from 32 bytes of
crypto/rand encoded as base64url. Generating it in AddClient rather than
at the two call sites in srs/auth_service.go makes "every client in
ServerState has a voice secret" an invariant of the type.

The control-path secret could not be reused: auth_service.go deletes the
authenticatingClients entry on unit-select success, so it is destroyed at
the moment the client starts existing in ServerState.

Also guards AddClient against a nil RadioClients map, which previously
panicked on a zero-valued ServerState.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
MSG
)"
```

---

### Task 3: Carry the secret in the HELLO packet payload

**Files:**
- Modify: `voice/protocol.go` (constants near :56; new constructor and accessor)
- Create: `voice/protocol_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `voice.VoiceSecretLen` — untyped int constant, `43`
  - `voice.NewVCSHelloPacket(clientId uuid.UUID, secret string) *VCSPacket`
  - `(*voice.VCSPacket).HelloSecret() (string, bool)`

- [ ] **Step 1: Write the failing tests**

Create `voice/protocol_test.go`:

```go
package voice

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNewVCSHelloPacketCarriesSecret(t *testing.T) {
	id := uuid.New()
	secret := strings.Repeat("a", VoiceSecretLen)
	pkt := NewVCSHelloPacket(id, secret)

	if pkt.Type != PacketTypeHello {
		t.Fatalf("expected HELLO, got %v", pkt.Type)
	}
	if pkt.SenderID != id {
		t.Fatalf("expected sender %v, got %v", id, pkt.SenderID)
	}
	if len(pkt.Payload) != VoiceSecretLen {
		t.Fatalf("expected a %d-byte payload, got %d", VoiceSecretLen, len(pkt.Payload))
	}
}

func TestHelloSecretRoundTrip(t *testing.T) {
	id := uuid.New()
	secret := strings.Repeat("x", VoiceSecretLen)

	parsed, err := ParsePacket(NewVCSHelloPacket(id, secret).SerializePacket())
	if err != nil {
		t.Fatalf("ParsePacket failed: %v", err)
	}

	got, ok := parsed.HelloSecret()
	if !ok {
		t.Fatal("expected a secret in the parsed packet")
	}
	if got != secret {
		t.Fatalf("expected %q, got %q", secret, got)
	}
}

// Review Focus 4: base64.RawURLEncoding emits '-' and '_', which must survive
// serialization and parsing unaltered.
func TestHelloSecretRoundTripURLSafeChars(t *testing.T) {
	id := uuid.New()
	secret := "-_" + strings.Repeat("Zz09", 10) + "_"
	if len(secret) != VoiceSecretLen {
		t.Fatalf("test fixture must be %d chars, got %d", VoiceSecretLen, len(secret))
	}

	parsed, err := ParsePacket(NewVCSHelloPacket(id, secret).SerializePacket())
	if err != nil {
		t.Fatalf("ParsePacket failed: %v", err)
	}
	got, ok := parsed.HelloSecret()
	if !ok {
		t.Fatal("expected a secret")
	}
	if got != secret {
		t.Fatalf("expected %q, got %q", secret, got)
	}
}

// Review Focus 2: bytes past the secret are reserved for future protocol use
// and must be ignored rather than rejected.
func TestHelloSecretIgnoresTrailingBytes(t *testing.T) {
	secret := strings.Repeat("q", VoiceSecretLen)
	pkt := &VCSPacket{Payload: append([]byte(secret), 0xFF, 0xFE, 0xFD)}

	got, ok := pkt.HelloSecret()
	if !ok {
		t.Fatal("a longer payload must still yield a secret")
	}
	if got != secret {
		t.Fatalf("expected %q, got %q", secret, got)
	}
}

func TestHelloSecretEmptyPayload(t *testing.T) {
	pkt := &VCSPacket{Payload: nil}
	if _, ok := pkt.HelloSecret(); ok {
		t.Fatal("expected ok=false for an empty payload")
	}
}

func TestHelloSecretShortPayload(t *testing.T) {
	pkt := &VCSPacket{Payload: []byte(strings.Repeat("s", VoiceSecretLen-1))}
	if _, ok := pkt.HelloSecret(); ok {
		t.Fatal("expected ok=false for a payload shorter than VoiceSecretLen")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./voice/ -run 'Hello' -v 2>&1 | grep -v "^ld: warning"
```

Expected: compile failure — `undefined: VoiceSecretLen`, `undefined: NewVCSHelloPacket`, `parsed.HelloSecret undefined`.

- [ ] **Step 3: Implement the constant, constructor and accessor**

In `voice/protocol.go`, extend the existing constant block:

```go
// Constants
const (
	HeaderSize = 27 // Total header size in bytes
	MagicVCS   = "VCS"

	// VoiceSecretLen is the length of the per-session voice secret as it
	// appears in a HELLO payload: base64.RawURLEncoding of 32 random bytes.
	// The secret occupies bytes [0:VoiceSecretLen]; anything after it is
	// reserved for future use and ignored.
	VoiceSecretLen = 43
)
```

Add the constructor immediately above `NewVCSHelloAckPacket`:

```go
// NewVCSHelloPacket builds a HELLO announcing the client's session and
// presenting its voice secret. The secret is carried as raw UTF-8 bytes at the
// start of the payload; the voice server validates it before binding the
// sender's address.
func NewVCSHelloPacket(clientId uuid.UUID, secret string) *VCSPacket {
	return &VCSPacket{
		Magic:     [3]byte{'V', 'C', 'S'},
		Version:   currentVersion,
		Type:      PacketTypeHello,
		Flags:     0,
		Sequence:  0,
		Frequency: 0,
		SenderID:  clientId,
		Payload:   []byte(secret),
	}
}
```

Add the accessor immediately after `ExtractKeepaliveTimestamp`:

```go
// HelloSecret returns the voice secret carried at the start of a HELLO
// payload. ok is false if the payload is shorter than VoiceSecretLen. Bytes
// beyond the secret are reserved and ignored, so a longer payload is valid.
func (p *VCSPacket) HelloSecret() (string, bool) {
	if len(p.Payload) < VoiceSecretLen {
		return "", false
	}
	return string(p.Payload[:VoiceSecretLen]), true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./voice/ -v 2>&1 | grep -v "^ld: warning" | tail -30
```

Expected: PASS for all six new tests plus the three pre-existing server tests.

- [ ] **Step 5: Mutation-verify the length guard**

Temporarily change `if len(p.Payload) < VoiceSecretLen` to `if len(p.Payload) < 0`, re-run Step 4, and confirm `TestHelloSecretEmptyPayload` and `TestHelloSecretShortPayload` FAIL (with a slice-bounds panic or an unexpected ok). Restore the guard and confirm the tests pass again.

- [ ] **Step 6: Commit**

```bash
git add voice/protocol.go voice/protocol_test.go
git commit -m "$(cat <<'MSG'
feat: carry the voice secret in the HELLO packet payload

Adds NewVCSHelloPacket and VCSPacket.HelloSecret. The secret occupies the
first VoiceSecretLen (43) bytes of the payload as raw UTF-8; bytes beyond
it are reserved and ignored so the payload can be extended later.

No length prefix: the secret is fixed-length, so there is no
attacker-controlled length field to validate. Protocol version stays 1 —
no client has shipped against it.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
MSG
)"
```

---

### Task 4: Proto schema changes and regeneration

**Files:**
- Modify: `srs.proto` (`ServerSyncResult` :356-362, `VoiceAddressUpdate` :281-284, annotations on :235-246 and :286-289)
- Modify: `control.proto` (`VoiceClientInfo`)
- Regenerate: `srspb/srs.pb.go`, `srspb/srs_grpc.pb.go`, `voicecontrolpb/control.pb.go`, `voicecontrolpb/control_grpc.pb.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `srspb.ServerSyncResult.VoiceSecret` — `string`
  - `srspb.VoiceAddressUpdate.VoiceSecret` — `string`
  - `voicecontrolpb.VoiceClientInfo.VoiceSecret` — `string`

- [ ] **Step 1: Add the field to `ServerSyncResult` in `srs.proto`**

Replace the existing `ServerSyncResult` message with:

```protobuf
message ServerSyncResult {
  map<string, ClientInfo> clients              = 1; // List of clients currently connected
  map<string, RadioInfo>  radios               = 2; // List of radio information
  ServerSettings          settings             = 3; // Current server settings
  string                  coalition_voice_addr = 4; // UDP host:port for coalition audio
  string                  global_voice_addr    = 5; // UDP host:port for global freq audio
  string                  voice_secret         = 6; // Per-session secret presented in the voice HELLO payload
}
```

- [ ] **Step 2: Add the field to `VoiceAddressUpdate` in `srs.proto`**

Replace the existing `VoiceAddressUpdate` message with:

```protobuf
message VoiceAddressUpdate {
  string coalition_voice_addr = 1; // new UDP host:port for the client's coalition
  string global_voice_addr    = 2; // current global voice node address
  string voice_secret         = 3; // unchanged across a rebalance; re-sent so a redirect is self-contained
}
```

- [ ] **Step 3: Annotate the unreachable messages in `srs.proto`**

Replace the `ServerInitializationResponse` and `VoiceHostDetails` messages with:

```protobuf
// UNIMPLEMENTED: this message is not the request or response of any RPC in this
// file, and the server never constructs it. Do not build a client against it.
// The live voice secret is ServerSyncResult.voice_secret.
message ServerInitializationResponse {
  string client_guid = 1; // Unique identifier for the client
  repeated VoiceHostDetails voice_hosts = 2; // Details of the voice server to connect to
  ClientCapabilities capabilities = 3; // Version and distribution information of the client
}

message VoiceHostDetails {
  string host = 1; // Host address for the voice server
  int32 port = 2; // Port for the voice server
  repeated FrequencyRange frequencies = 3; // List of frequencies assigned to the voice server with these details
  // UNIMPLEMENTED: never populated by the server. Use ServerSyncResult.voice_secret.
  optional string secret = 4;
}
```

Replace the `DistributionUpdate` message with:

```protobuf
// UNIMPLEMENTED: never emitted. It is declared as ServerUpdate.voice_hosts = 5,
// but buildServerUpdate never sets that variant of the oneof.
message DistributionUpdate {
  repeated VoiceHostDetails voice_hosts = 2; // Details of the voice server to connect to
  // UNIMPLEMENTED: never populated by the server. Use ServerSyncResult.voice_secret.
  optional string secret = 4;
}
```

- [ ] **Step 4: Add the field to `VoiceClientInfo` in `control.proto`**

Replace the existing `VoiceClientInfo` message with:

```protobuf
message VoiceClientInfo {
  string name         = 1;
  string coalition    = 2;
  string unit_id      = 3;
  uint32 role         = 4;
  // Per-session voice secret. Load-bearing: a distributed voice node builds its
  // ServerState from these messages and has no other source for the secret.
  // It must also be carried on every INFO_UPDATED delta, because applyDelta
  // replaces the whole ClientState and would otherwise wipe it.
  string voice_secret = 5;
}
```

- [ ] **Step 5: Regenerate both proto packages**

```bash
cd /Users/schiba/Projects/vanguard/vngd-srs-server
protoc --go_out=./srspb --go_opt=paths=source_relative \
       --go-grpc_out=./srspb --go-grpc_opt=paths=source_relative srs.proto
protoc --go_out=./voicecontrolpb --go_opt=paths=source_relative \
       --go-grpc_out=./voicecontrolpb --go-grpc_opt=paths=source_relative control.proto
```

Do **not** regenerate `vcs-auth-plugin.proto` — it is untouched and regenerating it would add unrelated version-header churn.

- [ ] **Step 6: Verify the new Go fields exist**

```bash
grep -n "VoiceSecret" srspb/srs.pb.go voicecontrolpb/control.pb.go | head
```

Expected: `VoiceSecret string` on `ServerSyncResult`, `VoiceAddressUpdate` and `VoiceClientInfo`, plus their `GetVoiceSecret()` accessors.

- [ ] **Step 7: Verify everything still builds**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go build -tags headless ./... 2>&1 | grep -v "^ld: warning" | grep -v "writing stat cache"
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./... 2>&1 | grep -v "^ld: warning" | tail -20
```

Expected: build clean; all tests still pass. Adding proto fields breaks nothing.

- [ ] **Step 8: Commit**

```bash
git add srs.proto control.proto srspb/ voicecontrolpb/
git commit -m "$(cat <<'MSG'
feat: add voice_secret to the live voice-host delivery messages

Adds voice_secret to ServerSyncResult and VoiceAddressUpdate — the only
two messages that actually reach a client with voice endpoint information
— and to VoiceClientInfo so distributed voice nodes receive it over the
control stream.

Also annotates ServerInitializationResponse, VoiceHostDetails and
DistributionUpdate as unimplemented. All three are unreachable:
ServerInitializationResponse is not the request or response of any RPC,
and ServerUpdate.voice_hosts is never set by buildServerUpdate. Their
pre-existing `secret` fields are left unpopulated so nobody wires a
client against a field the server never fills.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
MSG
)"
```

---

### Task 5: Deliver the secret over the control path

**Files:**
- Modify: `srs/srs_service.go` (`SyncClient` return at :141-153; `VoiceAddressUpdate` construction at :400-408)
- Modify: `srs/srs_service_test.go`

**Interfaces:**
- Consumes: `state.GetVoiceSecret`, `srspb.ServerSyncResult.VoiceSecret`, `srspb.VoiceAddressUpdate.VoiceSecret`
- Produces: a helper `(*SimpleRadioServer).voiceSecretFor(clientID uuid.UUID) string`

- [ ] **Step 1: Write the failing tests**

Append to `srs/srs_service_test.go`:

```go
func TestVoiceSecretForKnownClient(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "Alice", Coalition: "Blue"})

	s.serverState.RLock()
	want := s.serverState.Clients[id].VoiceSecret
	s.serverState.RUnlock()

	if want == "" {
		t.Fatal("AddClient should have generated a secret")
	}
	if got := s.voiceSecretFor(id); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestVoiceSecretForUnknownClientIsEmpty(t *testing.T) {
	s := newTestServer()
	if got := s.voiceSecretFor(uuid.New()); got != "" {
		t.Fatalf("expected an empty secret for an unknown client, got %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./srs/ -run 'VoiceSecretFor' -v 2>&1 | grep -v "^ld: warning"
```

Expected: compile failure — `s.voiceSecretFor undefined`.

- [ ] **Step 3: Add the helper and populate both paths**

In `srs/srs_service.go`, add the helper immediately after `getVoiceAddresses` (:63-66):

```go
// voiceSecretFor returns the client's voice secret, or "" if the client is
// unknown. The secret is delivered alongside the voice address so the client
// has everything it needs to send a HELLO.
func (s *SimpleRadioServer) voiceSecretFor(clientID uuid.UUID) string {
	secret, _ := s.serverState.GetVoiceSecret(clientID)
	return secret
}
```

In `SyncClient`, the block at :132-141 already resolves the caller's ID into `coalition`. Replace that block and the return so the client ID is kept and reused:

```go
	var coalition string
	var callerID uuid.UUID
	if clientID, err := clientIDFromContext(ctx); err == nil {
		callerID = clientID
		s.serverState.RLock()
		if client, exists := s.serverState.Clients[clientID]; exists {
			coalition = client.Coalition
		}
		s.serverState.RUnlock()
	}
	coalitionVoiceAddr, globalVoiceAddr := s.getVoiceAddresses(coalition)

	return &pb.SyncResponse{
		Success: true,
		SyncResult: &pb.SyncResponse_Data{
			Data: &pb.ServerSyncResult{
				Clients:            srsClients,
				Radios:             srsRadios,
				Settings:           s.buildServerSettings(),
				CoalitionVoiceAddr: coalitionVoiceAddr,
				GlobalVoiceAddr:    globalVoiceAddr,
				VoiceSecret:        s.voiceSecretFor(callerID),
			},
		},
	}, nil
```

In `SubscribeToUpdates`, add the secret to the `VoiceAddressUpdate` construction at :400-408:

```go
						addr := &pb.ServerUpdate{
							Type: pb.ServerUpdate_VOICE_ADDRESS_UPDATE,
							Update: &pb.ServerUpdate_VoiceAddressUpdate{
								VoiceAddressUpdate: &pb.VoiceAddressUpdate{
									CoalitionVoiceAddr: ce.NewAddr,
									GlobalVoiceAddr:    ce.GlobalAddr,
									VoiceSecret:        s.voiceSecretFor(clientID),
								},
							},
						}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./srs/ -v 2>&1 | grep -v "^ld: warning" | tail -25
```

Expected: PASS for both new tests and all pre-existing `srs` tests.

- [ ] **Step 5: Mutation-verify**

Temporarily change `voiceSecretFor` to `return ""`, re-run Step 4, and confirm `TestVoiceSecretForKnownClient` FAILS. Restore and confirm it passes.

- [ ] **Step 6: Commit**

```bash
git add srs/srs_service.go srs/srs_service_test.go
git commit -m "$(cat <<'MSG'
feat: deliver the voice secret on SyncClient and voice address updates

Populates ServerSyncResult.voice_secret and
VoiceAddressUpdate.voice_secret, the two live paths that carry voice
endpoint information to a client. Both run over TLS gRPC and already
identify the caller, so the secret is confidential in transit and scoped
to the requesting session.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
MSG
)"
```

---

### Task 6: Propagate the secret to distributed voice nodes

**Files:**
- Modify: `voiceontrol/service.go` (`buildStateSnapshot` :571-582; `buildClientDelta` :626-631)
- Modify: `voiceontrol/client.go` (`applySnapshot` :265-277; `applyDelta` :299-310)
- Modify: `voiceontrol/client_test.go`

**Interfaces:**
- Consumes: `voicecontrolpb.VoiceClientInfo.VoiceSecret`, `state.ClientState.VoiceSecret`
- Produces: nothing new

- [ ] **Step 1: Write the failing tests**

Append to `voiceontrol/client_test.go`:

```go
func TestApplySnapshotCarriesVoiceSecret(t *testing.T) {
	ss := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
	c := newTestClient(ss)
	id := uuid.New()

	c.applySnapshot(&pb.ClientStateSnapshot{
		Clients: map[string]*pb.VoiceClientInfo{
			id.String(): {Name: "Pilot", Coalition: "Blue", VoiceSecret: "snapshot-secret"},
		},
	})

	ss.RLock()
	defer ss.RUnlock()
	if got := ss.Clients[id].VoiceSecret; got != "snapshot-secret" {
		t.Fatalf("expected the snapshot secret, got %q", got)
	}
}

func TestApplyDeltaJoinedCarriesVoiceSecret(t *testing.T) {
	ss := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
	c := newTestClient(ss)
	id := uuid.New()

	c.applyDelta(&pb.ClientDelta{
		Type:       pb.ClientDelta_JOINED,
		ClientId:   id.String(),
		ClientInfo: &pb.VoiceClientInfo{Name: "Pilot", Coalition: "Blue", VoiceSecret: "joined-secret"},
	})

	ss.RLock()
	defer ss.RUnlock()
	if got := ss.Clients[id].VoiceSecret; got != "joined-secret" {
		t.Fatalf("expected the delta secret, got %q", got)
	}
}

// An INFO_UPDATED delta replaces the whole ClientState. If the secret is not
// carried on it, a client changing unit silently loses the ability to send a
// valid HELLO.
func TestApplyDeltaInfoUpdatedPreservesVoiceSecret(t *testing.T) {
	ss := &state.ServerState{
		Clients:      make(map[uuid.UUID]*state.ClientState),
		RadioClients: make(map[uuid.UUID]*state.RadioState),
	}
	c := newTestClient(ss)
	id := uuid.New()

	c.applyDelta(&pb.ClientDelta{
		Type:       pb.ClientDelta_JOINED,
		ClientId:   id.String(),
		ClientInfo: &pb.VoiceClientInfo{Name: "Pilot", Coalition: "Blue", UnitId: "A1", VoiceSecret: "stable-secret"},
	})
	c.applyDelta(&pb.ClientDelta{
		Type:       pb.ClientDelta_INFO_UPDATED,
		ClientId:   id.String(),
		ClientInfo: &pb.VoiceClientInfo{Name: "Pilot", Coalition: "Blue", UnitId: "B2", VoiceSecret: "stable-secret"},
	})

	ss.RLock()
	defer ss.RUnlock()
	client := ss.Clients[id]
	if client.UnitId != "B2" {
		t.Fatalf("expected the unit to update to B2, got %q", client.UnitId)
	}
	if client.VoiceSecret != "stable-secret" {
		t.Fatalf("the secret must survive an INFO_UPDATED delta, got %q", client.VoiceSecret)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./voiceontrol/ -run 'VoiceSecret' -v 2>&1 | grep -v "^ld: warning"
```

Expected: FAIL — the three tests compile (the proto field exists from Task 4) but report an empty secret, because neither `applySnapshot` nor `applyDelta` copies it.

- [ ] **Step 3: Emit the secret on the control server side**

In `voiceontrol/service.go`, in `buildStateSnapshot`, add the field to the `VoiceClientInfo` literal:

```go
			clients[id.String()] = &pb.VoiceClientInfo{
				Name:        client.Name,
				Coalition:   client.Coalition,
				UnitId:      client.UnitId,
				Role:        uint32(client.Role),
				VoiceSecret: client.VoiceSecret,
			}
```

In `buildClientDelta`, add the field to the `info` literal:

```go
		info := &pb.VoiceClientInfo{
			Name:        client.Name,
			Coalition:   client.Coalition,
			UnitId:      client.UnitId,
			Role:        uint32(client.Role),
			VoiceSecret: client.VoiceSecret,
		}
```

- [ ] **Step 4: Apply the secret on the voice node side**

In `voiceontrol/client.go`, in `applySnapshot`:

```go
		v.serverState.Clients[id] = &state.ClientState{
			Name:        info.Name,
			Coalition:   info.Coalition,
			UnitId:      info.UnitId,
			Role:        uint8(info.Role),
			VoiceSecret: info.VoiceSecret,
		}
```

In `applyDelta`, in the `JOINED, INFO_UPDATED` case:

```go
	case pb.ClientDelta_JOINED, pb.ClientDelta_INFO_UPDATED:
		if delta.ClientInfo != nil {
			v.serverState.Clients[id] = &state.ClientState{
				Name:        delta.ClientInfo.Name,
				Coalition:   delta.ClientInfo.Coalition,
				UnitId:      delta.ClientInfo.UnitId,
				Role:        uint8(delta.ClientInfo.Role),
				VoiceSecret: delta.ClientInfo.VoiceSecret,
			}
			if _, exists := v.serverState.RadioClients[id]; !exists {
				v.serverState.RadioClients[id] = &state.RadioState{Radios: []state.Radio{}}
			}
		}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./voiceontrol/ -v 2>&1 | grep -v "^ld: warning" | tail -30
```

Expected: PASS for the three new tests and all pre-existing `voiceontrol` tests.

- [ ] **Step 6: Mutation-verify**

Temporarily remove `VoiceSecret: delta.ClientInfo.VoiceSecret` from the `applyDelta` literal, re-run Step 5, and confirm `TestApplyDeltaJoinedCarriesVoiceSecret` and `TestApplyDeltaInfoUpdatedPreservesVoiceSecret` FAIL. Restore and confirm they pass.

- [ ] **Step 7: Commit**

```bash
git add voiceontrol/service.go voiceontrol/client.go voiceontrol/client_test.go
git commit -m "$(cat <<'MSG'
feat: propagate the voice secret to distributed voice nodes

A distributed voice node builds its ServerState entirely from
ClientStateSnapshot and ClientDelta messages and never calls AddClient, so
without this it has no secret to validate a HELLO against.

Carrying it on every delta is also required for correctness rather than
convenience: applyDelta constructs a whole new ClientState on
INFO_UPDATED, so a secret not present on the delta is wiped the first
time a player changes unit, and every later HELLO from that client fails.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
MSG
)"
```

---

### Task 7: Validate the secret in handleHelloPacket

**Files:**
- Modify: `voice/server.go` (imports; `Server` struct :24-37; `handleHelloPacket` :161-188; new limiter type)
- Modify: `voice/server_test.go`

**Interfaces:**
- Consumes: `state.GetVoiceSecret`, `voice.VCSPacket.HelloSecret`, `voice.NewVCSHelloPacket`
- Produces:
  - `voice.rejectLimiter` with `shouldLog(now time.Time) (bool, int)`
  - `(*voice.Server).rejectHello(reason string, senderID uuid.UUID, addr *net.UDPAddr)`
  - test helpers `newBoundTestServer(t *testing.T, ss *state.ServerState) *Server` and `newTestPeer(t *testing.T) *net.UDPConn`

- [ ] **Step 1: Write the failing tests**

Append to `voice/server_test.go`. Add `"crypto/rand"` is NOT needed; the imports required are `bytes`, `crypto/subtle` is not needed here either — add `"github.com/FPGSchiba/vcs-srs-server/state"` (already imported) and `"time"` (already imported).

```go
// newBoundTestServer returns a voice server listening on a real loopback UDP
// socket, so ACK behaviour can be asserted for real.
func newBoundTestServer(t *testing.T, ss *state.ServerState) *Server {
	t.Helper()
	s := NewServer(ss, slog.New(slog.NewTextHandler(os.Stderr, nil)), &state.DistributionState{}, &state.SettingsState{})
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	s.conn = conn
	s.running = true
	return s
}

// newTestPeer returns a loopback UDP socket standing in for a client.
func newTestPeer(t *testing.T) *net.UDPConn {
	t.Helper()
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// expectAck fails unless a packet of the given type arrives promptly.
func expectAck(t *testing.T, peer *net.UDPConn, want PacketType) {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, BufferSize)
	n, _, err := peer.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("expected a %v, got read error: %v", want, err)
	}
	pkt, err := ParsePacket(buf[:n])
	if err != nil {
		t.Fatalf("expected a %v, got unparseable packet: %v", want, err)
	}
	if pkt.Type != want {
		t.Fatalf("expected %v, got %v", want, pkt.Type)
	}
}

// expectNoAck fails if any packet arrives within the window.
func expectNoAck(t *testing.T, peer *net.UDPConn) {
	t.Helper()
	_ = peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, BufferSize)
	n, _, err := peer.ReadFromUDP(buf)
	if err == nil {
		t.Fatalf("expected no reply, but received %d bytes", n)
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected a read timeout, got: %v", err)
	}
}

// addClientWithSecret registers a client and returns its generated secret.
func addClientWithSecret(t *testing.T, ss *state.ServerState, id uuid.UUID) string {
	t.Helper()
	ss.AddClient(id, &state.ClientState{Name: "Pilot", Coalition: "Blue"})
	secret, ok := ss.GetVoiceSecret(id)
	if !ok || secret == "" {
		t.Fatal("expected AddClient to generate a secret")
	}
	return secret
}

func TestHelloValidSecretBinds(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	client, bound := s.clients[id]
	s.RUnlock()
	if !bound {
		t.Fatal("a valid HELLO must bind the client")
	}
	if client.Addr.Port != peer.LocalAddr().(*net.UDPAddr).Port {
		t.Fatalf("bound to the wrong port: %v", client.Addr)
	}
	expectAck(t, peer, PacketTypeHelloAck)
}

func TestHelloWrongSecretDoesNotBind(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	addClientWithSecret(t, ss, id)

	wrong := strings.Repeat("w", VoiceSecretLen)
	s.handleHelloPacket(NewVCSHelloPacket(id, wrong), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a HELLO with a wrong secret must not bind")
	}
	expectNoAck(t, peer)
}

// The hijack case: a bound victim must not be moved by an unauthenticated HELLO.
func TestHelloWrongSecretDoesNotOverwriteBinding(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	victimAddr := victim.LocalAddr().(*net.UDPAddr)
	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victimAddr)
	expectAck(t, victim, PacketTypeHelloAck)

	wrong := strings.Repeat("w", VoiceSecretLen)
	s.handleHelloPacket(NewVCSHelloPacket(id, wrong), attacker.LocalAddr().(*net.UDPAddr))

	s.RLock()
	client := s.clients[id]
	s.RUnlock()
	if client == nil {
		t.Fatal("the victim's binding was removed")
	}
	if client.Addr.Port != victimAddr.Port {
		t.Fatalf("the binding was hijacked: expected port %d, got %d", victimAddr.Port, client.Addr.Port)
	}
	expectNoAck(t, attacker)
}

func TestHelloMissingSecretRejected(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, ""), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a HELLO with no secret must not bind")
	}
	expectNoAck(t, peer)
}

func TestHelloShortSecretRejected(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret[:VoiceSecretLen-1]), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a truncated secret must not bind")
	}
	expectNoAck(t, peer)
}

// The pre-existing DoesClientExist rejection must still hold.
func TestHelloUnknownClientRejected(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New() // never added to state

	s.handleHelloPacket(NewVCSHelloPacket(id, strings.Repeat("a", VoiceSecretLen)), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("an unknown client must not bind")
	}
	expectNoAck(t, peer)
}

// Review Focus 1: a ClientState built from an old control server's delta has an
// empty secret. It must match nothing, not match an empty presented secret.
func TestHelloEmptyStoredSecretRejected(t *testing.T) {
	id := uuid.New()
	ss := &state.ServerState{
		Clients:      map[uuid.UUID]*state.ClientState{id: {Name: "Pilot", VoiceSecret: ""}},
		RadioClients: map[uuid.UUID]*state.RadioState{},
	}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)

	s.handleHelloPacket(NewVCSHelloPacket(id, ""), peer.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, bound := s.clients[id]
	s.RUnlock()
	if bound {
		t.Fatal("a client with no secret on record must never bind")
	}
	expectNoAck(t, peer)
}

// Review Focus 5: run with -race.
func TestHelloRaceWithCleanup(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)
	addr := peer.LocalAddr().(*net.UDPAddr)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Lock()
		delete(s.clients, id)
		s.Unlock()
	}()
	s.handleHelloPacket(NewVCSHelloPacket(id, secret), addr)
	<-done
}

func TestRejectLimiterSuppressesWithinWindow(t *testing.T) {
	var lim rejectLimiter
	base := time.Now()

	if ok, suppressed := lim.shouldLog(base); !ok || suppressed != 0 {
		t.Fatalf("the first rejection must log: ok=%v suppressed=%d", ok, suppressed)
	}
	for i := 0; i < 5; i++ {
		if ok, _ := lim.shouldLog(base.Add(time.Second)); ok {
			t.Fatal("rejections inside the window must be suppressed")
		}
	}
	ok, suppressed := lim.shouldLog(base.Add(rejectLogWindow + time.Second))
	if !ok {
		t.Fatal("a rejection after the window must log")
	}
	if suppressed != 5 {
		t.Fatalf("expected 5 suppressed, got %d", suppressed)
	}
}
```

Add `"strings"` to the import block of `voice/server_test.go`.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./voice/ -run 'Hello|RejectLimiter' -v 2>&1 | grep -v "^ld: warning" | tail -30
```

Expected: compile failure — `undefined: rejectLimiter`, `undefined: rejectLogWindow`. After those are added but before `handleHelloPacket` is changed, the hijack and rejection tests FAIL because any HELLO still binds.

- [ ] **Step 3: Add the rate limiter**

In `voice/server.go`, extend the imports to include `crypto/subtle`:

```go
import (
	"crypto/subtle"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/google/uuid"
)
```

Add the limiter type and window constant immediately below the `BufferSize` const block:

```go
// rejectLogWindow is the minimum interval between logged HELLO rejections.
const rejectLogWindow = 30 * time.Second

// rejectLimiter rate-limits rejection logging so repeated attempts stay visible
// without letting an attacker flood the log.
//
// Deliberately global rather than keyed by source address: UDP source addresses
// are trivially spoofable, so a per-address map is itself a memory-exhaustion
// vector and its attribution would be unreliable anyway.
type rejectLimiter struct {
	mu         sync.Mutex
	lastLogged time.Time
	suppressed int
}

// shouldLog reports whether this rejection should be logged now and, if so, how
// many rejections were suppressed since the last logged one.
func (r *rejectLimiter) shouldLog(now time.Time) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastLogged.IsZero() || now.Sub(r.lastLogged) >= rejectLogWindow {
		suppressed := r.suppressed
		r.suppressed = 0
		r.lastLogged = now
		return true, suppressed
	}
	r.suppressed++
	return false, 0
}
```

Add the field to the `Server` struct, after `serverId`:

```go
	serverId          string
	helloRejects      rejectLimiter
```

- [ ] **Step 4: Replace handleHelloPacket**

```go
// rejectHello records a refused HELLO without binding anything, at a rate that
// keeps repeated attempts visible without letting an attacker flood the log.
func (v *Server) rejectHello(reason string, senderID uuid.UUID, addr *net.UDPAddr) {
	if ok, suppressed := v.helloRejects.shouldLog(time.Now()); ok {
		v.logger.Warn("Rejected voice HELLO",
			"reason", reason,
			"sender_id", senderID,
			"addr", addr.String(),
			"suppressed_since_last", suppressed)
	}
}

func (v *Server) handleHelloPacket(packet *VCSPacket, addr *net.UDPAddr) {
	expected, known := v.serverState.GetVoiceSecret(packet.SenderID)
	if !known {
		v.rejectHello("unknown client", packet.SenderID, addr)
		return
	}
	// Defensive: a voice node fed by an older control server may hold a client
	// with no secret. Without this, an empty presented secret would match.
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

	v.logger.Info("Accepted voice HELLO", "sender_id", packet.SenderID, "addr", addr.String())

	v.Lock()
	v.clients[packet.SenderID] = &Client{
		Addr:     addr,
		LastSeen: time.Now(),
	}
	v.Unlock()

	if v.controlClient != nil {
		go v.controlClient.ReportClientConnected(packet.SenderID, addr)
	}

	if v.conn == nil {
		v.logger.Warn("No UDP connection available to send hello acknowledgment")
		return
	}

	ackPacket := NewVCSHelloAckPacket(packet.SenderID)
	ackData := ackPacket.SerializePacket()
	_, err := v.conn.WriteToUDP(ackData, addr)
	if err != nil {
		v.logger.Error("Failed to send hello acknowledgment",
			"to", addr.String(),
			"error", err)
		return
	}
}
```

Note the removed `v.logger.Info("Received hello packet", ...)` at the top: logging every inbound HELLO before validation is itself a log-flood vector. Acceptance is logged after validation instead.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./voice/ -v 2>&1 | grep -v "^ld: warning" | tail -40
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless -race ./voice/ 2>&1 | grep -v "^ld: warning" | tail -5
```

Expected: PASS for all tests, and no race detected.

- [ ] **Step 6: Mutation-verify each guard**

One at a time, revert a guard, run the suite, confirm the named test FAILS, then restore it and confirm PASS:

| Mutation | Test that must fail |
|---|---|
| Replace the `subtle.ConstantTimeCompare` check with `if false` | `TestHelloWrongSecretDoesNotBind`, `TestHelloWrongSecretDoesNotOverwriteBinding` |
| Remove the `expected == ""` check | `TestHelloEmptyStoredSecretRejected` |
| Change `if !ok` after `HelloSecret()` to `if false` | `TestHelloMissingSecretRejected`, `TestHelloShortSecretRejected` |
| Change `if !known` to `if false` | `TestHelloUnknownClientRejected` |

- [ ] **Step 7: Commit**

```bash
git add voice/server.go voice/server_test.go
git commit -m "$(cat <<'MSG'
feat: authenticate the voice HELLO packet

handleHelloPacket previously accepted any HELLO whose sender UUID existed
in server state and then unconditionally rebound that session's address.
The UUID travels in cleartext in every voice packet, so observing one
packet was enough to redirect a player's audio to yourself.

The HELLO now carries a per-session secret, compared with
subtle.ConstantTimeCompare. On any failure nothing is bound, nothing is
acknowledged and no state changes at all.

Rejections are logged through a global rate limiter rather than a
per-source map, because UDP source addresses are spoofable and a per-source
map would itself be a memory-exhaustion vector. The unconditional
pre-validation log line is removed for the same reason.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
MSG
)"
```

---

### Task 8: Bind every other packet type to the authenticated address

**Files:**
- Modify: `voice/server.go` (`handlePacket` :150-159; `handleKeepalivePacket`; `handleVoicePacket`; `handleTestFrequencyPacket`; `handleGoodbyePacket`)
- Modify: `voice/server_test.go`

**Interfaces:**
- Consumes: everything from Task 7
- Produces: `(*voice.Server).isBoundAddr(clientID uuid.UUID, addr *net.UDPAddr) bool`

- [ ] **Step 1: Write the failing tests**

Append to `voice/server_test.go`:

```go
// Review Focus 3: the same peer may present as 127.0.0.1 or ::ffff:127.0.0.1
// depending on socket family. A string comparison would spuriously reject it.
func TestIsBoundAddrMatchesIPv4MappedIPv6(t *testing.T) {
	s := newTestServer()
	id := uuid.New()
	s.clients[id] = &Client{
		Addr:     &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002},
		LastSeen: time.Now(),
	}

	mapped := &net.UDPAddr{IP: net.ParseIP("::ffff:127.0.0.1"), Port: 5002}
	if !s.isBoundAddr(id, mapped) {
		t.Fatal("an IPv4-mapped IPv6 address must match the same IPv4 binding")
	}

	wrongPort := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5003}
	if s.isBoundAddr(id, wrongPort) {
		t.Fatal("a different port must not match")
	}

	wrongIP := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5002}
	if s.isBoundAddr(id, wrongIP) {
		t.Fatal("a different IP must not match")
	}
}

func TestIsBoundAddrUnknownClient(t *testing.T) {
	s := newTestServer()
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002}
	if s.isBoundAddr(uuid.New(), addr) {
		t.Fatal("an unbound client must not match any address")
	}
}

// Impersonation: an attacker who sniffed the UUID must not be able to transmit
// as the victim from a different address.
func TestVoiceFromUnboundAddressDropped(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victim.LocalAddr().(*net.UDPAddr))
	expectAck(t, victim, PacketTypeHelloAck)

	s.RLock()
	before := s.clients[id].LastSeen
	s.RUnlock()

	voicePkt := NewVCSVoicePacket(id, 1, 243000, make([]byte, 40))
	s.handleVoicePacket(voicePkt, attacker.LocalAddr().(*net.UDPAddr))

	s.RLock()
	after := s.clients[id].LastSeen
	s.RUnlock()
	if !after.Equal(before) {
		t.Fatal("a voice packet from an unbound address must not refresh the session")
	}
}

// A spoofed BYE must not be able to disconnect an arbitrary player.
func TestByeFromUnboundAddressDoesNotDisconnect(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victim.LocalAddr().(*net.UDPAddr))
	expectAck(t, victim, PacketTypeHelloAck)

	s.handleGoodbyePacket(&VCSPacket{SenderID: id}, attacker.LocalAddr().(*net.UDPAddr))

	s.RLock()
	_, stillBound := s.clients[id]
	s.RUnlock()
	if !stillBound {
		t.Fatal("a BYE from an unbound address must not disconnect the client")
	}
}

func TestByeFromBoundAddressDisconnects(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)
	addr := peer.LocalAddr().(*net.UDPAddr)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), addr)
	expectAck(t, peer, PacketTypeHelloAck)

	s.handleGoodbyePacket(&VCSPacket{SenderID: id}, addr)

	s.RLock()
	_, stillBound := s.clients[id]
	s.RUnlock()
	if stillBound {
		t.Fatal("a BYE from the bound address must disconnect the client")
	}
}

func TestKeepaliveFromUnboundAddressIgnored(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victim.LocalAddr().(*net.UDPAddr))
	expectAck(t, victim, PacketTypeHelloAck)

	s.handleKeepalivePacket(NewVCSKeepalivePacket(id), attacker.LocalAddr().(*net.UDPAddr))
	expectNoAck(t, attacker)
}

// KEEPALIVE must never become a rebind path — a rebind is the whole attack.
func TestKeepaliveNeverRebindsAddress(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	victim := newTestPeer(t)
	attacker := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)

	victimAddr := victim.LocalAddr().(*net.UDPAddr)
	s.handleHelloPacket(NewVCSHelloPacket(id, secret), victimAddr)
	expectAck(t, victim, PacketTypeHelloAck)

	s.handleKeepalivePacket(NewVCSKeepalivePacket(id), attacker.LocalAddr().(*net.UDPAddr))

	s.RLock()
	bound := s.clients[id].Addr
	s.RUnlock()
	if bound.Port != victimAddr.Port {
		t.Fatalf("keepalive rebound the session: expected port %d, got %d", victimAddr.Port, bound.Port)
	}
}

// The ACK must go to the bound address, not the packet source, so nobody can
// elicit a reply for a sniffed UUID.
func TestKeepaliveAckGoesToBoundAddress(t *testing.T) {
	ss := &state.ServerState{}
	s := newBoundTestServer(t, ss)
	peer := newTestPeer(t)
	id := uuid.New()
	secret := addClientWithSecret(t, ss, id)
	addr := peer.LocalAddr().(*net.UDPAddr)

	s.handleHelloPacket(NewVCSHelloPacket(id, secret), addr)
	expectAck(t, peer, PacketTypeHelloAck)

	s.handleKeepalivePacket(NewVCSKeepalivePacket(id), addr)
	expectAck(t, peer, PacketTypeKeepalive)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./voice/ -run 'IsBoundAddr|Unbound|Bye|Keepalive' -v 2>&1 | grep -v "^ld: warning" | tail -30
```

Expected: compile failure — `s.isBoundAddr undefined`, and `handleVoicePacket`/`handleGoodbyePacket` taking one argument rather than two.

- [ ] **Step 3: Add the address check and thread the source address through**

In `voice/server.go`, add `isBoundAddr` immediately above `handleHelloPacket`:

```go
// isBoundAddr reports whether addr is the address currently bound to clientID.
//
// A verified HELLO is the only way to create or change a binding, so this is
// what authenticates every other packet type. IP.Equal is used rather than a
// string comparison so that an IPv4-mapped IPv6 form of the same address still
// matches.
func (v *Server) isBoundAddr(clientID uuid.UUID, addr *net.UDPAddr) bool {
	if addr == nil {
		return false
	}
	v.RLock()
	defer v.RUnlock()
	client, exists := v.clients[clientID]
	if !exists || client.Addr == nil {
		return false
	}
	return client.Addr.IP.Equal(addr.IP) && client.Addr.Port == addr.Port
}
```

Update the dispatch in `handlePacket`:

```go
	switch packet.Type {
	case PacketTypeHello:
		v.handleHelloPacket(packet, addr)
	case PacketTypeVoice:
		v.handleVoicePacket(packet, addr)
	case PacketTypeBye:
		v.handleGoodbyePacket(packet, addr)
	case PacketTypeKeepalive:
		v.handleKeepalivePacket(packet, addr)
	default:
		v.logger.Warn("Unknown packet type received", "type", packet.Type)
	}
```

Replace `handleKeepalivePacket` with a version that checks the binding and replies to it:

```go
func (v *Server) handleKeepalivePacket(packet *VCSPacket, addr *net.UDPAddr) {
	if !v.isBoundAddr(packet.SenderID, addr) {
		v.logger.Debug("Ignoring keepalive from an unbound address",
			"sender_id", packet.SenderID, "addr", addr.String())
		return
	}

	v.Lock()
	client, exists := v.clients[packet.SenderID]
	var boundAddr *net.UDPAddr
	if exists {
		client.LastSeen = time.Now()
		boundAddr = client.Addr
		// If the client echoed our timestamp, compute the round-trip latency.
		if ts := ExtractKeepaliveTimestamp(packet.Payload); ts > 0 {
			rtt := time.Now().UnixMilli() - ts
			if rtt > 0 {
				client.LatencyToVoiceMs = rtt
			}
		}
	}
	v.Unlock()
	if !exists {
		return
	}
	v.logger.Debug("Updated last seen for client", "sender_id", packet.SenderID, "addr", addr.String())

	if v.conn == nil {
		v.logger.Warn("No UDP connection available to send keepalive acknowledgment")
		return
	}

	// Reply to the bound address rather than the packet source, so a spoofed
	// keepalive cannot elicit a reply for a sniffed session id.
	ackPacket := NewVCSKeepaliveAckPacket(packet.SenderID)
	ackData := ackPacket.SerializePacket()
	_, err := v.conn.WriteToUDP(ackData, boundAddr)
	if err != nil {
		v.logger.Error("Failed to send keepalive acknowledgment",
			"to", boundAddr.String(),
			"error", err)
	}
}
```

Update `handleVoicePacket` to take and check the address:

```go
func (v *Server) handleVoicePacket(packet *VCSPacket, addr *net.UDPAddr) {
	if !v.isBoundAddr(packet.SenderID, addr) {
		v.logger.Debug("Dropping voice packet from an unbound address",
			"sender_id", packet.SenderID, "addr", addr.String())
		return
	}

	if v.settingsState.IsFrequencyTest(packet.FrequencyAsFloat32()) {
		v.handleTestFrequencyPacket(packet)
		return
	}

	v.Lock()
	client, exists := v.clients[packet.SenderID]
	if exists {
		client.LastSeen = time.Now()
	}
	v.Unlock()
	if !exists {
		v.logger.Warn("Received voice packet from unknown client", "sender_id", packet.SenderID)
		return
	}

	if v.serverState.IsClientMuted(packet.SenderID) {
		v.logger.Debug("Dropping voice packet from muted client", "sender_id", packet.SenderID)
		return
	}

	if len(packet.Payload) > 5 {
		v.broadcastVoice(packet, packet.SenderID)
	}

	v.logger.Debug("Received voice packet",
		"sender_id", packet.SenderID,
		"frequency", packet.FrequencyAsFloat32(),
		"size", len(packet.Payload))
}
```

`handleTestFrequencyPacket` keeps its single-argument signature: it is now only reachable from `handleVoicePacket`, which has already verified the binding, and it already echoes to `client.Addr` rather than to a packet source.

Update `handleGoodbyePacket`:

```go
func (v *Server) handleGoodbyePacket(packet *VCSPacket, addr *net.UDPAddr) {
	if !v.isBoundAddr(packet.SenderID, addr) {
		v.logger.Debug("Ignoring bye from an unbound address",
			"sender_id", packet.SenderID, "addr", addr.String())
		return
	}
	v.DisconnectClient(packet.SenderID)
}
```

- [ ] **Step 4: Fix the pre-existing keepalive race test**

`TestHandleKeepaliveRace` calls `s.handleKeepalivePacket(pkt, addr)` for a client it deletes concurrently. It still compiles unchanged, and the new binding check makes the unbound path return early, which is the behaviour under test. Leave it as is and confirm it still passes.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./voice/ -v 2>&1 | grep -v "^ld: warning" | tail -45
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless -race ./voice/ 2>&1 | grep -v "^ld: warning" | tail -5
```

Expected: PASS for every test, no race detected.

- [ ] **Step 6: Mutation-verify each guard**

| Mutation | Test that must fail |
|---|---|
| Change `handleVoicePacket`'s `if !v.isBoundAddr(...)` to `if false` | `TestVoiceFromUnboundAddressDropped` |
| Change `handleGoodbyePacket`'s check to `if false` | `TestByeFromUnboundAddressDoesNotDisconnect` |
| Change `handleKeepalivePacket`'s check to `if false` | `TestKeepaliveFromUnboundAddressIgnored` |
| Send the keepalive ACK to `addr` instead of `boundAddr` | `TestKeepaliveAckGoesToBoundAddress` (only if combined with the mutation above; otherwise note that the binding check already prevents the reflection and record that in the review) |
| Change `client.Addr.IP.Equal(addr.IP)` to `client.Addr.String() == addr.String()` | `TestIsBoundAddrMatchesIPv4MappedIPv6` |

- [ ] **Step 7: Run the full suite**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go build -tags headless ./... 2>&1 | grep -v "^ld: warning" | grep -v "writing stat cache"
env -u GOROOT GOCACHE="$TMPDIR/go-build" go vet -tags headless ./... 2>&1 | grep -v "^ld: warning"
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./... 2>&1 | grep -v "^ld: warning" | tail -20
```

Expected: build clean, all tests pass. `go vet` still reports the pre-existing `srs/srs_service.go:569:3: unreachable code`, which is not introduced here — leave it.

- [ ] **Step 8: Commit**

```bash
git add voice/server.go voice/server_test.go
git commit -m "$(cat <<'MSG'
feat: bind voice, keepalive and bye packets to the authenticated address

Authenticating HELLO alone does not stop impersonation.
handleVoicePacket checked only that a session existed, never that the
packet came from that session's address — and was not even passed one —
so a sniffed UUID was enough to transmit as another player.
handleGoodbyePacket had no check at all, so one spoofed 27-byte packet
could disconnect anyone.

Every non-HELLO packet now has to arrive from the address a verified
HELLO bound. The secret authenticates establishment of a binding; the
binding authenticates everything after it.

KEEPALIVE deliberately does not carry the secret. It cannot rebind, so it
cannot hijack, and putting a long-lived credential in every keepalive
would hand a passive sniffer the secret every few seconds instead of once.
Its ACK now goes to the bound address rather than the packet source, so a
spoofed keepalive cannot elicit a reply. A client whose NAT remaps must
re-HELLO to recover, which is already the only legitimate rebind path.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
MSG
)"
```

---

### Task 9: Document the protocol and the trust model

**Files:**
- Modify: `README.md` (Voice Communication Protocol section, roughly :170-255)

**Interfaces:**
- Consumes: nothing
- Produces: nothing

- [ ] **Step 1: Update the communication flow**

In the `#### Communication Flow` numbered list, replace items 1–3 with:

```markdown
1. **Client authenticates** with the control server and receives a Session ID, a voice secret, and a Voice Server endpoint (`ServerSyncResult.voice_secret` alongside `coalition_voice_addr` / `global_voice_addr`).
2. **Client sends HELLO** to the Voice Server, presenting its voice secret in the packet payload.
3. **Voice Server validates the secret** and, only on success, binds the client's UDP address and replies with HELLO-ACK. A HELLO with a missing, malformed or incorrect secret is silently dropped.
```

- [ ] **Step 2: Update the packet type descriptions**

In `#### Packet Types`, replace the HELLO and HELLO-ACK bullets with:

```markdown
- **HELLO**: Announces client presence and authenticates the session. The payload carries the per-session voice secret (43 bytes, `base64url`, at offset 0; later bytes are reserved). This is the only packet type that may establish or change a client's address binding.
- **HELLO-ACK**: Acknowledgement from the server, sent only after the secret has been validated.
```

- [ ] **Step 3: Update the header structure note**

Under the header table, after the `Session ID` bullet, add:

```markdown
- **Payload** for HELLO is the per-session voice secret: 43 bytes of `base64url` text at offset 0. Bytes after it are reserved for future use and ignored.
```

- [ ] **Step 4: Replace the Statelessness section with a trust model section**

Replace the `#### Statelessness` block with:

```markdown
#### Statelessness

- The Voice Server maintains only ephemeral state: a mapping of session IDs to their bound UDP address and last-seen time.
- All authentication, coalition membership, and access control are managed by the control server and mirrored to voice nodes over the control stream.

#### Trust Model

**The secret authenticates the establishment of a binding. The binding authenticates everything else.**

- **HELLO** carries the per-session voice secret and is the only packet type that may create or change a session's address binding. The secret is compared in constant time; on any failure nothing is bound, nothing is acknowledged and no state changes.
- **VOICE, KEEPALIVE and BYE** carry no secret. They must arrive from the address a verified HELLO bound, or they are dropped.

KEEPALIVE deliberately does not carry the secret. It cannot rebind, so it cannot be used to hijack a session, and repeating a long-lived credential on an unencrypted wire every few seconds would expose it far more than sending it once.

**What this does not protect against:**

- **Eavesdropping.** Payloads are cleartext. A passive observer on the network path still hears all traffic they can see. This needs transport encryption (DTLS/SRTP) and is not implemented.
- **Source-address spoofing.** An attacker who can both observe a session ID and forge the victim's source address — without needing to receive replies — can still inject VOICE and BYE packets. On a shared LAN this is achievable.
- **Flooding.** The server spawns a goroutine per received datagram with no backpressure. UDP flood mitigation is not implemented in-process.
```

- [ ] **Step 5: Verify the rendered section reads correctly**

```bash
sed -n '/## Voice Communication Protocol/,/## Distributed VOIP System Architecture/p' README.md
```

Confirm no duplicated headings, no orphaned list numbering, and that the trust model section sits before the distributed architecture heading.

- [ ] **Step 6: Commit**

```bash
git add README.md
git commit -m "$(cat <<'MSG'
docs: document voice path authentication and its trust model

Records that HELLO now carries a per-session secret, that it is the only
packet type able to establish an address binding, and that every other
packet type is authenticated by that binding.

States plainly what the model does not protect against — eavesdropping,
source-address spoofing and flooding — so the voice path is not read as
secure.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
MSG
)"
```

---

## Final Verification

Run before opening the PR. Every command must be clean.

- [ ] **Build, both tag configurations**

```bash
cd /Users/schiba/Projects/vanguard/vngd-srs-server
env -u GOROOT GOCACHE="$TMPDIR/go-build" go build -tags headless ./... 2>&1 | grep -v "^ld: warning" | grep -v "writing stat cache"
env -u GOROOT GOCACHE="$TMPDIR/go-build" go build ./... 2>&1 | grep -v "^ld: warning" | grep -v "writing stat cache"
rm -f vcs-srs-server
```

- [ ] **Vet**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go vet -tags headless ./... 2>&1 | grep -v "^ld: warning"
```

Expected: only the pre-existing `srs/srs_service.go:569:3: unreachable code`. Anything else is a regression.

- [ ] **Test, with and without the race detector**

```bash
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless ./... 2>&1 | grep -v "^ld: warning" | tail -20
env -u GOROOT GOCACHE="$TMPDIR/go-build" go test -tags headless -race ./... 2>&1 | grep -v "^ld: warning" | tail -20
```

Expected: every package `ok` or `[no test files]`, no `FAIL`, no race.

- [ ] **Confirm the client repo was not touched**

```bash
git -C /Users/schiba/Projects/vanguard/vngd-srs-server status --short
```

Expected: clean. Nothing under `~/Projects/vanguard/vcs-srs-client` may appear in any commit.
