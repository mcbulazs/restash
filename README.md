# restash

Backs up folders on Linux, Windows and macOS PCs to a home server, using
[restic](https://restic.net). Backups are encrypted on the PC; the server
stores them but can't read them.

Early days: right now there is a server and an agent you run by hand.

## Server

The server runs in Docker next to restic's
[rest-server](https://github.com/restic/rest-server). restash sits in front
of it and turns deletes into moves to a trash folder that is emptied after 30
days, so a broken or hacked PC can't wipe its own backup history.

See [compose.yaml](compose.yaml) for setup. In short:

```sh
mkdir -p storage/repos storage/trash storage/auth
chown -R 65532:65532 storage
docker compose up -d
docker compose exec rest-server create_user anna
```

| Variable                  | Default                   | Meaning                                    |
| ------------------------- | ------------------------- | ------------------------------------------ |
| `RESTASH_LISTEN`          | `:8080`                   | Address to listen on                       |
| `RESTASH_REST_SERVER_URL` | `http://rest-server:8000` | Where rest-server is                       |
| `RESTASH_STORAGE`         | `/storage`                | Folder holding `repos/` and `trash/`       |
| `RESTASH_TRASH_DAYS`      | `30`                      | Days deleted files stay in the trash       |

restic talks to the server at `https://<server>/repo/<username>/`.

## Agent

The agent needs `restic` on the PATH (or next to it, or set
`RESTASH_RESTIC`).

```sh
# Set up this PC. Prints the repo key: keep it safe.
restash-agent init -server https://restash.example.com -user anna

# Back up, list, restore, clean up
restash-agent backup -exclude node_modules ~/Documents ~/Projects
restash-agent snapshots
restash-agent restore -target ~/restored latest
restash-agent forget -tag entry:manual -keep-daily 7 -keep-monthly 12

# On a new PC, connect to the existing repo with its key
restash-agent init -server https://restash.example.com -user anna -key <key>
```

The password can also be given as `RESTASH_PASSWORD`, and the key as
`RESTASH_KEY`. Settings are saved to `<config dir>/restash/agent.json`
(override with `RESTASH_CONFIG`), readable only by the current user.

## Building

```sh
go test ./...
go build ./cmd/restash-server
go build ./cmd/restash-agent
```

Docker images are built by GitHub Actions and pushed to
`ghcr.io/mcbulazs/restash`. Release tags also attach agent binaries for all
three OSes to the GitHub release. Both come with build provenance:

```sh
gh attestation verify oci://ghcr.io/mcbulazs/restash:latest -R mcbulazs/restash
```
