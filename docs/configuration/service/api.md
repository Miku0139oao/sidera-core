---
icon: material/new-box
---

!!! question "Since sing-box 1.14.0"

# Sidera API

The Sidera API service provides gRPC remote control, the built-in Material 3
dashboard, server profile management, users, quotas, traffic, and connections.

It can be accessed by the [sing-box graphical clients](/clients/) for iOS, macOS, and
Android (via the Remote Control feature), or the
[sing-box dashboard](https://github.com/SagerNet/sing-box-dashboard).

The server also accepts [gRPC-Web](https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-WEB.md) requests,
including the WebSocket transport of [@improbable-eng/grpc-web](https://github.com/improbable-eng/grpc-web)
for bidirectional streaming methods.

### Structure

```json
{
  "type": "api",
  
  ... // Listen Fields
  
  "secret": "",
  "access_control_allow_origin": [],
  "access_control_allow_private_network": false,
  "dashboard": {
    "enabled": true,
    "path": "",
    "download_url": "",
    "data_path": "sidera-dashboard.json",
    "http_client": "", // or {}
    "update_interval": ""
  },
  "tls": {}
}
```

### Listen Fields

See [Listen Fields](/configuration/shared/listen/) for details.

### Fields

#### secret

Secret for the API.

Clients authenticate with the standard `authorization: Bearer <secret>` gRPC metadata header.

If empty, authentication is disabled.

#### access_control_allow_origin

CORS allowed origins, `*` will be used if empty.

#### access_control_allow_private_network

Allow access from private network.

#### dashboard

Web dashboard served over the API listener at `/dashboard/`; other browser
requests are redirected to it. When both `path` and `download_url` are empty,
the built-in Sidera dashboard is used.

The built-in dashboard manages all remotely consumable Sidera server protocols:
SOCKS, HTTP, Mixed, Shadowsocks, Snell, VMess, Trojan, Naive, ShadowTLS,
VLESS, AnyTLS, Hysteria, TUIC, Hysteria2, and OpenVPN Server. Structural
changes are persisted first and applied through a checked Core reload. Existing
base-configuration servers remain read-only, while supported user databases can
still be updated at runtime.

!!! info ""

    The object can be replaced with a boolean value (equivalent to `{ "enabled": <bool> }`),
    or with a string path (equivalent to `{ "enabled": true, "path": "<string>" }`).

##### enabled

Enable the dashboard.

##### path

Directory containing custom dashboard files. Leave empty to use the built-in
dashboard unless `download_url` is configured.

When a managed dashboard archive is used, an `.etag` file is stored inside the
directory to skip unchanged updates. A non-empty directory without an `.etag`
file is served as-is and never updated automatically.

##### download_url

Download URL of the dashboard archive (zip).

Leave empty to use the built-in Sidera dashboard.

##### data_path

Path to the dashboard management sidecar. It stores dashboard-owned server
profiles, credentials, quotas, expiry times, and traffic counters. The file is
written atomically with mode `0600`; its parent directory is created with mode
`0700`.

`sidera-dashboard.json` in the working directory is used by default.

For an Xray/x-ui configuration loaded from `config.json`, an optional native
bootstrap file named `config.json.sidera.json` may contain only the Sidera API
service. This keeps the dashboard configuration independent from files x-ui
regenerates. Dashboard-owned server profiles are then loaded from `data_path`.

##### http_client

HTTP client used to download the dashboard, with the same behavior as remote rule-sets.

See [HTTP Client Fields](/configuration/shared/http-client/) for details.

When empty, the default HTTP client is used: the one named by
[`default_http_client`](/configuration/route/#default_http_client), or the first top-level
`http_clients` entry when `default_http_client` is empty.

!!! failure "Implicit default deprecated in sing-box 1.14.0"

    When neither `http_clients` nor `default_http_client` is configured, an implicit HTTP
    client connecting through the default outbound is used. This implicit default is
    deprecated in sing-box 1.14.0 and will be removed in sing-box 1.16.0; define
    `http_clients` instead.

##### update_interval

Update interval of the dashboard.

`1d` will be used by default.

#### tls

TLS configuration, see [TLS](/configuration/shared/tls/#inbound).
