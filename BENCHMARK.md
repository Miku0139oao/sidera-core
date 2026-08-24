# Performance baseline

This document records engineering baselines, not marketing claims. Raw runs and
the standalone harness are kept in the sibling `bench-sidera-xray-singbox`
workspace. Every publishable run must identify source trees, build flags,
binaries, configurations, and the harness by hash.

## Corrected methodology

The comparison uses each core as its own SOCKS client and, for VLESS scenarios,
its own server. The sink exposes a duration-controlled stream. Connections are
prepared before a shared start timestamp, so throughput no longer depends on
repeated fixed-size HTTP bodies or EOF propagation.

Metrics:

- SOCKS CONNECT reply latency (not HTTP TTFB)
- end-to-end HTTP TTFB
- one- and eight-stream goodput
- client/server peak RSS on Linux
- explicit correctness/error counts

All implementations use a SOCKS inbound and equivalent TLS trust/name checks.
Xray is not singled out for a splice override. Cross-core CONNECT latency is
reported but not treated as equivalent when one implementation lazily dials the
upstream; TTFB is the comparable end-to-end latency.

## 2026-08-24 WSL2 engineering run

Environment: Linux amd64 under WSL2, AMD Ryzen 9 5900X, Go
`go1.26.5-X:nodwarf5`. Candidate source was `4e23d366` plus the uncommitted
stability changes in this review, so binary hashes—not the source revision
alone—identify these runs.

Seven-round VLESS/REALITY median:

| Core | TTFB p50 | 1 stream | 8 streams | Client RSS | Server RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| Sidera full | 3.43 ms | 6.18 Gbps | 17.43 Gbps | 56.7 MiB | 54.7 MiB |
| sing-box v1.13.14 | 3.34 ms | 6.31 Gbps | 17.83 Gbps | 45.1 MiB | 43.3 MiB |
| Xray `6e3322d2` | 3.82 ms | 4.50 Gbps | 16.44 Gbps | 38.9 MiB | 40.0 MiB |

Sidera and sing-box are within roughly 2–3% in this cell, below a defensible
regression threshold without A/A calibration. The clear difference is the full
profile's broader feature footprint.

A post-fix Windows interoperability smoke then completed 10/10 REALITY requests
in both directions (`Sidera client → Xray server` and `Xray client → Sidera
server`). This is a correctness gate, not a throughput result.

A one-core (`taskset -c 0`, `GOMAXPROCS=1`) REALITY pressure run produced:

| Core | TTFB p50 | 1 stream | 8 streams | Client RSS |
| --- | ---: | ---: | ---: | ---: |
| Sidera full | 4.05 ms | 3.11 Gbps | 3.52 Gbps | 52.2 MiB |
| sing-box | 3.86 ms | 3.27 Gbps | 3.51 Gbps | 40.9 MiB |
| Xray | 4.09 ms | 2.12 Gbps | 3.12 Gbps | 33.9 MiB |

This pins client, server, and sink to one CPU and is a stress model, not an
isolated client CPU-cost measurement.

## Low-memory artifact

`release/LOW_MEMORY_BUILD_TAGS` defines an additive artifact for low-RAM
systems. In this run:

- stripped Linux binary: about 35 MiB versus 76 MiB full (`-54%`)
- SOCKS/VLESS peak client RSS: about 17–19 MiB lower
- full-CPU throughput generally remained within a few percent across separate
  runs
- the separate one-core low-memory run was too variable to quantify a
  throughput trade-off, so a paired constrained-hardware gate is still required

The low-memory artifact therefore remains opt-in. The full profile is not
silently stripped.

## Release gate still required

Before making superiority or low-end-readiness claims:

1. benchmark clean commits with official Go 1.26.5 and content-addressed
   binaries;
2. run at least seven balanced paired blocks after an A/A noise calibration;
3. collect CPU seconds/GiB, PSS/USS, Go heap/GC, idle and post-load memory;
4. run production-splice and uniformly userspace-only cells on netns/veth or
   two hosts;
5. add UDP/TUN, churn, reload, long-duration memory slope, and fault injection.

The superseded fixed-64-MiB loopback numbers must not be used to claim that
Xray is tens of times slower.
