# matrix-fchat

A Matrix puppeting bridge for [F-Chat](https://wiki.f-list.net/F-Chat_Protocol), the chat of F-List,
built on [mautrix-go bridgev2](https://pkg.go.dev/maunium.net/go/mautrix/bridgev2).

## Features

| F-Chat                                          | Matrix                                                                   |
|-------------------------------------------------|--------------------------------------------------------------------------|
| Public channels and private rooms (ADH-…)       | One Matrix room per channel and login                                    |
| Private messages (PRI)                          | DM rooms, started with `start-chat <character>`; messages to offline characters are sent when they come online (`queue_offline_messages`), and the notice about it is edited once they're sent |
| BBCode (b, i, u, s, sup, sub, color, url, user, icon, eicon, spoiler, noparse, session) | HTML formatting, in both directions           |
| `[eicon]` and emoticons (`:heart:`, `:lif-smile:`, …) | Inline images (uploaded to Matrix once and cached); messages with only eicons are sent as one sticker (several eicons are combined into one image), so animated eicons play |
| `[icon]`                                        | Avatar image linked to the F-List profile, then 💬 (opens the private chat) |
| `[user]` names                                  | Name in the F-Chat gender color linked to the F-List profile, then 💬 (opens the private chat) |
| Character profiles                              | Ghost biography (profile summary + link to the F-List profile), pronouns and name color (see [Profile fields](#profile-fields)) |
| `/me` actions                                   | `m.emote`                                                                |
| Roleplay ads (LRP)                              | Quoted message with an `[Ad]` prefix; send with `ad <text>`, or every N minutes with `auto-ad`; turn on/off per character or room with `ads` |
| Character search (FKS)                          | `find-partners` command; `find-matches` shows the 5 online characters whose profiles match yours best |
| Dice rolls / bottle spins (RLL)                 | Notice; send with `roll 1d20` or `bottle`                                |
| Typing status (TPN) in DMs                      | Typing notifications (both directions)                                   |
| Channel description (CDS)                       | Room topic (both directions; changing it requires channel op)            |
| Channel ops / owner (COL, COA, COR, CSO)        | Power level 50 / 75; change with `op`, `deop`, `set-owner`               |
| Kick / ban / timeout (CKU, CBU, CUB, CTU, CBL)  | Kick / ban in Matrix (both directions for kick, ban, unban, invite); `kick`, `ban`, `unban`, `timeout`, `banlist` commands |
| Channel mode / room visibility (RMO, RST)       | Notice; change with `channel-mode` and `room-visibility`                 |
| Member list (ICH, JCH, LCH, FLN)                | Room members (up to `member_sync_limit`)                                 |
| Invites, system messages, notes                 | Notices in the management room                                           |
| Broadcasts (BRO)                                | Notices in a private chat with the "F-Chat Broadcast" ghost              |
| Ignore list                                     | Messages from ignored characters are dropped                             |
| Friends                                         | A DM room per friend (`create_friend_dms`), contact list, `friends` command; send, accept, deny and cancel requests and remove friends with `friend` and `friend-requests` |
| Bookmarks                                       | Contact list; `bookmarks` lists them with avatars, gender colors and online status; `bookmark add/remove` |
| Characters coming online                        | Optional notice for friends and bookmarks (`online_alerts`, `online-alerts` command), and for any character you turn it on for in the private chat |
| Character memos                                 | `memo` command; shown in `profile`                                       |
| Idle status                                     | Set automatically after `auto_idle_minutes` without activity on Matrix   |
| Avatars                                         | Ghost avatars from static.f-list.net                                     |
| Files from Matrix                               | Sent as a link if `public_media` is enabled in the bridge config         |

Several characters (even from the same F-List account) can be logged in at the same time –
just run `login` once per character. API tickets are shared per account, because requesting a
new ticket invalidates all previous ones.

### Not possible (protocol limitations)

F-Chat messages have no IDs. Because of that, there are **no edits, deletions, reactions,
replies or threads**, and no message history (backfill). The server doesn't confirm successful
messages; errors (such as flood control or "character not found") are matched to the sent
message using a short time window (`error_wait_ms`). The "channel not found" error (26) doesn't
name the channel either, so the bridge lists the joins the server didn't confirm; channels that
are gone for sure aren't rejoined anymore.

Link previews: links to F-List are shown in inline code and ads as quotes, because Element doesn't
generate previews there. Links that open chats and channels are in inline code as well. Messages also carry an empty `com.beeper.linkpreviews` list (MSC4095)
when they only contain F-List links (and always for ads), which clients that support it respect.

Element only plays animated images (eicons) in image and sticker events, never in formatted text.
That's why messages that only contain eicons are sent as a sticker (Element plays it on hover, or
always with "Autoplay GIFs" enabled), while eicons mixed with text stay still images in Element.
A sticker holds one image, so several eicons are combined into one image: an animated GIF if one
of them is animated (GIFs have no semi-transparency, so soft edges look a bit rougher).

Matrix clients choose the colors of member names themselves, so room members can't get their
F-Chat gender colors. Names in lists and `[user]` tags in messages are colored instead.

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

### Profile fields

Ghosts get custom Matrix profile fields: a biography with a short summary of the F-List profile
(gender, species, age, orientation, role, "currently looking for") and a link to the profile,
pronouns (Male: he/him, Female: she/her, other genders: they/them) and a name color in the
F-Chat gender color. This needs a homeserver with custom profile fields (Matrix v1.16 / MSC4133;
in Synapse `experimental_features: msc4133_enabled: true`) and, in the bridge config:

```yaml
matrix:
    ghost_extra_profile_info: true
```

There are no stable field names yet, so the bridge sets the keys of the proposals
(`m.biography`/`gay.fomx.biography`, `m.pronouns`/`io.fsky.nyx.pronouns`) and the keys used by
Sable and Commet (`moe.sable.app.bio`, `chat.commet.profile_bio`, `moe.sable.app.name_color`).
Element doesn't show these fields yet. Profiles are loaded from F-List at most once a week per
character (`network: fetch_character_profiles`).

mautrix normally also adds `com.beeper.bridge.*` fields (remote ID, identifiers, network) to
every ghost profile, which only Beeper clients use. The bridge leaves them out and removes
existing ones, unless `network: beeper_profile_fields` is enabled.

### Offline characters and ghost cleanup

Characters that go offline aren't removed from the channel rooms. Their ghost gets the Matrix
presence "offline" instead (`network: bridge_presence`), and, for homeservers with presence
disabled, optionally a suffix on its name (`network: offline_name_suffix`, e.g. `" (offline)"`).
When a character comes back online without rejoining a channel within two minutes, it leaves the
room. Synapse resets the presence of users that don't sync after 30 seconds, so the bridge sets the
presence of online channel members and private chat partners again every
`network: presence_refresh_seconds`.

With `network: ghost_cleanup_days` set, characters that were offline for that many days leave the
channel rooms (private chats are kept), and ghosts that weren't seen for that many days and that
nobody has a private chat with are removed: they leave all rooms and their Matrix profile is
cleared (an appservice can't delete Matrix accounts). They're restored automatically when the
character shows up again. The cleanup runs once an hour, only while all logins are connected.
Without `ghost_cleanup_days`, offline characters stay in the channel rooms.

### Direct media access

By default, avatars, eicons, emoticons and combined eicon stickers are downloaded from F-List and
uploaded to your homeserver. With `direct_media` in `config.yaml`, the bridge serves them itself
instead: it gives them `mxc://` URIs on its own media server, and the homeserver fetches them from
the bridge (which gets them from F-List) only when someone opens them. Avatars of characters you
never look at aren't downloaded at all.

```yaml
direct_media:
    enabled: true
    server_name: fchat-media.example.com
```

The homeserver reaches that server name over federation, so the domain must be proxied to the
bridge's appservice port (29380 by default) over HTTPS. The bridge answers
`/.well-known/matrix/server` itself (with `server_name:443`, change it with `well_known_response`).
An nginx example, which only passes on the paths the media server uses:

```nginx
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name fchat-media.example.com;

    ssl_certificate     /etc/letsencrypt/live/fchat-media.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/fchat-media.example.com/privkey.pem;

    # Media downloads, server keys and the .well-known delegation.
    location ~ ^/(_matrix/federation/|_matrix/key/|_matrix/client/v1/media/|\.well-known/matrix/server$) {
        proxy_pass http://127.0.0.1:29380;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        # Stream images instead of buffering them.
        proxy_buffering off;
    }

    location / {
        return 404;
    }
}
```

With Docker, use the container's address (e.g. `http://matrix-fchat:29380`) in `proxy_pass`.
Check the setup with the [federation tester](https://federationtester.matrix.org/) (enter the
media server name).

Images that were uploaded before stay as they are; avatars switch to direct media with their next
weekly refresh. Combinations of very many eicons whose URI would be too long are still uploaded.

### Join and chat links

The `channels`, `rooms`, `friends` and `bookmarks` lists (and `[user]` names in messages) contain
links that join a channel or open a private chat with one click. They're room aliases
(`#fchat_open_…`) that the bridge creates when the homeserver asks for them, so the registration
needs an alias namespace. The bridge adds it to `registration.yaml` automatically on startup (and
logs a warning when it did); **restart the homeserver afterwards** so it reads the new registration:

```yaml
namespaces:
    aliases:
        - regex: ^#fchat_open_[a-z0-9_]+:your\.domain$
          exclusive: true
```

Without the namespace, the links show an "alias not found" error; the `join` and `start-chat`
commands still work.

The links are matrix.to links, so they work in every client. Element Web shows them as a pill
with the room's name (or the short alias, if you're not in that room yet) instead of the link
text, so the names in the lists are shown next to the links, not as their text. The aliases only
contain a short token; the bridge stores what it stands for in its database.

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
3. Run it again to generate `data/registration.yaml`.
4. Start the bridge (it adds the [alias namespace](#join-and-chat-links) to the registration on
   the first start), then add `data/registration.yaml` to your homeserver:
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
| `channels [filter]`            | List public channels (click a name to join)                      |
| `rooms [filter]`               | List open private rooms (click a name to join)                   |
| `bookmarks [filter] [number]`  | List your bookmarks (10 by default, `*` = no filter, e.g. `bookmarks * 30`) |
| `friends [filter] [number]`    | List your character's friends, like `bookmarks`                  |
| `create-room <title>`          | Create a new private room                                        |
| `status <status> [message]`    | Set your status (online, looking, busy, dnd, idle, away)         |
| `roll <dice>` / `bottle`       | Roll dice / spin the bottle in the current channel               |
| `ad <text>`                    | Post a roleplay ad in the current channel                        |
| `auto-ad [<minutes> <text>\|stop]` | Post an ad now and then every N minutes (at least 10) for 3 hours; without arguments: list the running ads |
| `ads [room\|character] [on\|off\|default]` | Show or hide ads: in a channel room for that room, elsewhere for the character (without arguments: show the settings) |
| `find-partners kinks: … [; genders: …; limit: N]` | Find online characters (without arguments: help and the possible values) |
| `find-matches` (`matches`)     | The 5 online characters whose profiles match your character's best (takes about a minute, up to 3 when F-Chat or F-List limit requests; the bridge waits and tries again). Candidates come from character searches for your favorite kinks among everyone online; kinks, gender and orientation, language, furry preference, dom/sub role and position are compared |
| `profile [character]`                           | Overview of an F-List profile: status, all filled-in profile fields (general, RP preferences, sexual and contact details), kinks, kink match with your character, your memo and the start of the description. Works in any room and is only visible to you; in a private chat the character is optional |
| `memo [character] [= text]`    | Show, set or (with an empty text) delete your private memo about a character |
| `friend <add\|remove\|accept\|deny\|cancel> [character]` | Friend requests and friendships                          |
| `friend-requests`              | List incoming and sent friend requests                           |
| `bookmark <add\|remove> [character]` | Add or remove a bookmark                                  |
| `online-alerts [character\|chat\|for <character>] [on\|off\|default]` | Notices when characters come online: for friends and bookmarks per character, or for one character per private chat (also for non-friends; `off` silences a friend). Without a scope, a private chat changes that chat |
| `server-info`                  | Characters online and server uptime                              |
| `ignore [remove] <character>`  | Edit your ignore list                                            |
| `reconnect`                    | Reconnect (e.g. after "logged in at another location")           |

Commands that take a character also work in the private chat with that character without it
(`profile`, `memo`, `friend`, `bookmark`).

Channel and room administration (in a channel room; most need channel op or owner rights on F-Chat):

| Command                        | Description                                                      |
|--------------------------------|------------------------------------------------------------------|
| `invite <character>`           | Invite a character to the current private room                   |
| `kick`, `ban`, `unban <character>` | Channel moderation (channel ops)                             |
| `timeout <minutes> <character>` | Time out a character for 1 to 90 minutes (channel ops)          |
| `banlist`                      | Show the channel's ban list (channel ops)                        |
| `op`, `deop`, `set-owner <character>` | Change channel ops and the owner (channel owner)          |
| `channel-mode <chat\|ads\|both>` | Which messages the channel accepts (channel owner)             |
| `room-visibility <public\|private>` | Open the private room to everyone or make it invite-only (room owner) |

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
