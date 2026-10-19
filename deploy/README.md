# Deploy examples

The M1 text hub is implemented. File storage and TLS are the next milestones.

## Docker

```bash
cd deploy
docker compose up -d
docker compose logs -f

# If proxy.golang.org is blocked:
GOPROXY=https://goproxy.cn,direct docker compose build
```

The compose file binds to `127.0.0.1` by default. For a LAN, change the port
mapping to `<hub-ip>:8787:8787` and add a firewall rule.

## systemd

```bash
sudo useradd --system --home /srv/agentchat --shell /usr/sbin/nologin agentchat
sudo install -d -o agentchat -g agentchat -m 0750 /srv/agentchat
sudo install -m 0644 deploy/agentchat.service /etc/systemd/system/agentchat.service
sudo systemctl daemon-reload
sudo systemctl enable --now agentchat
```

The unit points at `https://github.com/Dofu27/agentchat-lan`.

## Firewall

```bash
sudo ufw allow from 192.168.1.0/24 to any port 8787 proto tcp
sudo ufw deny 8787/tcp
```

Adjust the subnet to your LAN. Never expose the port to the internet.

## Data

- `/srv/agentchat` contains SQLite and the file store.
- Back it up before upgrades.
- Run the service as a dedicated non-root user.
- Do not put the data directory on an SMB share.
