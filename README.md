# vcs-srs-server

## About

This is the official server for the [Vanguard Communication System](). It is a distributed VOIP system that allows users to communicate over digitalized radio frequencies. The server is written in Go and uses gRPC for communication between the different components.

## Installation

Important: This project uses the [Wails](https://v3alpha.wails.io/) framework, so you need to install it first. You can find the installation instructions on the [Wails website](https://v3alpha.wails.io/getting-started/installation/).
Be aware that Wails3 is currently in alpha, so you need to install the Wails3 CLI. You can do this by running:

```bash
go install -v github.com/wailsapp/wails/v3/cmd/wails3@latest
```

### Note about Wails3

I am currently using Wails3 mainly for the Task file to be able to build the different modes of the server.
So to run the commands that are mentioned in the Live development section, you need to have Wails3 installed. But you can also run the server without Wails3 by using the commands configured in the `Taskfile.yml` file.

## Development

### Standalone GUI Mode

To run the server in standalone GUI mode, you can use the Wails framework. This allows you to run the server with a graphical user interface (GUI) that provides an easy way to manage the server and its settings.

Use the command: 
```bash
wails3 dev
```

Or
```bash
wails3 task run
```

This will start the server in standalone mode with a GUI. The GUI will allow you to manage the server and configure settings.

### Standalone Headless Mode

To run the server in standalone headless mode, you can use the following command:

```bash
wails3 task headless-standalone
```

This will start the server without a GUI, allowing you to run it in the background or on a server without a graphical interface. Also it does not need any dependencies related to the GUI, so it is more lightweight and faster to start.

### Distributed Control Server Mode

To run the server in distributed control server mode, you can use the following command:

```bash
wails3 task headless-control
```

This will start the server in a mode that allows it to communicate with multiple voice servers. This is useful for larger deployments where you need to manage multiple voice servers and distribute the load across them.

### Distributed Voice Server Mode

To run the server in distributed voice server mode, you can use the following command:

```bash
wails3 task headless-voice
```

### Config File

Git currently ignores the runtime config. So copy the `example.config.yaml` to `config.yaml` and edit it to your needs.

The config file is also editable through the Server GUI.

### Building Protobuf files

To build a `*.proto` file, you need to install the `protoc` compiler and the Go plugin for it. You can do this by running:

```bash
protoc --go_out=./srspb --go_opt=paths=source_relative --go-grpc_out=./srspb --go-grpc_opt=paths=source_relative srs.proto
```

## Building

To build a redistributable, production mode package, use `wails build`.

## Running

The built application can be found in the `build/bin` directory. You can run it by executing the binary file.

### Flags
You can pass flags to the application by using the `--` separator. The following Flags are available:
- `--config /path/to/config.yaml` - Path to the config file. Default is `./config.yaml`
- `--autostart` - If the servers should be started automatically. Default is `false`
- `--banned /path/to/banned.json` - Path to the banned users file. Default is `banned_clients.json`
- `--log-folder /path/to/logs` - Path to the log folder. Default is `log`
- `--file-log` - If the logs should be written to a file. Default is `true`~~

## Improvements

* Use a Go Module for the Protobuf files / Generated Go code
  * Use this when the C# Client is finished and the Go Client is ready
* UI to configure plugins and view their status
  * Should include a reload button to reload the plugin without restarting the server
* Observability over all components
  * Metrics, Tracing, Logging
  * Especially tracing for user sessions over multiple components

## Authentication & Plugin architecture

This project uses a plugin architecture for authentication. The `vcs-vanguard-auth-plugin` is used to authenticate users against the Vanguard Wix platform. The plugin can be extended or replaced with other authentication methods as needed.

For more information on how to create your own plugin, see the [plugin documentation](https://github.com/FPGSchiba/vcs-vanguard-auth-plugin).

### Architecture

Plugins are in simple terms just gRPC servers that implement the `AuthPlugin` Service. The main server will connect to the plugin and use it to authenticate users. The Service is written to be very flexible and can be used with any payloads.

```mermaid
sequenceDiagram
  participant User
  participant VCS-Server
  participant Plugin-A
  participant Plugin-B
  participant 3rd-Party

  critical PLugin Connection and Configuration
    VCS-Server->>Plugin-A: Connection and Configuration
    VCS-Server->>Plugin-B: Connection and Configuration
  end

  User->>VCS-Server: Client Initialization
  activate VCS-Server
  VCS-Server-->>User: Distribution and Plugin Information
  deactivate VCS-Server
  
  alt Login
    User->>VCS-Server: Guest Login
    activate VCS-Server
    VCS-Server->>User: Token (With Guest Role and selected Coalition and Unit)
    deactivate VCS-Server
  else
    User->>VCS-Server: Login (Using Plugin-A)
    activate VCS-Server
    VCS-Server->>Plugin-A: Credential forwarding
    activate Plugin-A
    Plugin-A<<-->>3rd-Party: Credential Verification
    Plugin-A-->>VCS-Server: User Infromation (Roles and Units)
    deactivate Plugin-A
    VCS-Server-->>User: User Login Information (Roles, Units and Coalitions)
    deactivate VCS-Server
    User->>VCS-Server: UnitSelection (With Secret from Login)
    activate VCS-Server
    VCS-Server-->>User: Token (With selected Role, Unit and Coalition)
    deactivate VCS-Server
  else
    User->>VCS-Server: Login (Using Plugin-B)
    activate VCS-Server
    VCS-Server->>Plugin-B: Credential forwarding
    activate Plugin-B
    Plugin-B-->>VCS-Server: User Information (Roles and Units)
    deactivate Plugin-B
    VCS-Server-->>User: User Login Information (Roles, Units and Coalitions)
    deactivate VCS-Server
    User->>VCS-Server: UnitSelection (With Secret from Login)
    activate VCS-Server
    VCS-Server-->>User: Token (With selected Role, Unit and Coalition)
    deactivate VCS-Server
  end
```

## Voice Communication Protocol

### Overview

The Voice Communication Protocol enables real-time, distributed voice communication over digital radio-like frequencies. It is designed for low-latency, stateless operation, supporting both standalone and distributed deployments.  
Clients authenticate and receive a session token, then communicate with a Voice Server using compact UDP packets. Each client can listen to multiple frequencies and transmit on one at a time, with all routing and access control managed by the control server.  
The protocol is codec-agnostic and supports efficient fan-out to all listeners on a given frequency, while keeping the Voice Server stateless and scalable.

---

### Architecture

The communication flow follows a simple pattern: clients first authenticate their session with the Voice Server, then exchange voice data in real-time. 
The Voice Server acts as a stateless relay, forwarding voice packets to all clients listening on the same frequency. 
Periodic keepalive messages maintain NAT bindings and session liveness, while graceful disconnection is handled via BYE packets.

```mermaid
sequenceDiagram
    participant Client
    participant VoiceServer

    Client->>VoiceServer: HELLO (SessionID, voice secret)
    VoiceServer-->>Client: HELLO-ACK (only if the secret is valid)

    loop While PTT is down
        Client->>VoiceServer: VOICE (SessionID, freq, PTT=1, Opus frame)
        VoiceServer-->>OtherClients: VOICE (SessionID, freq, PTT=1, Opus frame)
    end

    Client->>VoiceServer: VOICE (SessionID, freq, PTT=0, Opus frame or empty)

    Note over Client,VoiceServer: Periodic keepalive
    Client->>VoiceServer: KEEPALIVE (SessionID, [echoed timestamp])

    Client->>VoiceServer: BYE (SessionID)
```

#### Communication Flow

1. **Client authenticates** with the control server and receives a Session ID, a voice secret, and a Voice Server endpoint (`ServerSyncResult.voice_secret` alongside `coalition_voice_addr` / `global_voice_addr`).
2. **Client sends HELLO** to the Voice Server, presenting its voice secret in the packet payload.
3. **Voice Server validates the secret** and, only on success, binds the client's UDP address and replies with HELLO-ACK. A HELLO with a missing, malformed or incorrect secret is silently dropped.
4. **Client transmits VOICE packets** when PTT is active, specifying the frequency and including the Opus audio frame.
5. **Voice Server fans out VOICE packets** to all other clients listening on the same frequency.
6. **Client sends KEEPALIVE** packets periodically to maintain its NAT binding and its liveness on the server. The server replies with a timestamped ACK so the client can measure round-trip latency. Keepalives do not change the client's listening frequencies — those are set over the control channel.
7. **Client sends BYE** when disconnecting.


### Protocol Details

#### Packet Types

- **HELLO**: Announces client presence and authenticates the session. The payload carries the per-session voice secret (43 bytes, `base64url`, at offset 0; later bytes are reserved). This is the only packet type that may establish or change a client's address binding.
- **HELLO-ACK**: Acknowledgement from the server, sent only after the secret has been validated.
- **VOICE**: Carries voice data (Opus frames) from the client to the server, and from the server to all other clients listening on the same frequency. Includes a flag indicating whether Push-To-Talk (PTT) is active.
- **KEEPALIVE**: Sent periodically by the client to maintain its NAT binding and refresh its liveness. The server's ACK carries an 8-byte timestamp the client echoes back on the next cycle for round-trip latency measurement. It must arrive from the bound address, and it cannot change that binding. It does not carry frequency information.
- **BYE**: Indicates client disconnection.

#### Header Structure

| Field        | Size (bytes) | Description                                               |
|--------------|--------------|-----------------------------------------------------------|
| Magic        | 3            | Protocol identifier (e.g., `0x564353` for `VCS`) in ASCII |
| Version/Type | 1            | 4 bits version, 4 bits type                               |
| Flags        | 1            | 1 Byte Flags described below                              |
| Sequence     | 3            | 24-bit sequence number                                    |
| Frequency    | 3            | 24-bit kHz integer (000001–999999)                        |
| Session ID   | 16           | Client GUID UUIDv4 (128 bits, RFC 4122)                   |
| Payload      | variable     | Opus frame(s) or control data                             |

- **Frequency** is encoded as an integer in kHz (e.g., 145.500 MHz → 145500).
- **Session ID** is a short, random token issued after authentication (not a JWT).
- **Payload** for HELLO is the per-session voice secret: 43 bytes of `base64url` text at offset 0. Bytes after it are reserved for future use and ignored.

**Flags**:
- **PTT**: Indicates if the client is currently transmitting (1) or not (0).
- **Intercom**: Indicates if the packet is an intercom message (1) or a regular voice packet (0). This is used for special communication modes.

#### Frequency Handling

- Each frequency is a floating-point value from 000.001 to 999.999 MHz, encoded as a 24-bit integer in kHz for compactness and precision.
- Clients may listen to multiple frequencies but may only transmit on one at a time.

#### Statelessness

- The Voice Server maintains only ephemeral state: a mapping of session IDs to their bound UDP address, last-seen time, and measured keepalive latency.
- All authentication, coalition membership, and access control are managed by the control server and mirrored to voice nodes over the control stream.

#### Trust Model

**The secret authenticates the establishment of a binding. The binding authenticates everything else.**

- **HELLO** carries the per-session voice secret and is the only packet type that may create or change a session's address binding. The secret is compared in constant time; on any failure nothing is bound, nothing is acknowledged and no state changes.
- **VOICE, KEEPALIVE and BYE** carry no secret. They must arrive from the address a verified HELLO bound, or they are dropped.

KEEPALIVE deliberately does not carry the secret. It cannot rebind, so it cannot be used to hijack a session, and repeating a long-lived credential on an unencrypted wire every few seconds would expose it far more than sending it once.

**What this does not protect against:**

- **Replay of a captured HELLO.** A HELLO carries the session ID and the secret together, in cleartext. An attacker who captures one can replay it from their own address and take over the session outright — no address forging needed. The secret does not rotate and HELLO carries no nonce or timestamp, so a captured HELLO stays valid for the life of the session. What the secret buys is that a HELLO is sent only at connect or reconnect, whereas voice packets flow continuously: the window narrows from "sniff any packet at any time" to "sniff the one HELLO at session start". Closing this needs transport encryption.
- **Eavesdropping.** Payloads are cleartext. A passive observer on the network path still hears all traffic they can see. This needs transport encryption (DTLS/SRTP) and is not implemented.
- **Source-address spoofing.** An attacker who can observe a session ID *and* forge the victim's source address — without needing to receive replies — can still inject VOICE and BYE packets on an existing binding. A forged BYE is the cheapest case: a single 27-byte packet that needs no reply disconnects the victim. On a shared LAN this is achievable. For this injection path specifically, the bar rises from "observe one packet" to "observe one packet and forge addresses blind" — a real improvement, though it does not apply to the HELLO-replay hijack above, which needs no forging at all.
- **Flooding.** The server spawns a goroutine per received datagram with no backpressure. UDP flood mitigation is not implemented in-process.


## Distributed VOIP System Architecture

### Overview

This document outlines the architecture and implementation plan for a distributed VOIP system over digitalized radio frequencies, focusing on reliability, scalability, and maintainability.

---

### Architecture

#### High-Level Components

- **Client/User:** Connects to multiple radios (frequencies) and a coalition instance.
- **Voice Server:** Handles UDP traffic for assigned frequency ranges.
- **Control Server:** Maintains system state, handles REST (frontend) and gRPC (admin, inter-server) requests.
- **Frontend:** Web interface for control and monitoring.

---

### Diagrams

#### Standalone Overview

```mermaid
flowchart TD
  Client[User/Client] -- UDP/gRPC --> StandaloneServer[Standalone Server]
  StandaloneServer -- REST --> Frontend[profile.vngd.net]
```

#### Standalone Communication

```mermaid
sequenceDiagram
  participant User
  participant Admin
  participant StandaloneServer

  User->>StandaloneServer: gRPC Login
  StandaloneServer-->>User: Login Success

  User->>StandaloneServer: UDP Connect (Voice)
  loop Voice Data Exchange (UDP)
    User->>StandaloneServer: UDP Voice Data
    StandaloneServer-->>User: UDP Voice Data (from others)
  end

  User->>StandaloneServer: gRPC Change Frequency
  StandaloneServer-->>User: Frequency Changed

  Admin->>StandaloneServer: gRPC Request Info
  StandaloneServer-->>Admin: User/Frequency Info

  Admin->>StandaloneServer: gRPC Kick User
  StandaloneServer-->>User: Disconnect Notice
  User-->>StandaloneServer: Disconnect

  Admin-->>StandaloneServer: Disconnect
```

#### Distributed Overview

```mermaid
flowchart TD
  subgraph Clients
    Client1[User/Client 1]
    Client2[User/Client 2]
    ClientN[User/Client N]
    AdminUser[Admin User]
  end

  subgraph VoiceServers
    VoiceServer1[Voice Server 1]
    VoiceServer2[Voice Server 2]
    VoiceServerN[Voice Server N]
  end

  subgraph Control
    ControlServer[Control Server]
  end
  
  subgraph Internet
    Frontend[Web Frontend]
  end

  Client1 -- UDP --> VoiceServer1
  Client2 -- UDP --> VoiceServer2
  ClientN -- UDP --> VoiceServerN

  VoiceServer1 -- gRPC --> ControlServer
  VoiceServer2 -- gRPC --> ControlServer
  VoiceServerN -- gRPC --> ControlServer

  Frontend -- REST --> ControlServer

  AdminUser -- gRPC --> ControlServer

  Client1 -- gRPC --> ControlServer
  Client2 -- gRPC --> ControlServer
  ClientN -- gRPC --> ControlServer
```

#### Distributed Communication

```mermaid
sequenceDiagram
  participant User
  participant Admin
  participant ControlServer
  participant VoiceServer1
  participant VoiceServer2

  User->>ControlServer: gRPC Login
  ControlServer-->>User: Login Success + VoiceServer1 Info

  User->>VoiceServer1: UDP Connect (Voice)
  loop Voice Data Exchange (UDP)
    User->>VoiceServer1: UDP Voice Data
    VoiceServer1-->>User: UDP Voice Data (from others)
  end

  User->>ControlServer: gRPC Change Frequency
  alt Frequency on same VoiceServer1
    ControlServer-->>User: Frequency Changed
  else Frequency moved to VoiceServer2
    ControlServer-->>User: Connect to VoiceServer2
    User-->>VoiceServer2: UDP Connect (Voice)
  end

  Admin->>ControlServer: gRPC Request Info
  ControlServer-->>Admin: User/Frequency Info

  Admin->>ControlServer: gRPC Kick User
  ControlServer->>VoiceServer1: Kick User
  VoiceServer1-->>User: Disconnect Notice
  User-->>VoiceServer1: Disconnect

  Admin-->>ControlServer: Disconnect

  %% Voice Server Outage Scenario
  VoiceServer1-->>ControlServer: Health Check Fails
  ControlServer-->>User: Reconnect to VoiceServer2
  User-->>VoiceServer2: UDP Connect (Voice)
  ControlServer-->>VoiceServer2: Assign Frequency
```

---

### Lifecycle Scenarios Covered

The above sequence diagrams illustrate the following scenarios for both Standalone and Distributed architectures:

1. **User Login** (gRPC)
2. **Voice Data Exchange** (repeating UDP communication)
3. **Changing Frequency** (gRPC)
4. **Admin Requests Information** (gRPC)
5. **Admin Kicks User** (gRPC)
6. **User Disconnection** (on kick)
7. **Admin Disconnection**
8. **[Distributed only] Voice Server Outage & Rebalancing**
  - Control Server detects Voice Server failure, reassigns frequencies, and instructs clients to reconnect.

---

### Notes

- **Voice Data Exchange** is a continuous, repeating process, as indicated by the `loop` block in the sequence diagrams.
- In the **Distributed** setup, the Control Server is responsible for health checks and rebalancing in case of Voice Server outages.
- All admin actions (info requests, kicking users) are routed through the Control Server in the distributed architecture.

---

### Quick Reference Table

| Scenario                        | Standalone Server | Distributed Server |
|----------------------------------|------------------|-------------------|
| User Login                       | gRPC to StandaloneServer | gRPC to ControlServer |
| Voice Data Exchange              | UDP to StandaloneServer  | UDP to VoiceServer    |
| Change Frequency                 | gRPC to StandaloneServer | gRPC to ControlServer (may change VoiceServer) |
| Admin Info Request               | gRPC to StandaloneServer | gRPC to ControlServer |
| Admin Kick User                  | gRPC to StandaloneServer | gRPC to ControlServer (instructs VoiceServer) |
| User Disconnection (Kick)        | StandaloneServer disconnects | VoiceServer disconnects (via ControlServer) |
| Admin Disconnection              | StandaloneServer | ControlServer     |
| Voice Server Outage & Rebalance  | N/A              | ControlServer reassigns, clients reconnect |

---