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
- **Small and self-contained.** One static Go binary plus SQLite. Runs under
  systemd (or Docker) behind Caddy for automatic HTTPS, with auto-deploy from
  GitHub Actions.

## How it works

```
GitHub org webhook ── POST /github/webhook ──────┐
                                                 ▼
Discord /admin ──── POST /discord/interactions ─▶ Caddy (HTTPS) ─▶ pingcord ─▶ Discord channel webhook
                                                                    │
                                                                    ├─ SQLite: links, ignored users, cache
                                                                    └─ GitHub API: org/team members, username → ID
```

## Telegram

pingcord can deliver to Telegram instead of Discord. Set
`NOTIFICATION_PROVIDER=telegram`; the default remains `discord`. One running
instance delivers to one platform. Telegram sends each selected person a private
message and keeps the same GitHub recipient selection, team expansion, watcher
levels, ignores, and bot/self filters.
No GitHub Actions workflow is needed to send notifications.

### Set up the bot

1. Open [@BotFather](https://t.me/BotFather), send `/newbot`, and save the token
   as `TELEGRAM_BOT_TOKEN`. Every team member, including admins, must open a
   private conversation with the bot and press **Start** before it can DM them.
   No Telegram group is needed.
2. Before registering a Telegram webhook, send `/start` privately to your bot,
   then run:

   ```sh
   curl "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getUpdates"
   ```

   Find your `message.from.id` and set `ADMIN_TELEGRAM_IDS` to your positive
   numeric user ID. For multiple admins use a comma-separated list. These are
   user IDs, not usernames. `getUpdates` cannot be used while a Telegram webhook
   is registered; after setup, `/whoami` privately shows each person's user ID.
3. Configure the server with your existing GitHub settings plus:

   ```dotenv
   NOTIFICATION_PROVIDER=telegram
   TELEGRAM_BOT_TOKEN=123456789:your-token
   TELEGRAM_WEBHOOK_SECRET=your-random-secret
   ADMIN_TELEGRAM_IDS=123456789
   DB_PATH=/var/lib/pingcord/pingcord-telegram.db
   ```

   Generate the webhook secret with `openssl rand -hex 32`. For Docker use
   `/data/pingcord-telegram.db`. Discord settings are unnecessary in Telegram
   mode. **Use a new database when switching platforms**: an existing Discord
   database is protected against being reused as Telegram identities. Link users
   and recreate watches in the new database; your Discord database is preserved.
4. Deploy the updated binary and Caddy configuration, then register Telegram's
   webhook (export the variables below in your local shell first):

   ```sh
   curl --fail-with-body -X POST \
     "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/setWebhook" \
     --data-urlencode "url=https://YOUR_DOMAIN/telegram/webhook" \
     --data-urlencode "secret_token=${TELEGRAM_WEBHOOK_SECRET}" \
     --data-urlencode 'allowed_updates=["message"]' \
     --data-urlencode 'max_connections=1'
   ```

   Check that Telegram returns `"ok": true`. The secret authenticates incoming
   Telegram updates. GitHub continues posting to `/github/webhook` with its
   existing GitHub webhook secret and event selections. If using a different
   reverse proxy, expose `/telegram/webhook` alongside `/github/webhook`.
5. Send `/whoami` privately to the bot to verify connectivity. Admins manage
   links and watches in a **private conversation with the bot**:

   ```text
   /admin link 234567890 alice-dev
   /admin unlinked
   /admin list
   /admin watch 234567890 new-prs *
   /admin watch 234567890 all api
   /admin unwatch 234567890 api
   /admin ignore alice-dev
   /admin unignore alice-dev
   /admin unlink 234567890
   ```

   `/admin link` takes a Telegram user ID followed by a GitHub login. Watch levels
   are `new-prs`, `prs`, and `all`; a repository is required (`*` means the whole
   org). `/start` and `/help` show command syntax; `/whoami` shows user and chat
   IDs. Only `ADMIN_TELEGRAM_IDS` can run admin commands. Group admin commands
   are ignored to keep account management private.

Each GitHub event produces a separate private message for each selected linked
user. Each person sees their own notification reasons and the event context.
Users who are not involved receive no GitHub notification. Watchers receive
private messages for the activity they watch. Context from GitHub is escaped;
no group mention settings are involved. Users can still mute or block the bot.

Admin commands, help, and `/whoami` work only in private conversations with the
bot. Audit messages go silently to the configured admins' private chats, not to
all linked users. Unlike Discord, these text commands have no field autocomplete.
`TELEGRAM_CHAT_ID` is no longer used; remove it from existing configurations.
Existing Telegram user links and watches can be kept when upgrading from group
mode; only a switch from Discord requires a separate database.

If a recipient has not started the bot or has blocked it, Telegram rejects their
DM. pingcord continues attempting the other recipients, reports a failed GitHub
delivery, and logs the failed user ID. Ask that person to start or unblock the
bot, then use **Redeliver** in GitHub. Redelivery can duplicate messages for
people who already received them; outbound messages are not an exactly-once
queue. Check Telegram's `getWebhookInfo` for inbound command delivery errors.
Recent command update IDs and results are cached in memory; after a restart, a
retried update may repeat an audit message.

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
| Release published | people @mentioned in the release notes (once, and never for drafts) |
| Anything a **watcher** watches | the watcher (see [Watchers](#watchers)) |

Never pinged: the person who triggered the event, bots (configurable), users an
admin has ignored, and anyone without a link (they show up in `/admin unlinked`).
To keep things bounded, at most 25 @users and 5 @teams are resolved per event,
and only teams in `GITHUB_ORG` count. Deliveries from other orgs are ignored.

## Setup

You need a Linux server, a domain pointing at it, admin rights in your GitHub
org, and "Manage Server" in your Discord server. It takes about 20 minutes.

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
   - **Organization permissions → Members:** Read-only
   - **Repository access:** All repositories. This adds the mandatory
     *Metadata: Read-only*, so `/admin watch` can suggest private repos too. It
     can't read code, issues or anything else. With *Public repositories*
     only, the suggestions include just the public ones.

   Nothing else.

   This is `GITHUB_TOKEN`. Fine-grained tokens expire, and pingcord logs a warning
   14 days before expiry.
2. **Secret.** Run `openssl rand -hex 32`. This is `GITHUB_WEBHOOK_SECRET`.

### 3. Server

Follow [`deploy/README.md`](deploy/README.md). In short: install Caddy on the
host, put the values above in `/etc/pingcord/pingcord.env`, install the
systemd unit, add the deploy secrets to the repo's `production` environment,
and push to `main`. GitHub Actions builds and deploys pingcord from then on.

Check it with `journalctl -u pingcord -f`. You should see
`registered /admin slash commands` and `github token ok`.

Prefer Docker? `cp .env.example .env && docker compose up -d` runs the same
thing on `127.0.0.1:8090`; point your reverse proxy at it with
[`deploy/pingcord.caddy`](deploy/pingcord.caddy).

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
| `/admin watch user level repo` | Make someone a **watcher** (see below) |
| `/admin unwatch user repo` | Stop a watch (`*` = the org-wide one) |

### Watchers

A team lead may want to hear about activity even when nobody mentions them.
`/admin watch` makes a linked user a watcher for one repo (`api` or
`org/api`; it autocompletes from the org's repos) or, with repo `*`, for the
whole org. `repo` is required so an org-wide watch is never created by
accident.

| Level | Pinged for |
|---|---|
| `new-prs` | PRs opened, or marked ready for review |
| `prs` | the above, plus PRs merged / closed / reopened and every submitted review |
| `all` | the above, plus comments, issues opened/closed/reopened, discussions, published releases |

A person can have one org-wide watch and any number of per-repo watches. For
each event the highest applicable level counts, so `new-prs` org-wide plus
`all` on `api` means everything in `api` and only new PRs elsewhere. Draft
PRs, pushes, edits, labels, inline review comments and bot activity never
notify watchers, and nobody is notified about their own actions. Unlinking
someone removes their watches.

## Configuration

All configuration is environment variables. pingcord validates everything at
startup and lists every problem it finds.

| Variable | Required | Description |
|---|:-:|---|
| `GITHUB_WEBHOOK_SECRET` | ✓ | Shared secret for webhook signatures (≥ 16 chars) |
| `GITHUB_TOKEN` | ✓ | Fine-grained PAT with org *Members: Read-only*, all repositories (*Metadata: Read-only*) |
| `GITHUB_ORG` | ✓ | Organization login |
| `NOTIFICATION_PROVIDER` | | `discord` (default) or `telegram` |
| `TELEGRAM_BOT_TOKEN` | Telegram | BotFather bot token |
| `TELEGRAM_WEBHOOK_SECRET` | Telegram | 16–256 letters, digits, underscores or hyphens |
| `ADMIN_TELEGRAM_IDS` | Telegram | Comma-separated positive admin user IDs; receive silent audit DMs |
| `DISCORD_WEBHOOK_URL` | Discord | Channel webhook for notifications |
| `DISCORD_APP_ID` | Discord | Discord application ID |
| `DISCORD_PUBLIC_KEY` | Discord | Application public key (verifies slash-command requests) |
| `DISCORD_BOT_TOKEN` | Discord | Used only to register `/admin` |
| `DISCORD_GUILD_ID` | Discord | Your Discord server ID |
| `ADMIN_DISCORD_IDS` | Discord | Comma-separated Discord user IDs allowed to use `/admin` |
| `LISTEN_ADDR` | | Listen address, e.g. `127.0.0.1:8090` (overrides `PORT`; set by the systemd unit) |
| `PORT` | | Listen port on all interfaces (default `8080`) |
| `DB_PATH` | | SQLite path (default `/data/pingcord.db`; the systemd unit uses `/var/lib/pingcord/pingcord.db`) |
| `IGNORE_BOTS` | | Skip bot actors and bot recipients (default `true`) |
| `LOG_LEVEL` | | `debug`, `info` (default), `warn`, `error` |
| `GITHUB_API_URL` | | GitHub Enterprise Server: `https://HOST/api/v3` (https required) |

## Security

- **GitHub requests:** every webhook must carry a valid HMAC-SHA256 signature.
- **Discord requests:** every interaction must carry a valid Ed25519 signature
  with a timestamp within 5 minutes.
- **Who can be pinged:** `allowed_mentions` is always set, so a message can only
  ping the users it lists, never `@everyone` or roles. Text quoted from GitHub is
  defanged and can't ping anyone.
- **GitHub token:** read-only: org members plus repository names
  (*Metadata*), no code. The webhook URL and
  tokens never appear in logs or errors.
- **Process isolation:** under systemd it runs as a throwaway unprivileged
  user that can only write its own state directory, listening on loopback
  only. The Docker image is distroless and non-root, with a read-only root
  filesystem and all capabilities dropped.
- **Deploy secrets:** SSH credentials live in a GitHub environment's
  encrypted secrets, and the deploy user can run exactly two root commands.

Found a vulnerability? Please open a private security advisory on GitHub rather
than a public issue.

## Troubleshooting

| Symptom | Check |
|---|---|
| Discord won't save the Interactions Endpoint URL | Is the domain reachable over HTTPS? Is `DISCORD_PUBLIC_KEY` correct? Check `journalctl -u caddy` |
| `/admin` doesn't appear | Logs should show `registered /admin slash commands`; the app must be invited with `applications.commands`; you need *Manage Server* |
| GitHub deliveries show 401 | `GITHUB_WEBHOOK_SECRET` doesn't match the webhook's secret |
| GitHub deliveries show 415 | Set the webhook's content type to `application/json` |
| GitHub deliveries show 502 | Discord rejected the message, or a requested review team couldn't be looked up; see the logs, fix it, then click *Redeliver* in GitHub |
| GitHub deliveries show 503 | Too many deliveries arriving at once; click *Redeliver* |
| `/admin watch` doesn't suggest private repos | Logs show `github repos visible` with a count. Give the token *Repository access: All repositories*, then restart pingcord |
| Someone isn't pinged | `/admin unlinked`; logs list `unlinked` logins for each event |
| `GITHUB_TOKEN cannot read org members` | The token's resource owner must be the org, with *Members: Read-only*; org owners may need to approve fine-grained tokens |

## Development

```sh
make test         # go test -race ./...
make cover-check  # coverage, fails under 80%
make lint         # golangci-lint
make build        # ./bin/pingcord
```

Requires Go 1.26+ (`go.mod` pins the patched toolchain). See [`PLAN.md`](PLAN.md) for the design.

## License

[MIT](LICENSE)
