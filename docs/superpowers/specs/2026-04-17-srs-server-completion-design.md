# VCS SRS Server — Completion Design Spec

**Date:** 2026-04-17
**Status:** Approved
**Scope:** Standalone mode only (no distributed system changes)

---

## Overview

Three areas of the `vngd-srs-server` are incomplete. This spec covers finishing them:

1. **Missing SRS gRPC handler implementations** — gaps in `srs/srs_service.go`, `srs/utils.go`, `srs/auth_service.go`, and `app/clients.go`
2. **GraphQL HTTP API** — full admin surface over HTTP using `gqlgen`, protected by static API key
3. **Frontend additions** — Security + VoiceControl settings sections, live log view tab

Out of scope: distributed system (VoiceControl gRPC), proto file changes, auth flow changes, UDP voice server.

---

## Section 1: Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│  shared.go / main.go / headless.go                  │
│  app.New() → eventBus                               │
│  parseFlags(eventBus) → slog fanout                 │
│      ├── TextHandler (stdout)                       │
│      ├── JSONHandler (file)                         │
│      └── BusHandler (event bus) ← NEW              │
└──────────────┬──────────────────────────────────────┘
               │ eventBus
    ┌──────────▼──────────┐        ┌────────────────────┐
    │  Wails frontend      │        │  GraphQL HTTP API   │
    │  Events.On("logs/…") │        │  /api/v1/graphql    │
    │  → Logs tab (live)  │        │  X-API-Key header   │
    │  Settings: +Security │        │  gqlgen resolvers   │
    │           +VoiceCtrl │        │  → app.* / state.* │
    └──────────────────────┘        └────────────────────┘
```

**Key structural changes:**
- `app.New()` called before `parseFlags()` in both `main.go` and `headless.go` so the event bus can be threaded into the logger
- `parseFlags` gains an `*events.EventBus` parameter
- New `logging/` package: `BusHandler` implementing `slog.Handler`
- New `graphql/` package: SDL schema + gqlgen generated code + resolvers
- New `ApiSettings` section in `SettingsState` (API key)
- New Taskfile task `generate:graphql` as a sibling of `generate:proto`

**Headless mode compatibility:** `BusHandler` publishes to the internal `events.EventBus` in both modes. In headless mode the existing `for range channel {}` drain loop in `handleFrontendEmits` discards the events cleanly — no Wails calls are made.

---

## Section 2: Missing SRS gRPC Implementations

### Gap 1 — `IsIntercom` missing from radio conversions (`srs/utils.go`)

`convertSingleRadio` and `convertSingleRadioState` both silently drop the `IsIntercom` field present in both the proto and `state.Radio`. One-line fix in each function.

### Gap 2 — `buildServerUpdate` sends no payload for client/radio changes

`CLIENT_INFO_UPDATE` and `CLIENT_RADIO_UPDATE` are emitted with only `Type` set — no `ClientUpdate` oneof. Subscribed clients must call `SyncClient` again to learn what changed.

Fix: populate `pb.ClientUpdate` with the specific client's info/radio data in `buildServerUpdate`.

### Gap 3 — Cannot distinguish CLIENT_JOINED / CLIENT_LEFT / CLIENT_INFO_UPDATE

All client state changes publish the same `ClientsChanged` event with a bare `map[uuid.UUID]*state.ClientState`. `buildServerUpdate` has no way to know whether it was a join, leave, or update.

Fix: replace the raw map payload with a typed wrapper in `events/events.go`:

```go
type ClientChangeType int

const (
    ClientJoined      ClientChangeType = iota
    ClientLeft
    ClientInfoUpdated
)

type ClientChangeEvent struct {
    Type     ClientChangeType
    ClientID uuid.UUID
    Clients  map[uuid.UUID]*state.ClientState
}
```

All publishers of `ClientsChanged` are updated to use `ClientChangeEvent`. `buildServerUpdate` checks `.Type` to set the correct `UpdateType`. The event name `"clients/changed"` is unchanged — the frontend receives a richer payload.

### Gap 4 — SERVER_ACTION never sent to subscribers (kick/ban/mute)

`app.KickClient`, `app.MuteClient`, `app.UnmuteClient` have `// TODO: Implement Backend Logic to notify the Client` comments. They update state but never emit an event that subscribed gRPC clients receive.

Fix:
- New event constant `events.ServerAction` in `events/events.go`
- New payload struct `events.ServerActionEvent{ActionType, TargetClientID, Reason string}`
- `app/clients.go` publishes this alongside existing state events after each action
- `buildServerUpdate` maps `ServerAction` events to `pb.ServerUpdate{Type: SERVER_ACTION, Update: &ServerUpdate_ServerAction{...}}`

Action type values: KICK, BAN, MUTE, UNMUTE (matching `pb.ServerAction_ActionType`).

### Gap 5 — IP ban check broken in `InitAuth` (`srs/auth_service.go`)

`p.Addr.String()` returns `"IP:port"` (e.g. `"192.168.1.1:54321"`), but stored banned client IPs have no port. The equality check never matches.

Fix: use `net.SplitHostPort` to extract only the host before comparing to stored IPs.

---

## Section 3: GraphQL HTTP API

### Package layout

```
graphql/
  schema.graphql          ← SDL (source of truth, do not edit generated files)
  generated/
    generated.go          ← gqlgen output
    models_gen.go         ← gqlgen output
  resolver.go             ← Resolver struct + constructor (holds *app.VCSApplication)
  query.resolvers.go      ← Query implementations
  mutation.resolvers.go   ← Mutation implementations
gqlgen.yml                ← at project root
```

### Schema

```graphql
type Query {
  systemInfo: SystemInfo!
  clients: [Client!]!
  bannedClients: [BannedClient!]!
  settings: Settings!
}

type Mutation {
  # Settings
  updateGeneralSettings(input: GeneralSettingsInput!): MutationResult!
  updateSecuritySettings(input: SecuritySettingsInput!): MutationResult!
  updateVoiceControlSettings(input: VoiceControlSettingsInput!): MutationResult!
  updateFrequencySettings(input: FrequencySettingsInput!): MutationResult!
  updateCoalitions(coalitions: [CoalitionInput!]!): MutationResult!
  updateServerSettings(input: ServerSettingsInput!): MutationResult!
  # Server control
  startServer: MutationResult!
  stopServer: MutationResult!
  # Client management
  kickClient(clientId: ID!, reason: String!): MutationResult!
  banClient(clientId: ID!, reason: String!): MutationResult!
  unbanClient(clientId: ID!): MutationResult!
  muteClient(clientId: ID!): MutationResult!
  unmuteClient(clientId: ID!): MutationResult!
}

type SystemInfo {
  version: String!
  httpStatus: ServiceStatus!
  voiceStatus: ServiceStatus!
  controlStatus: ServiceStatus!
}

type ServiceStatus {
  isRunning: Boolean!
  error: String!
}

type Client {
  id: ID!
  name: String!
  coalition: String!
  unitId: String!
  roleId: Int!
  lastUpdate: String!
  muted: Boolean!
}

type BannedClient {
  id: ID!
  name: String!
  ipAddress: String!
  reason: String!
}

type Settings {
  general: GeneralSettings!
  security: SecuritySettings!
  voiceControl: VoiceControlSettings!
  frequencies: FrequencySettings!
  coalitions: [Coalition!]!
  servers: ServerSettings!
}

type GeneralSettings { maxRadiosPerUser: Int! }
type SecuritySettings {
  enablePluginAuth: Boolean!
  enableGuestAuth: Boolean!
  plugins: [PluginSettings!]!
}
type PluginSettings { name: String!; enabled: Boolean!; address: String! }
type VoiceControlSettings {
  port: Int!
  remoteHost: String!
  listenHost: String!
  certificateFile: String!
  privateKeyFile: String!
}
type FrequencySettings {
  testFrequencies: [Float!]!
  globalFrequencies: [Float!]!
}
type Coalition { name: String!; color: String!; description: String! }
type ServerSettings { http: ServerSetting!; voice: ServerSetting!; control: ServerSetting! }
type ServerSetting { host: String!; port: Int! }
type MutationResult { success: Boolean!; message: String }

input GeneralSettingsInput { maxRadiosPerUser: Int! }
input SecuritySettingsInput { enablePluginAuth: Boolean!; enableGuestAuth: Boolean! }
input VoiceControlSettingsInput {
  port: Int!; remoteHost: String!; listenHost: String!
  certificateFile: String!; privateKeyFile: String!
}
input FrequencySettingsInput {
  testFrequencies: [Float!]!
  globalFrequencies: [Float!]!
}
input CoalitionInput { name: String!; color: String!; description: String!; password: String }
input ServerSettingsInput {
  http: ServerSettingInput!
  voice: ServerSettingInput!
  control: ServerSettingInput!
}
input ServerSettingInput { host: String!; port: Int! }
```

### Authentication middleware

- New field in `state/settings.go`:
  ```go
  type ApiSettings struct {
      Key string `yaml:"key"`
  }
  // added to SettingsState:
  Api ApiSettings `yaml:"api"`
  ```
- New `rest/middleware.go`: Gin middleware reads `X-API-Key` header.
  - Key empty in config → `503 Service Unavailable` (API disabled)
  - Key present but wrong → `401 Unauthorized`
  - Key correct → passes through
- Applied only to `/api/v1/graphql`, not to the health root `/api/v1/`.

### Resolver wiring

`Resolver` holds `*app.VCSApplication`. All mutations delegate to existing `app.*` methods (same surface as Wails services). Queries read directly from `app.ServerState` and `app.SettingsState` under appropriate read locks.

---

## Section 4: Log Streaming

### New package `logging/`

```go
// logging/bus_handler.go

type LogEntry struct {
    Time    time.Time      `json:"time"`
    Level   string         `json:"level"`
    Message string         `json:"msg"`
    Attrs   map[string]any `json:"attrs,omitempty"`
}

type BusHandler struct {
    bus   *events.EventBus
    attrs []slog.Attr   // accumulated via WithAttrs
    group string        // accumulated via WithGroup
}
```

`Handle()` converts the `slog.Record` (plus any accumulated attrs) to `LogEntry` and calls `bus.Publish(Event{Name: events.LogEntry, Data: entry})`. `Enabled` always returns true. `WithAttrs` and `WithGroup` return a new `BusHandler` with the updated state. No inner handler — the file and console handlers are separate fanout members.

### New event constant

```go
// events/events.go
const LogEntry = "logs/entry"
```

### Logger wiring (`shared.go`)

`parseFlags` gains an `*events.EventBus` parameter. The fanout becomes:

```go
slogmulti.Fanout(
    slog.NewTextHandler(os.Stdout, nil),
    slog.NewJSONHandler(f, nil),
    logging.NewBusHandler(bus),
)
```

`app.New()` is moved before `parseFlags()` in both `main.go` and `headless.go` so the bus is available at logger construction time.

---

## Section 5: Frontend Changes

### 5a — Settings page: Security + VoiceControl sections

Two new accordion/box sections appended to the existing `Settings.tsx` form below the Servers section.

**Security section:**
- `EnableGuestAuth` toggle
- `EnablePluginAuth` toggle
- Read-only plugin table: name, address, enabled state

**VoiceControl section:**
- `ListenHost` (text), `Port` (number)
- `RemoteHost` (text)
- `CertificateFile`, `PrivateKeyFile` (text, file paths)

New Wails service methods in `services/settings.go`:
- `SaveSecuritySettings(enableGuestAuth bool, enablePluginAuth bool)` — updates only the two toggles, never touches the plugin list (which contains sensitive config). Same save/notify pattern as `SaveGeneralSettings`.
- `SaveVoiceControlSettings(input *state.VoiceControlSettings)` — same pattern as `SaveGeneralSettings`

The Zod schema in `Settings.tsx` is extended for both new sections. All sections save on the same submit button.

### 5b — New "Logs" tab

New tab 6 added to `ContentWrapper.tsx`. New page `frontend/src/pages/Logs.tsx`:

- **Ring buffer:** 500-entry in-memory cap; oldest entries dropped when full
- **Event listener:** `Events.On("logs/entry", ...)` appends new `LogEntry` to buffer
- **Display:** MUI virtualized list, level-coloured rows (DEBUG=grey, INFO=default, WARN=orange, ERROR=red)
- **Auto-scroll:** follows new entries unless user has manually scrolled up
- **Controls:** Clear button (in-memory only, does not touch log file), level filter dropdown

---

## Section 6: Build System Integration

New task in `build/Taskfile.yml`, following the same pattern as `generate:proto`:

```yaml
generate:graphql:
  summary: Generates GraphQL server code via gqlgen
  sources:
    - "graphql/schema.graphql"
    - "gqlgen.yml"
  generates:
    - "graphql/generated/generated.go"
    - "graphql/generated/models_gen.go"
  preconditions:
    - sh: which gqlgen
      msg: "Install gqlgen: go install github.com/99designs/gqlgen@latest"
  cmds:
    - go run github.com/99designs/gqlgen generate
```

Added as a dependency of `go:mod:tidy` alongside `generate:proto`:

```yaml
go:mod:tidy:
  deps:
    - task: generate:proto
    - task: generate:graphql
  cmds:
    - go mod tidy
```

`gqlgen.yml` at project root:
- Schema: `graphql/schema.graphql`
- Generated output: `graphql/generated/`
- Resolver output: `graphql/` (only generates stubs for missing methods; never overwrites existing resolvers)

---

## Implementation Order

1. `events/events.go` — new types (`ClientChangeEvent`, `ServerActionEvent`, `LogEntry` const)
2. `srs/utils.go` — `IsIntercom` fix, radio conversion fixes
3. `srs/auth_service.go` — IP ban check fix
4. `srs/srs_service.go` — `buildServerUpdate` with payload + action type differentiation
5. `app/clients.go` — emit `ServerActionEvent` from kick/ban/mute/unmute
6. `logging/bus_handler.go` — new package
7. `shared.go` + `main.go` + `headless.go` — wire bus into logger
8. `state/settings.go` — `ApiSettings` field
9. `graphql/schema.graphql` + `gqlgen.yml` — schema and config
10. `build/Taskfile.yml` — `generate:graphql` task
11. Run `go run github.com/99designs/gqlgen generate`
12. `graphql/resolver.go` + `query.resolvers.go` + `mutation.resolvers.go` — implement resolvers
13. `rest/middleware.go` — API key middleware
14. `rest/router.go` — mount GraphQL handler with middleware
15. `services/settings.go` — `SaveSecuritySettings`, `SaveVoiceControlSettings`
16. `frontend/src/pages/Settings.tsx` — Security + VoiceControl sections
17. `frontend/src/pages/Logs.tsx` — new Logs page
18. `frontend/src/components/ContentWrapper.tsx` — add Logs tab
