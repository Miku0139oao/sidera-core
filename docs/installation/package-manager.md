---
icon: material/package
---

# Package Manager

Sidera does not currently publish a standalone APT, DNF, Homebrew, or other
third-party package repository. Packages named `sing-box` install the upstream
project, not Sidera Core.

## :material-download-box: GitHub Releases

The installer downloads the matching Sidera package from this repository's
GitHub releases for Debian, RPM-based distributions, Arch Linux, Alpine, and
OpenWrt:

```shell
curl -fsSL https://raw.githubusercontent.com/Miku0139oao/sidera-core/main/docs/installation/tools/install.sh | sh
```

Use `--beta` for the latest prerelease or `--version <version>` for a specific
release:

```shell
curl -fsSL https://raw.githubusercontent.com/Miku0139oao/sidera-core/main/docs/installation/tools/install.sh | sh -s -- --beta
curl -fsSL https://raw.githubusercontent.com/Miku0139oao/sidera-core/main/docs/installation/tools/install.sh | sh -s -- --version <version>
```

## :material-book-multiple: Service Management

Linux packages install a `sidera` systemd service:

| Operation | Command                                      |
|-----------|----------------------------------------------|
| Enable    | `sudo systemctl enable sidera`              |
| Disable   | `sudo systemctl disable sidera`             |
| Start     | `sudo systemctl start sidera`               |
| Stop      | `sudo systemctl stop sidera`                |
| Restart   | `sudo systemctl restart sidera`             |
| Logs      | `sudo journalctl -u sidera --output cat -e` |
| New Logs  | `sudo journalctl -u sidera --output cat -f` |
