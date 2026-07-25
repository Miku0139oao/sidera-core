# Upstream Baselines

Sidera Core pins upstream revisions so compatibility targets remain reproducible.

| Project | Role | Stable baseline | Development snapshot |
| --- | --- | --- | --- |
| SagerNet/sing-box | Initial architecture and implementation base | v1.13.14 | `f60f3728ce618d97abb59ead8d7059df984ea1a0` |
| XTLS/Xray-core | Interoperability reference and selected MPL-2.0 ports | v26.3.27 | `6e3322d219140a025285ded1114fe17a5edb74d8` |

## Porting Rules

1. Record the upstream file, revision, and Sidera destination for every direct port.
2. Preserve upstream copyright and SPDX/license notices.
3. Prefer published protocol specifications and independent tests over copying code.
4. Do not import or start a complete second proxy core.
5. Reject unsupported configuration explicitly instead of silently changing behavior.
