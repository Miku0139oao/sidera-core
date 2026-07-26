---
icon: material/package
---

# 包管理器

Sidera 目前没有发布独立的 APT、DNF、Homebrew 或其他第三方软件源。名为
`sing-box` 的软件包安装的是上游项目，并非 Sidera Core。

## :material-download-box: GitHub Releases

以下脚本会从本仓库的 GitHub Releases 下载与系统匹配的 Sidera 软件包，
支持 Debian、RPM 系发行版、Arch Linux、Alpine 和 OpenWrt：

```shell
curl -fsSL https://raw.githubusercontent.com/Miku0139oao/sidera-core/main/docs/installation/tools/install.sh | sh
```

使用 `--beta` 安装最新预发布版本，或使用 `--version <version>` 安装指定版本：

```shell
curl -fsSL https://raw.githubusercontent.com/Miku0139oao/sidera-core/main/docs/installation/tools/install.sh | sh -s -- --beta
curl -fsSL https://raw.githubusercontent.com/Miku0139oao/sidera-core/main/docs/installation/tools/install.sh | sh -s -- --version <version>
```

## :material-book-multiple: 服务管理

Linux 软件包会安装 `sidera` systemd 服务：

| 操作 | 命令                                           |
|------|------------------------------------------------|
| 启用 | `sudo systemctl enable sidera`                |
| 禁用 | `sudo systemctl disable sidera`               |
| 启动 | `sudo systemctl start sidera`                 |
| 停止 | `sudo systemctl stop sidera`                  |
| 重启 | `sudo systemctl restart sidera`               |
| 日志 | `sudo journalctl -u sidera --output cat -e`   |
| 实时日志 | `sudo journalctl -u sidera --output cat -f` |
