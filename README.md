# Sidera Core

Sidera Core is a single-data-plane universal proxy core for Linux and Windows.
It is derived from sing-box and is being extended with selected Xray-core
capabilities behind one router, DNS engine, lifecycle, and configuration model.

The project is under active development. No compatibility or security claim is
made until the corresponding item is green in the published compatibility
matrix.

## Configuration Status

- Native sing-box JSON remains the default and is decoded unchanged.
- Xray JSON is detected automatically. The current translation slice covers
  local SOCKS, mixed, and HTTP inbounds; VLESS, freedom, and blackhole
  outbounds; TLS, REALITY, WebSocket, gRPC, HTTPUpgrade; and common field-based
  routing rules.
- Unsupported Xray fields fail with an explicit error. Multi-file Xray merge,
  mixed-dialect merge, and rewriting Xray input through `format` or `merge` are
  not enabled yet because their native semantics are not preserved.

## Goals

- Load native sing-box and Xray configuration dialects without silent field loss.
- Preserve the broad protocol, DNS, routing, TUN, endpoint, and service support
  of the sing-box architecture.
- Add Xray capabilities such as VLESS Encryption, current Vision/XUDP behavior,
  XHTTP, FinalMask, fallback, and reverse proxy without embedding a second core.
- Gate releases on bidirectional upstream interoperability, fuzzing, race tests,
  and repeatable performance measurements.

## Build

The supported release toolchain is Go 1.25.12. The repository includes a
`.go-version` file, and Make targets set `GOTOOLCHAIN=go1.25.12` to avoid
private runtime symbol changes in newer Go releases.

```sh
go build ./cmd/sidera
```

The optional 3x-ui SQLite importer is excluded from the compact default binary.
Build it explicitly when migration support is required:

```sh
go build -tags with_3xui_import ./cmd/sidera
```

The release build profiles and optional feature tags are listed in `release/`.

## Provenance

Sidera Core is an independent derivative and is not affiliated with SagerNet,
sing-box, Project X, or Xray-core. See `UPSTREAM.md` and `NOTICE.md` for pinned
source revisions and attribution.

## License

The combined project is distributed under GPL-3.0-or-later. Files derived from
MPL-2.0 sources retain their notices and MPL-2.0 obligations.
