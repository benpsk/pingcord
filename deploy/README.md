# Deploying pingcord

pingcord runs as a single binary under systemd, behind the server's **host
Caddy**. Every push to `main` makes GitHub Actions run the tests, build the
binary, copy it to the server and restart it. Nothing is built on the server.

```
push to main ─► GitHub Actions: CI ─► build ─► scp ─► install.sh on the server
                                                        │ swap binary, health check (rollback on failure)
                                                        └ /etc/caddy/sites/pingcord.caddy, reload Caddy

https://PINGCORD_DOMAIN ─► Caddy (host, :80 :443) ─► 127.0.0.1:8090 pingcord ─► SQLite /var/lib/pingcord
```

| File | What |
|---|---|
| `pingcord.service` | systemd unit: loopback only, sandboxed, `DynamicUser` |
| `pingcord.caddy` | Caddy site; the workflow fills in the domain |
| `install.sh` | Runs on the server during each deploy |
| `../.github/workflows/deploy.yml` | CI → build → deploy |

## 1. Host Caddy (once per server)

One Caddy on the host owns ports 80/443 and serves every app on the server.
Each app drops its own site file into `/etc/caddy/sites/` and only listens on
`127.0.0.1`, so ports can't collide. On this server shagan does the same
(its `deploy/shagan.caddy`).

```bash
# Install from Caddy's official repository (https://caddyserver.com/docs/install)
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update && sudo apt install -y caddy

# The main config only imports the apps' sites
echo 'import /etc/caddy/sites/*.caddy' | sudo tee /etc/caddy/Caddyfile
sudo mkdir -p /etc/caddy/sites
sudo chown "$DEPLOY_USER" /etc/caddy/sites   # the user the deploys SSH in as
```

> **Moving from a Caddy container** (shagan's old `caddy` service held 80/443):
> the host Caddy can't start until that container is gone. shagan's next
> deploy removes it and then starts the host Caddy, so expect a minute of
> downtime while Caddy fetches fresh certificates.

## 2. The deploy user (once)

The workflow SSHes in as a normal user (`$DEPLOY_USER`, for example the one
shagan already deploys with). It needs to:

```bash
# own the app directory
sudo mkdir -p /opt/pingcord && sudo chown "$DEPLOY_USER" /opt/pingcord

# run exactly these commands as root, nothing else
echo "$DEPLOY_USER ALL=(root) NOPASSWD: /usr/bin/systemctl restart pingcord, /usr/bin/systemctl reload-or-restart caddy" \
  | sudo tee /etc/sudoers.d/pingcord-deploy
sudo chmod 440 /etc/sudoers.d/pingcord-deploy && sudo visudo -c

# read pingcord's logs (printed by a failed deploy)
sudo usermod -aG systemd-journal "$DEPLOY_USER"
```

## 3. Secrets and the service (once)

```bash
sudo mkdir -p /etc/pingcord
sudo vim /etc/pingcord/pingcord.env      # paste ../.env.example, fill it in
sudo chmod 600 /etc/pingcord/pingcord.env

# install the unit (the first deploy starts it)
sudo curl -fsSL https://raw.githubusercontent.com/benpsk/pingcord/main/deploy/pingcord.service \
  -o /etc/systemd/system/pingcord.service
sudo systemctl daemon-reload && sudo systemctl enable pingcord
```

`pingcord.env` holds `GITHUB_*`, `DISCORD_*` and `ADMIN_DISCORD_IDS`. The unit
sets `LISTEN_ADDR` and `DB_PATH`. The root README explains every value and
how to get it.

## 4. DNS

Add an A record for pingcord's domain (for example `pingcord.example.com`)
pointing at the server. It must resolve before the first deploy, because
Caddy fetches the certificate as soon as it loads the site.

## 5. GitHub (once)

1. **Deploy key.** On your machine:
   ```bash
   ssh-keygen -t ed25519 -f pingcord-deploy -N ""
   ```
   Add `pingcord-deploy.pub` to the deploy user's `~/.ssh/authorized_keys`.
2. **Environment.** Go to *Repository → Settings → Environments → New
   environment* and name it **`production`**. Optionally restrict
   *Deployment branches* to `main`.

   **Secrets** (encrypted; never shown, even in a public repo, and never given
   to pull requests from forks):

   | Secret | Value |
   |---|---|
   | `DEPLOY_SSH_HOST` | the server's IP or name |
   | `DEPLOY_SSH_USER` | `$DEPLOY_USER` |
   | `DEPLOY_SSH_KEY` | contents of `pingcord-deploy` (the private key) |
   | `DEPLOY_SSH_KNOWN_HOSTS` | output of `ssh-keyscan <server>` |

   **Variables** (not secret):

   | Variable | Value |
   |---|---|
   | `PINGCORD_DOMAIN` | e.g. `pingcord.example.com` |
   | `DEPLOY_GOARCH` | optional, `arm64` for an ARM server (default `amd64`) |

## 6. First deploy

Push to `main`, or go to *Actions → deploy → Run workflow*. Then finish the
setup in the root README: set the Discord Interactions Endpoint URL, add the
GitHub org webhook, and link your team with `/admin`.

## Every day

| Task | How |
|---|---|
| Deploy | push or merge to `main` |
| Status | `systemctl status pingcord` |
| Logs | `journalctl -u pingcord -f` (Caddy: `journalctl -u caddy -f`) |
| Roll back | re-run an earlier successful *deploy* run in Actions, or on the server: `cd /opt/pingcord && mv pingcord.prev pingcord && sudo systemctl restart pingcord` |
| Back up | copy `/var/lib/pingcord/pingcord.db` (links and ignores; everything else is rebuilt) |
| Change secrets | edit `/etc/pingcord/pingcord.env`, then `sudo systemctl restart pingcord` |
| Unit file changed | the deploy log warns; copy `/opt/pingcord/release/pingcord.service` to `/etc/systemd/system/`, then `daemon-reload` and restart |
