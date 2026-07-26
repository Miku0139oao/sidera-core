---
icon: material/new-box
---

!!! question "自 sing-box 1.14.0 起"

# Sidera API

Sidera API 服务提供 gRPC 远程控制、内置 Material 3 管理界面、节点与用户管理、流量额度和实时连接管理。

它可以由 iOS、macOS 和 Android 上的 [sing-box 图形客户端](/zh/clients/)（通过 Remote Control 功能）或 [sing-box dashboard](https://github.com/SagerNet/sing-box-dashboard) 访问。

服务器同时接受 [gRPC-Web](https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-WEB.md) 请求,
包括用于双向流方法的 [@improbable-eng/grpc-web](https://github.com/improbable-eng/grpc-web) WebSocket 传输。

### 结构

```json
{
  "type": "api",
  
  ... // 监听字段
  
  "secret": "替换为随机密钥",
  "access_control_allow_origin": [],
  "access_control_allow_private_network": false,
  "dashboard": {
    "enabled": true,
    "path": "",
    "download_url": "",
    "data_path": "sidera-dashboard.json",
    "public_base_url": "https://panel.example.com",
    "http_client": "", // 或 {}
    "update_interval": ""
  },
  "tls": { "enabled": true }
}
```

### 监听字段

参阅 [监听字段](/zh/configuration/shared/listen/)。

### 字段

#### secret

API 密钥。

客户端通过标准的 `authorization: Bearer <secret>` gRPC metadata 头认证。

启用管理界面时必须设置非空密钥，即使仅监听回环地址也是如此。未启用管理界面的纯 API 服务可以留空。

#### access_control_allow_origin

允许的 CORS 来源。启用管理界面时，留空默认仅允许同源访问；未启用管理界面的纯 API 服务仍默认使用 `*`。

#### access_control_allow_private_network

允许从私有网络访问。

#### dashboard

通过 API 监听器在 `/dashboard/` 提供的 Web 仪表板；其他浏览器请求将被重定向到该路径。`path` 与 `download_url` 均为空时使用内置 Sidera 管理界面。

每个 Core 实例只能启用一个管理界面。管理界面监听非回环地址时还必须启用 TLS。

内置管理界面支持所有远程 Server 协议：SOCKS、HTTP、Mixed、Shadowsocks、Snell、VMess、Trojan、Naive、ShadowTLS、VLESS、AnyTLS、Hysteria、TUIC、Hysteria2 与 OpenVPN Server。结构变更会先持久化，再通过经过完整检查的 Core 重载套用。

!!! info ""

    该对象可以替换为布尔值（等同于 `{ "enabled": <bool> }`），
    或字符串路径（等同于 `{ "enabled": true, "path": "<string>" }`）。

##### enabled

启用仪表板。

##### path

自定义仪表板文件目录。留空时使用内置管理界面，除非配置了 `download_url`。

如果目录为空，将下载仪表板，并在其中存放 `.etag` 文件以跳过未变更的更新。
非空且不含 `.etag` 文件的目录将按原样提供，且不会自动更新。

##### download_url

仪表板压缩包（zip）的下载 URL。

留空时使用内置 Sidera 管理界面。

##### data_path

管理数据 sidecar 路径，保存由面板建立的 Server 配置、凭证、额度、到期时间、流量统计，以及按精确用户名索引的加密安全随机订阅 Token。文件以 `0600` 权限原子写入，父目录使用 `0700`。

默认使用工作目录下的 `sidera-dashboard.json`。

当 Xray/x-ui 从 `config.json` 启动时，可额外建立原生 `config.json.sidera.json`，其中仅配置 Sidera API 服务。这样 x-ui 重新生成主配置时不会移除管理界面；面板节点则从 `data_path` 载入。

##### public_base_url

用于用户订阅链接的公开 HTTPS origin，例如 `https://panel.example.com`。不得包含显式端口、路径、查询参数、用户信息或 fragment。配置后，未经认证的 `GET` 与 `HEAD /sub/{token}` 会返回禁止缓存、使用标准填充 Base64 编码的 URI 列表。

订阅会按完全相同的用户名合并已套用面板节点中的有效用户。目前支持具有完整公开连接信息的 VLESS + Reality、Hysteria2 与 TUIC。Reality 公钥由私钥推导，私钥绝不会输出。

##### http_client

用于下载仪表板的 HTTP 客户端，行为与远程规则集相同。

参阅 [HTTP 客户端字段](/zh/configuration/shared/http-client/)。

留空时使用默认 HTTP 客户端：即由 [`default_http_client`](/zh/configuration/route/#default_http_client)
指定的客户端，或当 `default_http_client` 为空时使用顶级 `http_clients` 的第一项。

!!! failure "隐式默认已在 sing-box 1.14.0 废弃"

    当 `http_clients` 与 `default_http_client` 均未配置时，将使用通过默认出站连接的隐式 HTTP 客户端。
    该隐式默认已在 sing-box 1.14.0 废弃，并将在 sing-box 1.16.0 移除；请改为定义 `http_clients`。

##### update_interval

仪表板的更新间隔。

默认使用 `1d`。

#### tls

TLS 配置,参阅 [TLS](/zh/configuration/shared/tls/#inbound)。
