# matrix-fchat

A Matrix puppeting bridge for [F-Chat](https://wiki.f-list.net/F-Chat_Protocol), the chat of F-List,
built on [mautrix-go bridgev2](https://pkg.go.dev/maunium.net/go/mautrix/bridgev2).

## Features

| F-Chat                                          | Matrix                                                                   |
|-------------------------------------------------|--------------------------------------------------------------------------|
| Public channels and private rooms (ADH-…)       | One Matrix room per channel and login                                    |
| Private messages (PRI)                          | DM rooms, started with `start-chat <character>`                          |
| BBCode (b, i, u, s, sup, sub, color, url, user, icon, eicon, spoiler, noparse, session) | HTML formatting, in both directions           |
| `/me` actions                                   | `m.emote`                                                                |
| Roleplay ads (LRP)                              | Message with an `[Ad]` prefix; send with `ad <text>`                     |
| Dice rolls / bottle spins (RLL)                 | Notice; send with `roll 1d20` or `bottle`                                |
| Typing status (TPN) in DMs                      | Typing notifications (both directions)                                   |
| Channel description (CDS)                       | Room topic (both directions; changing it requires channel op)            |
| Channel ops / owner (COL, COA, COR, CSO)        | Power level 50 / 75                                                      |
| Kick / ban / timeout (CKU, CBU, CTU)            | Kick / ban in Matrix (both directions for kick, ban, unban, invite)      |
| Member list (ICH, JCH, LCH, FLN)                | Room members (up to `member_sync_limit`)                                 |
| Invites, system messages, broadcasts, notes     | Notices in the management room                                           |
| Ignore list                                     | Messages from ignored characters are dropped                             |
| Friends / bookmarks (FRL)                       | Contact list; search covers all online characters                        |
| Avatars                                         | Ghost avatars from static.f-list.net                                     |
| Files from Matrix                               | Sent as a link if `public_media` is enabled in the bridge config         |

Several characters (even from the same F-List account) can be logged in at the same time –
just run `login` once per character. API tickets are shared per account, because requesting a
new ticket invalidates all previous ones.

### Not possible (protocol limitations)

F-Chat messages have no IDs. Because of that, there are **no edits, deletions, reactions,
replies or threads**, and no message history (backfill). The server doesn't confirm successful
messages; errors (such as flood control or "character not found") are matched to the sent
message using a short time window (`error_wait_ms`).

## Building

Requires Go ≥ 1.26.

```sh
./build.sh
```

The script builds with `-tags goolm` (a pure Go implementation of Olm), so `libolm` isn't needed.

## Setup

1. Generate the example config and adjust it:
   ```sh
   ./matrix-fchat -e
   ```
   In `config.yaml`, set at least `homeserver.address`, `homeserver.domain`, `appservice.address`,
   `database` and `bridge.permissions`. Recommended:
   ```yaml
   bridge:
       # F-Chat doesn't remember channels permanently – leaving the room = leaving the channel
       bridge_matrix_leave: true
   ```
   The F-Chat specific settings are under `network:` (server URLs, `member_sync_limit`, `bridge_ads`, etc.).
2. Generate the registration and add it to your homeserver
   (see the [mautrix docs](https://docs.mau.fi/bridges/general/registering-appservices.html)):
   ```sh
   ./matrix-fchat -g
   ```
3. Start the bridge: `./matrix-fchat`

## Docker

Build the image:

```sh
docker build -t matrix-fchat .
```

All bridge files (config, registration, SQLite database) live in the `/data` volume.

1. Run the container once to generate `config.yaml`. The appservice `hostname` is set to `0.0.0.0`
   automatically, so the homeserver can reach the bridge inside the container:
   ```sh
   docker run --rm -v "$(pwd)/data:/data" matrix-fchat
   ```
2. Edit `data/config.yaml` as described in [Setup](#setup). Use the container's address for
   `appservice.address` (e.g. `http://matrix-fchat:29380` on a shared Docker network). For SQLite, use:
   ```yaml
   database:
       type: sqlite3-fk-wal
       uri: file:/data/matrix-fchat.db?_txlock=immediate
   ```
3. Run it again to generate `data/registration.yaml`, then add that to your homeserver.
4. Start the bridge:
   ```sh
   docker run -d --name matrix-fchat --restart unless-stopped -v "$(pwd)/data:/data" matrix-fchat
   ```

The bridge runs as UID/GID 1337 by default; change this with the `UID` and `GID` environment variables.

## Usage

In the chat with the bridge bot (`@fchatbot:your.domain`):

1. `login` – enter your F-List **account** name and password, then choose a character.
2. `join Frontpage` or `join ADH-…` – join a channel; the Matrix room is created automatically.
3. `start-chat <character>` – start a private conversation.

More commands (in portal rooms, use the `!fchat` prefix):

| Command                        | Description                                                      |
|--------------------------------|------------------------------------------------------------------|
| `join <name/ID>`               | Join a channel                                                   |
| `leave [name/ID]`              | Leave a channel (without an argument: the current room)          |
| `channels [filter]`            | List public channels                                             |
| `rooms [filter]`               | List open private rooms                                          |
| `create-room <title>`          | Create a new private room                                        |
| `status <status> [message]`    | Set your status (online, looking, busy, dnd, idle, away)         |
| `roll <dice>` / `bottle`       | Roll dice / spin the bottle in the current channel               |
| `ad <text>`                    | Post a roleplay ad in the current channel                        |
| `invite <character>`           | Invite a character to the current private room                   |
| `ignore [remove] <character>`  | Edit your ignore list                                            |
| `reconnect`                    | Reconnect (e.g. after "logged in at another location")           |

Outside of portal rooms, commands apply to your default login; in a portal room, they apply to
the character that owns the room.

You can type BBCode directly in messages from Matrix (e.g. `[b]bold[/b]`); formatting from the
Matrix client is converted to BBCode as well.

## Security and F-List policies

- The F-List password is stored **in plain text in the bridge database**, because F-Chat needs a
  new API ticket (valid for 30 minutes) for every connection. Protect the database accordingly.
- F-List asks developers to test clients on the test server first (access via helpdesk ticket) and
  to follow the [Developer Policy](https://wiki.f-list.net/Developer_Policy). The server URL can be
  changed with `network.server_url`.
- The bridge respects the server's flood control (spacing according to `msg_flood`) and doesn't
  reconnect automatically after error codes 2, 9, 30, 31, 33, 39 and 40, as the protocol docs require.

## Layout

```
cmd/matrix-fchat/    Entry point (mxmain.BridgeMain)
Dockerfile           Multi-stage image build; docker-run.sh is its entry point
pkg/fchat/           F-Chat protocol: websocket client, reconnects, pings, flood control, ticket API
pkg/bbcode/          BBCode → HTML/text
pkg/connector/       bridgev2 connector: login, portals, ghosts, event conversion, commands
```

Tests: `go test -tags goolm ./...` (the protocol client is tested against a simulated F-Chat server).
