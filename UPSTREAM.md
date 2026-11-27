# Upstream Baselines

Sidera Core is an independent project. Upstream trees are historical and interoperability references, not release or CI parents.

CI and release binaries use official Go (`actions/setup-go`). They do not patch Go from MetaCubeX or ship Windows 7 / macOS 10.13 compatibility toolchains.

Sidera Core pins upstream revisions so compatibility targets remain reproducible.

| Project | Role | Stable baseline | Development snapshot |
| --- | --- | --- | --- |
| SagerNet/sing-box | Initial architecture and implementation base | v1.13.14 | `f60f3728ce618d97abb59ead8d7059df984ea1a0` |
| XTLS/Xray-core | Interoperability reference and selected MPL-2.0 ports | v26.3.27 | `6e3322d219140a025285ded1114fe17a5edb74d8` |
| sagernet/cronet-go | Naive outbound native library builder | pin in `.github/CRONET_GO_VERSION` | `617d38f41f935b46a68f550d9add2e38abb3f168` |

Naive/musl release jobs fetch that exact Cronet commit and its pinned
`naiveproxy` submodule. That is a build dependency, not a parent project. A
Sidera-owned mirror can be added later; do not drop naive outbound without a
product decision.

Release workflows pin third-party GitHub Actions to full commit SHA values and
pin the `ghr` uploader revision. Updating a pin requires reviewing its diff and
recording the new immutable revision in the same change. Release provenance is
valid only when the source tag resolves to the attested commit, the checkout is
clean (including untracked files), and every published artifact is listed in a
sorted `SHA256SUMS`; see `release/RELEASE.md`.

## Ported Fixes (2026-11-27)

The baselines stay pinned; no upstream rebase is taken. `SagerNet/sing-box`
v1.14.4 is 524 commits ahead of the f60f3728 snapshot and would force Go 1.25.5
plus the sing 0.9.x / sing-tun 0.9.x / quic-go 0.61 / sing-quic 0.7.x /
sing-mux 0.3.10 dependency lines onto this fork's diverged code. The fork keeps
`go 1.24.7` and sing v0.8.12, and ports individual fixes instead. Xray v26.11.9
changes REALITY only for the Go 1.26 crypto/mldsa migration and server-side
`SuppressCloseNotify`; there is no client handshake change, so the v26.3.27
baseline and 6e3322d snapshot stay pinned.

Ported in this batch (upstream commit -> Sidera destination):

- db4e5da7 (DNS address-filter ordering) -> `dns/client.go`: stale rejected
  cache hits start the background refresh before the rejection is returned.
- 9be5fcf4 (dial contexts cancelled while connections are in use) ->
  `common/dialer/tfo.go`, `common/httpclient/http3_transport.go`,
  `protocol/cloudflare/inbound.go`, `protocol/group/urltest.go`,
  `transport/v2rayhttp/client.go`, `transport/v2raygrpclite/client.go`,
  `transport/v2raygrpc/client.go`. The `route/network.go` hunk does not apply:
  this fork's `NetworkManager` has no update/notify model, and the fork's conns
  already cancel on `Close`. Dependency bumps from the commit are not taken.
- 18b03170 (HTTP/2 transport races) -> `transport/v2rayhttp/client.go`
  (`common.TypedValue` round tripper) and `transport/v2rayhttp/conn.go`
  (`baderror.WrapH2` on the readiness error). The sing/sing-mux bumps are not
  taken.
- 37bddc91 (packet handshake-failure close handler) -> `route/conn.go`.
- 0d264e4e + 19f57607 (IP-literal sniffing) -> `common/sniff/tls.go`,
  `common/sniff/http.go`.
- 60f0a036 (half-close through connection wrappers) ->
  `common/badtls/read_wait.go`, `common/tls/reality_client.go`,
  `common/tls/reality_server.go`, `route/conn.go`. The sing bump is not taken;
  pinned sing already has `UnwrapCountWriter`.
- 288411b0 (missing interface monitor) ->
  `route/rule/rule_default_interface_address.go`,
  `route/rule/rule_network_interface_address.go`,
  `dns/transport/dhcp/dhcp.go`, `protocol/direct/outbound.go`.
- f260e771 (unbounded allocations on untrusted binary data) ->
  `adapter/experimental.go`, `common/srs/binary.go`,
  `common/srs/ip_set.go`, `common/srs/ip_cidr.go`,
  `common/geosite/reader.go`, `experimental/libbox/profile_import.go`.
- e6b7c6ca (rule-set cache invalidation on URL change) ->
  `adapter/experimental.go`, `route/rule/rule_set_remote.go`
  (`SavedBinary` format version 2 with `URLHash`).
- 42f487bd (IPv6 literal hosts) -> `dns/transport/https.go`,
  `dns/transport/quic/http3.go`, `common/tls/reality_client.go`,
  `transport/v2rayhttp/client.go`, `transport/v2rayhttpupgrade/client.go`.
  The HTTP/3 dial keeps `quic.DialEarly` because the pinned quic-go v0.59
  predates `DialEarlyConn`; the TLS config and h3 ALPN wiring match upstream.
- 52f9bad0 (protocol input validation, remaining hunks) ->
  `protocol/naive/inbound.go`, `transport/v2rayhttpupgrade/server.go`,
  `transport/openconnect/device_system_stack.go`,
  `transport/openvpn/device_system_stack.go`,
  `dns/transport/fakeip/fakeip.go`, `protocol/tailscale/dns_transport.go`.
  The ja3 and sniff/quic hunks were already implemented here.
- 83aec422 (hijacked DNS handshake report) -> `route/dns.go`.
- a8091761 (hijacked DNS process info) -> `route/dns.go`.
- 94ae1619 (resolver discovery queries) -> `dns/router.go`.
- 78afbb2e (DNS dedup timeouts) -> `dns/client.go`.
- 0dd17d78 (DNS response headroom) -> `dns/client_truncate.go`,
  `protocol/dns/handle.go`, `route/dns.go`, `service/resolved/service.go`.
- 7b1d7e1c (tls_fragment empty write) needs no port: `common/tlsfragment`
  already forwards empty writes without consuming the ClientHello opportunity.

Evaluated but deliberately not ported in this batch, because they need
dedicated interop testing or a cross-layer redesign:

- 67348575 (fakeip UDP reply address mapping): new NAT-conn behavior; needs
  full-cone/QUIC interop coverage.
- 54c60dc7 (DHCP DNS search-domain expansion): resolver semantics, and this
  fork uses a per-state search list.
- 5372a195 (URL-test failover for routed connections): changes the connection
  manager signature and Box wiring.
- 154d74b7 / 42cae14c (Apple TLS read-wait sizing and engine leak):
  platform-specific, not testable in this environment.
- f50ca224 (HTTP/2 transport reset): applies only after a Go 1.27 upgrade.

If the Xray pin is ever updated, bump the development snapshot together with
the stable tag and re-run REALITY client-version interop.

## Porting Rules

1. Record the upstream file, revision, and Sidera destination for every direct port.
2. Preserve upstream copyright and SPDX/license notices.
3. Prefer published protocol specifications and independent tests over copying code.
4. Do not import or start a complete second proxy core.
5. Reject unsupported configuration explicitly instead of silently changing behavior.
