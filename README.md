# restash

Backs up folders on Linux, Windows and macOS PCs to a home server, using
[restic](https://restic.net). Backups are encrypted on the PC; the server
stores them but can't read them.

## How the keys work

- Your account password never leaves your device. Your browser (or the
  agent on a PC) stretches it with PBKDF2 and splits the result into an
  **auth key**, which logs you in (the server keeps only its hash), and a
  **wrap key**, which unlocks your **account key**.
- Each PC's restic key is random and stored on the server **locked with your
  account key**. The agent unlocks it once, when you type your password on
  that PC, and keeps the unlocked copy locally.
- At signup you get a **recovery code**. It locks a second copy of the
  account key, so you can set a new password if you forget the old one.
  There is no other way in: the admin can't reset it for you.
- The admin sees account names, PC names, backup times and sizes. Never file
  contents, names or paths.

The crypto lives in [internal/vault](internal/vault) (Go) and
[web/src/vault.ts](web/src/vault.ts) (browser); both are tested against the
same vectors.

## Entries

A PC backs up one or more **entries**: a folder (or a few) with its own
schedule (every hour, every day, every week, or only by hand), skip
patterns and retention ("every backup for 2 days, then one a day for 30
days, then one a month for 2 years"). They're set up on the PC's page.

- The entry's name, folders, skips and retention are locked with a key
  derived from the PC's key, so only the owner's browser and the PC can
  read them. The server stores only the schedule, so it can tell when a
  backup is late. Error messages from failed runs are locked the same way.
- "Add a folder" asks the PC for its folders over its live link; the
  request and the listing are locked too.
- All entries share the PC's restic repo. Each snapshot is tagged
  `entry:<id>`, and retention runs per tag, so one entry's rules never
  remove another entry's backups. Data freed by retention is pruned at most
  once a day and lands in the server's trash.
- A run missed while the PC was off happens as soon as it's back on.

## Restoring

- **In any browser:** a PC's page has **Restore files**. The page loads a
  small restic reader (Go compiled to WebAssembly, built by
  `scripts/build-wasm.sh`), fetches the encrypted backup from the server
  and decrypts it right there. You can browse, search, and download files
  or a .zip, even if the PC is gone. The server only ever hands out
  encrypted data.
- **Onto a PC:** from the same page, restore next to the originals (copies
  get "(restored Nov 7)" added to their names) or into a folder you pick on
  any of your PCs. The request travels locked to that PC, which does the
  restoring.
- **A PC died:** on the new PC, `restash-agent join` offers to bring back
  another of your PCs' files, or run `restash-agent pull` later. It asks
  for your password, unlocks the old PC's key and restores the newest
  backup of each of its entries into `Restored from <PC>`.

A PC can read (never write) the backups of its owner's other PCs; a
logged-in browser can read its owner's backups. Writing stays limited to
each PC's own repo.

## Alerts and status

The server checks every few minutes whether each entry is **failed** (its
last run failed), **late** (a scheduled run is over an hour overdue while
the PC was on) or whether a PC is **quiet** (no good backup for 3 days).
Problems show on the PC list, People and **Activity**, which also lists
backups, failures, restores, downloads and logins.

Alerts to [ntfy](https://ntfy.sh) are off unless `RESTASH_NTFY_SERVER` is
set. With it, each new problem is announced once to the owner's topic, and
the admin's, and recoveries are announced too. New logins from an unknown
browser are announced the same way, and a weekly summary can be turned on.
People set their topic and what they want under Activity. Without it, a
monitoring system can alert from the metrics below instead.

Alerts name PCs, never entries or files: the server can't read those.

`RESTASH_METRICS_LISTEN` (e.g. `:9464`) serves Prometheus metrics per PC:
online, last good backup, stored size and problems. Keep it off the
public site.

## Server

The server is one Docker container. It serves the restic repos itself, with
restic's own [rest-server](https://github.com/restic/rest-server) code built
in, and checks every request first: each PC's login, each PC in its own
repo, and deletes turned into moves to a trash folder that is emptied after
30 days, so a broken or hacked PC can't wipe its own backup history.

See [compose.yaml](compose.yaml). In short:

```sh
mkdir -p storage/repos storage/trash
chown -R 65532:65532 storage
docker compose up -d
docker compose logs restash | grep signup   # link for the admin account
```

| Variable                  | Default                   | Meaning                                   |
| ------------------------- | ------------------------- | ----------------------------------------- |
| `RESTASH_PUBLIC_URL`      | `http://localhost:8080`   | The site's address, used in links         |
| `RESTASH_LISTEN`          | `:8080`                   | Address to listen on                      |
| `RESTASH_STORAGE`         | `/storage`                | Folder holding the database, repos, trash |
| `RESTASH_DB`              | `$RESTASH_STORAGE/restash.db` | SQLite database                       |
| `RESTASH_TRASH_DAYS`      | `30`                      | Days deleted files stay in the trash      |
| `RESTASH_NTFY_SERVER`     | (off)                     | ntfy server for alerts, e.g. `https://ntfy.sh` |
| `RESTASH_METRICS_LISTEN`  | (off)                     | Address for Prometheus metrics            |
| `RESTASH_AGENT_SOURCE`    | this version's GitHub release | Where "Add a PC" downloads come from (a release URL ending in `/`, or a folder) |

People join by invite link (People → Invite someone). restash doesn't send
email; you send the link yourself.

The admin can give each person a storage limit (People → the size under
Stored). It covers all of that person's PCs, counts the encrypted backups
as they sit on the server (not the trash), and shows on their Your PCs
page. Over the limit, their PCs don't start new backups; they record a
failed run saying why and clean up old backups right away, only deleting
data nothing uses so the cleanup itself needs no room. As a backstop the
server refuses new backup data once someone is 10% past their limit.

## Agent

On the PC, open **Add a PC** on the website and download restash. The file
is named for a one-time code (`restash-setup-K7Q2MX.exe`), so opening it is
all it takes: it links the PC to the account, asks for the account password
(checked on the PC, never sent), and installs itself to run in the
background and start with the PC:

| OS | Runs as | Agent | Log |
| --- | --- | --- | --- |
| Linux | systemd user service, with lingering so it also runs after a reboot | `~/.local/share/restash/restash-agent` | `journalctl --user -u restash-agent` |
| macOS | launchd agent while you're logged in | `~/Library/Application Support/restash/restash-agent` | `~/Library/Logs/restash-agent.log` |
| Windows | Windows service (asks for administrator once) | `%ProgramFiles%\restash\restash-agent.exe` | `%ProgramData%\restash\agent.log` |

On Linux and macOS it runs as you, so restored files are yours. On Windows
the service runs as SYSTEM; its settings are in `%ProgramData%\restash`,
open only to administrators. The builds are unsigned: on Windows click
"More info" → "Run anyway"; on macOS use "Open Anyway" in System Settings →
Privacy & Security, and give the agent Full Disk Access to back up folders
macOS keeps private (setup prints the path).

If the file got renamed, setup asks for the code. With the agent already
there, `restash-agent setup -server https://restash.example.com K7Q2MX`
does the same.

The service updates itself from this repo's GitHub releases (never from the
restash server): it checks every 6 hours, checks the download against the
release's `SHA256SUMS`, waits for running backups and restores, and
restarts. Development builds don't update; `RESTASH_NO_UPDATE=1` turns it
off.

By hand:

```sh
restash-agent status                 # linked? unlocked? service running?
restash-agent join -server https://restash.example.com K7Q2MX
restash-agent install                # run in the background (uninstall to stop)
restash-agent run                    # or stay in the foreground instead
restash-agent entries                # list entries and when they run next
restash-agent pull                   # bring back another PC's files
restash-agent backup -exclude node_modules ~/Documents ~/Projects
restash-agent snapshots
restash-agent restore -target ~/restored latest
restash-agent forget -tag entry:manual -keep-daily 7 -keep-monthly 12
```

Setup files link to `https://restash.bulazs.com` unless built with
`-ldflags "-X main.defaultServer=https://restash.example.com"`
(`RESTASH_SERVER` overrides it at run time).

Release builds of the agent carry the official restic release inside
(pinned and checked in [scripts/restic.sha256](scripts/restic.sha256)) and
unpack it to the user's cache folder on first use. Builds without it look
for `restic` next to the agent or on the PATH; `RESTASH_RESTIC` overrides
both. To build one with restic inside:

```sh
scripts/fetch-restic.sh linux amd64
go build -tags restic_bundled ./cmd/restash-agent
```

The agent's settings are saved to
`<config dir>/restash/agent.json` (`%ProgramData%\restash\agent.json` on
Windows; override with `RESTASH_CONFIG`), readable only by the current user. `RESTASH_ACCOUNT_PASSWORD` skips the password
prompt, for scripts.

## Building

```sh
scripts/build-wasm.sh
(cd web && npm ci && npm test && npm run build)
go test ./...
go build ./cmd/restash-server
go build ./cmd/restash-agent
```

For UI work, run `npm run watch` in `web/` and start the server with
`RESTASH_WEB_DIR=web/dist`.

Docker images are built by GitHub Actions and pushed to
`ghcr.io/mcbulazs/restash`. Release tags also attach agent binaries for all
three OSes to the GitHub release. Both come with build provenance:

```sh
gh attestation verify oci://ghcr.io/mcbulazs/restash:latest -R mcbulazs/restash
```
