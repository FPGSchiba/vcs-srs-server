# VCS-SRS-Server Deployment

## Prerequisites

- Docker 24+ and Docker Compose v2
- TLS certificates for the voice-control gRPC channel (generated automatically on first
  Control node start; see [Config notes](#config-notes) for how to persist them)
- A `config/` directory containing one YAML settings file per service (see below)

---

## Central deployment

The central deployment runs a Control node alongside a coalition voice node and a global
voice node on the same host.

```
deploy/
  config/
    control.yaml       # Control node settings
    voice-eu.yaml      # Coalition voice node settings
    voice-global.yaml  # Global voice node settings
  certs/               # TLS cert + key (auto-generated into this dir on first run)
```

Start all services:

```sh
docker compose -f docker-compose.central.yml up -d
```

Ports exposed on the host:

| Port | Protocol | Purpose |
|------|----------|---------|
| 14446 | TCP | Client gRPC (SRS clients connect here) |
| 14448 | TCP | Voice-control gRPC (voice nodes connect here) |
| 5002 | UDP | Coalition voice node (DCS audio) |
| 5003 | UDP | Global voice node (cross-coalition audio) |

---

## Regional voice node

A regional node is a standalone voice node that connects outward to a remote Control
server. Set `CONTROL_ADDR` to the public address of your Control node and start:

```
deploy/
  config/
    voice-region.yaml  # Voice node settings with remoteHost pointing to Control
  certs/               # Must contain the Control node's voicecontrol-cert.pem
```

```sh
docker compose -f docker-compose.region.yml up -d
```

---

## Stopping

```sh
# Central
docker compose -f docker-compose.central.yml down

# Regional
docker compose -f docker-compose.region.yml down
```

---

## Config notes

Each service reads its settings from a YAML file mounted at `/config/<name>.yaml`.
Pass the path via `--config /config/<name>.yaml` (already set in the compose files).

### Control node (`config/control.yaml`)

Key fields:

```yaml
servers:
  control:
    host: 0.0.0.0
    port: 14446       # Client gRPC port

voiceControl:
  listenHost: 0.0.0.0
  port: 14448         # Voice-control gRPC port (voice nodes connect here)
  certificateFile: /certs/voicecontrol-cert.pem
  privateKeyFile:  /certs/voicecontrol-private-key.pem
```

The certificate and private key are generated automatically by the Control node at the
paths above if they do not exist. Mount `./certs` as a read-write volume on the Control
node the first time you run it, then switch to `:ro` once the files are created.

### Coalition voice node (`config/voice-eu.yaml`)

```yaml
servers:
  voice:
    host: 0.0.0.0
    port: 5002        # UDP port this node listens on

voiceControl:
  remoteHost: control  # Docker service name of the Control node
  port: 14448
  certificateFile: /certs/voicecontrol-cert.pem  # Copy of Control's cert
  publicAddr: voice-eu  # Hostname/IP clients use to reach this node
  region: eu
```

### Global voice node (`config/voice-global.yaml`)

Same as the coalition voice node but using port 5003:

```yaml
servers:
  voice:
    host: 0.0.0.0
    port: 5003

voiceControl:
  remoteHost: control
  port: 14448
  certificateFile: /certs/voicecontrol-cert.pem
  publicAddr: voice-global
  region: eu
```

### Regional voice node (`config/voice-region.yaml`)

```yaml
servers:
  voice:
    host: 0.0.0.0
    port: 5002

voiceControl:
  remoteHost: control.example.com  # Public hostname of your Control server
  port: 14448
  certificateFile: /certs/voicecontrol-cert.pem  # Copy from the Control node
  publicAddr: voice.myregion.example.com
  region: us
```

### Certs directory

Mount `./certs` into every container. The Control node writes the cert/key pair on first
start. Copy `voicecontrol-cert.pem` (the public certificate only) to the `certs/`
directory of every voice node so they can verify the Control node's TLS identity.
