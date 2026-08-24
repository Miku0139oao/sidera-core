# Performance baseline

This document records **unattested local engineering observations**, not
marketing claims or publication-grade comparative evidence. The standalone
harness and raw local runs are kept in the sibling
`bench-sidera-xray-singbox` Git repository. It is locally committed but has no
publication remote, so its diagnostic bundles are not public evidence. Every
publishable run must identify clean source trees, exact build
commands and flags, configurations, binaries, raw samples, and the harness by
commit and SHA-256.

## Corrected methodology

The current harness uses each core as its own SOCKS client and, for VLESS
scenarios, its own server. The sink exposes a duration-controlled stream.
Connections are prepared before a shared start timestamp, so throughput no
longer depends on repeated fixed-size HTTP bodies or EOF propagation.

Metrics and correctness evidence recorded by the engineering harness include:

- SOCKS CONNECT reply latency (not HTTP TTFB)
- end-to-end HTTP TTFB
- one- and eight-stream goodput
- UDP/XUDP byte-integrity, loss, duplicate and corruption accounting
- DNS UDP, TCP and forced truncation-to-TCP fallback
- Linux CPU/RSS/PSS/USS telemetry, with explicit unavailable status elsewhere
- explicit process-readiness, cleanup and correctness/error counts

All implementations use a SOCKS inbound and equivalent TLS trust/name checks.
Xray is not singled out for a splice override. Cross-core CONNECT latency is
reported but is not equivalent when one implementation lazily dials the
upstream; TTFB is the intended end-to-end latency.

The harness now fails closed on response integrity, stale readiness,
process-tree cleanup, incomplete bundles and missing publication pins. That
does not make old retained runs publishable: they predate these gates, and a
new comparative publication still requires predeclared hashes, a real harness
remote, A/A calibration, balanced blocks and native Linux/two-host telemetry.

## 2026-08-24 WSL2 observations (unattested)

The following values are transcribed from local engineering reports. They are
not release claims, have no confidence intervals, and must not be used to infer
equivalence, superiority, causation, or expected production performance.

Environment recorded for the main run: Linux amd64 under WSL2, AMD Ryzen 9
5900X, and `go1.26.5-X:nodwarf5`. Candidate source was `4e23d366` plus
uncommitted changes. The main manifest reports dirty Sidera and Xray trees and
an unknown sing-box source identity; its harness source also does not match the
current unversioned harness. Binary hashes identify those local artifacts only
and are not published in this repository.

Unattested seven-round VLESS/REALITY medians from local run
`20260824-065609`:

| Core | TTFB p50 | 1 stream | 8 streams | Client RSS | Server RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| Sidera full | 3.43 ms | 6.18 Gbps | 17.43 Gbps | 56.7 MiB | 54.7 MiB |
| sing-box v1.13.14 | 3.34 ms | 6.31 Gbps | 17.83 Gbps | 45.1 MiB | 43.3 MiB |
| Xray `6e3322d2` | 3.82 ms | 4.50 Gbps | 16.44 Gbps | 38.9 MiB | 40.0 MiB |

The Sidera and sing-box point estimates differ by roughly 2–3% in this one
cell. Without A/A calibration, balanced paired blocks, or uncertainty bounds,
that observation cannot establish equivalence or a regression threshold. RSS
values are sampled main-process `VmRSS`; they do not establish why the
artifacts differ.

The superseded early Windows smoke was unattested. A later integrated
diagnostic bundle is recorded below with hashes and retained bidirectional
interop output.

Unattested values from local one-core run `20260824-one-core`
(`taskset -c 0`, `GOMAXPROCS=1`):

| Core | TTFB p50 | 1 stream | 8 streams | Client RSS |
| --- | ---: | ---: | ---: | ---: |
| Sidera full | 4.05 ms | 3.11 Gbps | 3.52 Gbps | 52.2 MiB |
| sing-box | 3.86 ms | 3.27 Gbps | 3.51 Gbps | 40.9 MiB |
| Xray | 4.09 ms | 2.12 Gbps | 3.12 Gbps | 33.9 MiB |

That result directory has no colocated version manifest. It pins client,
server, and sink to one CPU and is a stress smoke, not an isolated client
CPU-cost measurement.

## Final integrated correctness smoke

Diagnostic bundle `20260824-131932-627705600-1099` used clean Sidera
`2dd1db15`, sing-box `f60f3728`, Xray `6e3322d2`, and locally committed harness
`35709aab`. All 27 implementation/scenario cells completed without a recorded
error across SOCKS direct, VLESS TCP/TLS/REALITY, UDP direct, VLESS/XUDP, DNS
UDP/TCP and DNS truncation fallback. REALITY interop passed in both directions.

This was deliberately one short Windows block, so it is a correctness smoke,
not comparative performance evidence. Its content-addressed files include:

- `report.json` SHA-256 `ef3922296d691dd23984105c1dfde5db82fd5b627c29ec5b8c6622e4a41e19d7`;
- `artifact-manifest.json` SHA-256 `065926ea299da991e72173ed41e62b2c01f473b3be5fbd4404a4be3f8467816c`;
- bundle status `complete`, 156 records, zero failed cells; and
- TUN status `scaffold_only_not_run` rather than a fabricated pass.

## Low-memory artifact observations (unattested)

`release/LOW_MEMORY_BUILD_TAGS` defines an additive artifact for low-RAM
systems. Final stripped Windows builds from `2dd1db15` were 36,685,824 bytes
versus 80,516,608 bytes full (54.44% smaller). This is an artifact-size fact,
not an RSS or throughput claim. Earlier unmanifested local smoke runs suggested:

- a stripped Linux binary of about 35 MiB versus 76 MiB full;
- sampled SOCKS/VLESS client RSS about 17–19 MiB lower; and
- full-CPU point estimates within a few percent in separate runs.

The low-memory and one-core-low-memory result directories have no colocated
source/build/binary manifest. These observations cannot support a quantitative
memory saving, a causal feature-footprint claim, or a throughput trade-off. The
separate one-core smoke was too variable even for a local estimate. The
low-memory artifact therefore remains opt-in, and the full profile is not
silently stripped.

## Publication gate still required

Before making comparative, superiority, equivalence, or low-end-readiness
claims:

1. publish the committed harness to a pinned remote and benchmark clean,
   immutable commits using official Go 1.26.5;
2. retain source archives, complete commands/environments/configurations, raw
   samples, logs, and SHA-256 values for every source tree, harness, and binary;
3. run on native Linux for PID ownership and PSS/USS/CPU evidence; Windows
   intentionally marks those publication telemetry fields unavailable;
4. run balanced randomized paired blocks only after byte-identical A/A noise
   calibration, preserving failed pairs as infrastructure failures;
5. collect CPU seconds/GiB, PSS/USS, Go heap/GC, idle/load/drain memory, errors,
   and actual phase durations;
6. run production-splice and uniformly userspace-only cells on netns/veth or
   two hosts, plus full/low, 1/2-core, memory-pressure, TCP/UDP/TUN, churn,
   reload, long-duration, and directed cross-core interoperability matrices;
7. publish analysis rules and confidence intervals before inspecting final
   comparative results.

The superseded fixed-64-MiB loopback numbers must not be used to claim that
Xray is tens of times slower. Current evidence supports engineering diagnosis
only.
