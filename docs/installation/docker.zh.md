---
icon: material/docker
---

# Docker

## :material-console: 命令

```bash
docker run -d \
  -v /etc/sidera:/etc/sidera/ \
  --name=sidera \
  --restart=always \
  ghcr.io/miku0139oao/sidera \
  -D /var/lib/sidera \
  -C /etc/sidera/ run
```

## :material-box-shadow: Compose

```yaml
version: "3.8"
services:
  sidera:
    image: ghcr.io/miku0139oao/sidera
    container_name: sidera
    restart: always
    volumes:
      - /etc/sidera:/etc/sidera/
    command: -D /var/lib/sidera -C /etc/sidera/ run
```
