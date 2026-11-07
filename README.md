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

## Server

The server runs in Docker next to restic's
[rest-server](https://github.com/restic/rest-server). restash sits in front
of it: it checks each PC's login, keeps each PC in its own repo, and turns
deletes into moves to a trash folder that is emptied after 30 days, so a
broken or hacked PC can't wipe its own backup history.

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
| `RESTASH_REST_SERVER_URL` | `http://rest-server:8000` | Where rest-server is                      |
| `RESTASH_STORAGE`         | `/storage`                | Folder holding the database, repos, trash |
| `RESTASH_DB`              | `$RESTASH_STORAGE/restash.db` | SQLite database                       |
| `RESTASH_TRASH_DAYS`      | `30`                      | Days deleted files stay in the trash      |

People join by invite link (People → Invite someone). restash doesn't send
email; you send the link yourself.

## Agent

Open **Add a PC** on the website for a one-time code, then on that PC:

```sh
restash-agent join -server https://restash.example.com K7Q2MX
```

It asks for your account password, unlocks the PC's key and creates its
repo. Then:

```sh
restash-agent run                    # stay linked and back up entries on schedule
restash-agent entries                # list entries and when they run next
restash-agent backup -exclude node_modules ~/Documents ~/Projects
restash-agent snapshots
restash-agent restore -target ~/restored latest
restash-agent forget -tag entry:manual -keep-daily 7 -keep-monthly 12
```

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
`<config dir>/restash/agent.json` (override with `RESTASH_CONFIG`), readable
only by the current user. `RESTASH_ACCOUNT_PASSWORD` skips the password
prompt, for scripts.

## Building

```sh
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
