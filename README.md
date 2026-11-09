# pingcord

**Ping the right developer in Discord when GitHub needs them, and nobody else.**

pingcord is a small self-hosted server that receives your GitHub organization's
webhooks and posts to one private Discord channel. Each message @pings **only**
the people the event concerns: whoever was @mentioned, asked to review,
assigned, or whose PR just got reviewed or commented on.

The team lead can still watch everything, and everyone else is only pinged when
they're needed.

```
@carol review requested
@dave  mentioned via @acme/backend
┌──────────────────────────────────────────────────┐
│ bob                                              │
│ [acme/api] PR #42: Add login flow                │
│ LGTM, but @carol can you double-check the token  │
│ refresh? cc @acme/backend                        │
│ pull_request_review · submitted                  │
└──────────────────────────────────────────────────┘
```

## Features

- **Pings only the people involved.** Role-based pings (review requested,
  assigned, review or comment on your PR or issue) plus @user and @team
  mentions anywhere: PRs, issues, comments, reviews, discussions, commit
  comments, releases.
- **No config for event types.** The events you tick in GitHub's webhook
  settings decide what's on.
- **Low noise.** Mentions inside code, quotes and PR-template comments are
  ignored. You're never pinged for your own actions. Edits only ping
  newly added mentions, bots are skipped, and each GitHub event produces one
  Discord message.
- **Survives renames.** Links use GitHub's and Discord's permanent numeric IDs,
  so username changes on either side don't break anything.
- **Managed from Discord.** Admin-only `/admin link`, `/admin unlinked` and so
  on, with autocomplete from your org's member list. No redeploys.
- **Small and self-contained.** One static Go binary plus SQLite. Runs in a
  distroless container behind Caddy for automatic HTTPS.

## How it works

```
GitHub org webhook ── POST /github/webhook ──────┐
                                                 ▼
Discord /admin ──── POST /discord/interactions ─▶ Caddy (HTTPS) ─▶ pingcord ─▶ Discord channel webhook
                                                                    │
                                                                    ├─ SQLite: links, ignored users, cache
                                                                    └─ GitHub API: org/team members, username → ID
```

## Who gets pinged

| GitHub activity | Pinged |
|---|---|
| `@alice` in a PR/issue body, comment, review, discussion, commit comment or release notes | alice |
| `@acme/backend` team mention | every member of the team |
| Review requested from a user or team | the reviewer, or every member of the team |
| Assigned to a PR or issue | the assignee |
| Review submitted on your PR | the PR author (approved / changes requested / commented) |
| Comment on your PR, issue or discussion | its author |
| Inline review comment | only people @mentioned in it (the author is pinged once by the review itself) |

Never pinged: the person who triggered the event, bots (configurable), users an
admin has ignored, and anyone without a link (they show up in `/admin unlinked`).

## Setup

You need a server with Docker, a domain pointing at it, admin rights in your
GitHub org, and "Manage Server" in your Discord server. It takes about 15 minutes.

### 1. Discord

1. **Channel webhook.** Create a private channel for the team, then go to
   *Edit Channel → Integrations → Webhooks → New Webhook → Copy Webhook URL*.
   This is `DISCORD_WEBHOOK_URL`.
2. **Application.** In the [Discord Developer Portal](https://discord.com/developers/applications),
   choose *New Application*. From *General Information*, copy the
   **Application ID** (`DISCORD_APP_ID`) and **Public Key** (`DISCORD_PUBLIC_KEY`).
3. **Bot token.** Go to *Bot → Reset Token* and copy it (`DISCORD_BOT_TOKEN`). It's
   only used to register the `/admin` commands.
4. **Invite the app.** Go to *OAuth2 → URL Generator*, choose scopes `bot` and
   `applications.commands` with no bot permissions, open the generated URL and
   add the app to your server.
5. **IDs.** Turn on *User Settings → Advanced → Developer Mode*. Right-click your
   server and choose *Copy Server ID* (`DISCORD_GUILD_ID`). Right-click yourself
   and choose *Copy User ID* (`ADMIN_DISCORD_IDS`).

### 2. GitHub

1. **Token.** Go to *Settings → Developer settings → Fine-grained tokens → Generate new token*:
   - **Resource owner:** your organization
   - **Organization permissions → Members:** Read-only (nothing else)

   This is `GITHUB_TOKEN`. Fine-grained tokens expire, and pingcord logs a warning
   14 days before expiry.
2. **Secret.** Run `openssl rand -hex 32`. This is `GITHUB_WEBHOOK_SECRET`.

### 3. Server

```sh
git clone https://github.com/benpsk/pingcord && cd pingcord
cp .env.example .env     # fill in every value
docker compose up -d
docker compose logs -f pingcord
```

You should see `registered /admin slash commands` and `github token ok`.

### 4. Connect the endpoints

1. **Discord.** In the Developer Portal, go to *General Information → Interactions
   Endpoint URL* and enter `https://YOUR_DOMAIN/discord/interactions`, then save.
   Discord sends a signed test request, and saving only succeeds if pingcord answers it.
2. **GitHub.** In your org, go to *Settings → Webhooks → Add webhook*:
   - **Payload URL:** `https://YOUR_DOMAIN/github/webhook`
   - **Content type:** `application/json`
   - **Secret:** your `GITHUB_WEBHOOK_SECRET`
   - **Events:** *Let me select individual events*, then tick **Issues, Issue
     comments, Pull requests, Pull request reviews, Pull request review
     comments**. Optionally also tick *Discussions, Discussion comments, Commit
     comments, Releases*.

   GitHub sends a `ping` event, which should show a green ✓ under *Recent Deliveries*.

### 5. Link your team

In Discord:

```
/admin unlinked                                  ← who still needs a link
/admin link  user:@Alice  github:alice-dev       ← github autocompletes from your org
```

That's it. The next time someone requests Alice's review, she gets pinged.

## Admin commands

Only members with *Manage Server* can see `/admin`, and pingcord also checks every
request against `ADMIN_DISCORD_IDS`. Replies are only visible to you, and every
change is posted to the channel as an audit line that doesn't ping anyone.

| Command | |
|---|---|
| `/admin link user github` | Link a Discord user to a GitHub account (replaces any existing link on either side) |
| `/admin unlink user` | Remove a link |
| `/admin list` | All links |
| `/admin unlinked` | Org members without a link |
| `/admin ignore github` | Never ping this GitHub user |
| `/admin unignore github` | Undo `ignore` |

## Configuration

All configuration is environment variables. pingcord validates everything at
startup and lists every problem it finds.

| Variable | Required | Description |
|---|:-:|---|
| `GITHUB_WEBHOOK_SECRET` | ✓ | Shared secret for webhook signatures (≥ 16 chars) |
| `GITHUB_TOKEN` | ✓ | Fine-grained PAT with org *Members: Read-only* |
| `GITHUB_ORG` | ✓ | Organization login |
| `DISCORD_WEBHOOK_URL` | ✓ | Channel webhook for notifications |
| `DISCORD_APP_ID` | ✓ | Discord application ID |
| `DISCORD_PUBLIC_KEY` | ✓ | Application public key (verifies slash-command requests) |
| `DISCORD_BOT_TOKEN` | ✓ | Used only to register `/admin` |
| `DISCORD_GUILD_ID` | ✓ | Your Discord server ID |
| `ADMIN_DISCORD_IDS` | ✓ | Comma-separated Discord user IDs allowed to use `/admin` |
| `PINGCORD_DOMAIN` | compose | Domain Caddy serves and gets a certificate for |
| `PORT` | | Listen port (default `8080`) |
| `DB_PATH` | | SQLite path (default `/data/pingcord.db`) |
| `IGNORE_BOTS` | | Skip bot actors and bot recipients (default `true`) |
| `LOG_LEVEL` | | `debug`, `info` (default), `warn`, `error` |
| `GITHUB_API_URL` | | GitHub Enterprise Server: `https://HOST/api/v3` |

### Behind an existing reverse proxy

Remove the `caddy` service from `docker-compose.yml`, publish `127.0.0.1:8080`
from `pingcord`, and proxy `/github/webhook` and `/discord/interactions` to it.
With Caddy on the host:

```
pingcord.example.com {
	@pingcord path /github/webhook /discord/interactions /healthz
	handle @pingcord {
		reverse_proxy 127.0.0.1:8080
	}
	handle {
		respond 404
	}
}
```

## Security

- **GitHub requests:** every webhook must carry a valid HMAC-SHA256 signature.
- **Discord requests:** every interaction must carry a valid Ed25519 signature
  with a timestamp within 5 minutes.
- **Who can be pinged:** `allowed_mentions` is always set, so a message can only
  ping the users it lists, never `@everyone` or roles. Text quoted from GitHub is
  defanged and can't ping anyone.
- **GitHub token:** read-only and limited to member access. The webhook URL and
  tokens never appear in logs or errors.
- **Container:** runs as non-root in a distroless image, with a read-only root
  filesystem and all capabilities dropped.

Found a vulnerability? Please open a private security advisory on GitHub rather
than a public issue.

## Troubleshooting

| Symptom | Check |
|---|---|
| Discord won't save the Interactions Endpoint URL | Is the domain reachable over HTTPS? Is `DISCORD_PUBLIC_KEY` correct? Check `docker compose logs caddy` |
| `/admin` doesn't appear | Logs should show `registered /admin slash commands`; the app must be invited with `applications.commands`; you need *Manage Server* |
| GitHub deliveries show 401 | `GITHUB_WEBHOOK_SECRET` doesn't match the webhook's secret |
| GitHub deliveries show 415 | Set the webhook's content type to `application/json` |
| GitHub deliveries show 502 | Discord rejected the message; see the logs, fix it, then click *Redeliver* in GitHub |
| Someone isn't pinged | `/admin unlinked`; logs list `unlinked` logins for each event |
| `GITHUB_TOKEN cannot read org members` | The token's resource owner must be the org, with *Members: Read-only*; org owners may need to approve fine-grained tokens |

## Development

```sh
make test         # go test -race ./...
make cover-check  # coverage, fails under 80%
make lint         # golangci-lint
make build        # ./bin/pingcord
```

Requires Go 1.25+. See [`PLAN.md`](PLAN.md) for the design.

## License

[MIT](LICENSE)
