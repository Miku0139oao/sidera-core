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

### Runtime ownership

Server profiles in the dashboard are desired state. To let the dashboard apply
structural changes safely, start Sidera with a dedicated last-known-good runtime
document:

```bash
sidera -C /etc/sidera --runtime-config /var/lib/sidera/runtime.json run
```

Sidera writes the effective native configuration only after the complete
candidate instance has started and all staged services are ready. A failed
startup or reload restores the dashboard Store and runtime files, then starts
the previous runtime document. Both `runtime.json` and `runtime.json.bak` are
eligible for cold-start fallback.

The runtime path must be explicitly configured. It must not be a source
configuration, an Xray sidecar, a dashboard `data_path`, or a JSON file inside
a `--config-directory`. The file is atomically replaced with restricted
permissions and may contain credentials, certificates, and private keys.

Validate the desired configuration before reload, and validate the stored
runtime document independently when diagnosing fallback behavior:

```bash
sidera -C /etc/sidera --runtime-config /var/lib/sidera/runtime.json check
sidera --runtime-config /var/lib/sidera/runtime.json check --active-runtime
```

`SIGHUP` reload and dashboard-triggered reload use the same checked activation
transaction. The packaged systemd and OpenRC services configure a writable
runtime path by default.

### Structure

```json
{
  "type": "api",
  
  ... // Listen Fields
  
  "secret": "replace-with-a-random-secret",
  "access_control_allow_origin": [],
  "access_control_allow_private_network": false,
  "dashboard": {
    "enabled": true,
    "path": "",
    "download_url": "",
    "data_path": "sidera-dashboard.json",
    "public_base_url": "https://panel.example.com",
    "http_client": "", // or {}
    "update_interval": ""
  },
  "tls": { "enabled": true }
}
```

### Listen Fields

See [Listen Fields](/configuration/shared/listen/) for details.

### Fields

#### secret

Secret for the API.

Clients authenticate with the standard `authorization: Bearer <secret>` gRPC metadata header.

An enabled management dashboard requires a non-empty secret, including on a
loopback listener. API-only services without the dashboard may leave it empty.

#### access_control_allow_origin

CORS allowed origins. An enabled dashboard defaults to same-origin access when
this list is empty. API-only services retain the `*` default.

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

Only one dashboard-enabled API service may exist in a Core instance. A
non-loopback dashboard listener also requires enabled TLS.

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
`0700`. It also stores cryptographically random subscription tokens keyed by
the exact user name.

`sidera-dashboard.json` in the working directory is used by default.

For an Xray/x-ui configuration loaded from `config.json`, an optional native
bootstrap file named `config.json.sidera.json` may contain only the Sidera API
service. This keeps the dashboard configuration independent from files x-ui
regenerates. Dashboard-owned server profiles are then loaded from `data_path`.

##### 3x-ui import

This optional endpoint is available only in builds compiled with the
`with_3xui_import` tag. Compact default builds return `501 Not Implemented`
without linking the SQLite engine.

Authenticated clients can submit a 3x-ui 3.5.0 SQLite backup to
`POST /api/admin/imports/3x-ui/dry-run` as `multipart/form-data`. The `database`
part contains one SQLite file up to 256 MiB. The optional `inbound_map` part is
a JSON object, up to 1 MiB, mapping numeric 3x-ui inbound IDs to existing Sidera
server tags, for example `{ "1": "reality-in" }`.

The endpoint opens the temporary database read-only, runs an integrity and
schema check, and returns a deterministic compatibility report. It never
changes the dashboard store or runtime users. UUIDs, passwords, authentication
values, external-link values, subscription IDs, and operator credentials are
not returned. A valid SQLite upload returns `200` even when the report contains
blocking compatibility errors; malformed uploads and maps return `400`.

To apply an import, submit the same `database` and `inbound_map` parts to
`POST /api/admin/imports/3x-ui/apply`, together with a `fingerprint` form part
equal to the fingerprint returned by the preflight report. Sidera computes the
fingerprint from both the database bytes and canonical inbound map, then reruns
the complete preflight while holding the dashboard mutation lock. A changed
upload or map returns `409`; a current report containing
blocking issues is returned with `409` and does not change state.

A successful apply returns `201` and creates `account_global` accounts. Account
enabled state, quota, expiry, IP limit, reset interval, and base traffic come
from the 3x-ui clients. Source inbound enabled state is retained per membership.
Sidera generates native subscription tokens and also retains valid 3x-ui
subscription IDs as external aliases. All mapped inbound records are validated
before they are changed. Store persistence and runtime user updates are one
transaction: a runtime or save failure restores accounts, memberships,
subscription maps, and runtime users.

The built-in dashboard exposes this two-step workflow on the Settings page. A
new preflight is required after changing either the selected database or the
inbound map.

##### public_base_url

Public HTTPS origin used for user subscription links, for example
`https://panel.example.com`. It must not contain an explicit port, path, query,
userinfo, or fragment. When configured, unauthenticated `GET` and `HEAD`
requests to `/sub/sidera/{token}` return a no-store, padded Base64 URI list.
Deployments may also preserve 3x-ui subscription IDs in the dashboard store.
Sidera serves those IDs natively at `/sub/{external-id}`, so existing client
URLs can remain valid while the reverse proxy is moved away from 3x-ui.

Subscriptions group active users with the same exact name across applied
dashboard server profiles. They currently include VLESS with Reality,
Hysteria2, and TUIC profiles that have complete advertise metadata. Reality
public keys are derived from private keys and private keys are never emitted.

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
