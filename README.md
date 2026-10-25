# matrix-fchat

Eine Matrix-Puppeting-Bridge für [F-Chat](https://wiki.f-list.net/F-Chat_Protocol), den Chat von F-List,
gebaut auf [mautrix-go bridgev2](https://pkg.go.dev/maunium.net/go/mautrix/bridgev2).

## Funktionen

| F-Chat                                          | Matrix                                                                   |
|-------------------------------------------------|--------------------------------------------------------------------------|
| Öffentliche Channels und private Räume (ADH-…)  | Ein Matrix-Raum pro Channel und Login                                    |
| Private Nachrichten (PRI)                       | DM-Räume, Start mit `start-chat <Charakter>`                             |
| BBCode (b, i, u, s, sup, sub, color, url, user, icon, eicon, spoiler, noparse, session) | HTML-Formatierung und zurück                  |
| `/me`-Aktionen                                  | `m.emote`                                                                |
| Rollenspiel-Anzeigen (LRP)                      | Nachricht mit `[Ad]`-Präfix; senden mit `ad <Text>`                      |
| Würfeln / Flaschendrehen (RLL)                  | Notice; senden mit `roll 1d20` bzw. `bottle`                             |
| Tipp-Status (TPN) in DMs                        | Typing-Notifications (beide Richtungen)                                  |
| Channel-Beschreibung (CDS)                      | Raum-Topic (beide Richtungen, Ändern nur als Channel-Op)                 |
| Channel-Ops / Owner (COL, COA, COR, CSO)        | Power-Level 50 / 75                                                      |
| Kick / Ban / Timeout (CKU, CBU, CTU)            | Kick / Ban in Matrix (beide Richtungen für Kick, Ban, Unban, Invite)     |
| Mitgliederliste (ICH, JCH, LCH, FLN)            | Raummitglieder (bis `member_sync_limit`)                                 |
| Einladungen, Systemmeldungen, Broadcasts, Notizen | Hinweise im Management-Raum                                            |
| Ignore-Liste                                    | Nachrichten ignorierter Charaktere werden verworfen                      |
| Freundesliste / Lesezeichen (FRL)               | Kontaktliste; Suche über alle Online-Charaktere                          |
| Avatare                                         | Ghost-Avatare von static.f-list.net                                      |
| Dateien aus Matrix                              | Als Link, wenn `public_media` in der Bridge-Konfig aktiviert ist          |

Mehrere Charaktere (auch vom selben F-List-Account) können parallel eingeloggt werden –
einfach mehrmals `login` ausführen. API-Tickets werden pro Account geteilt, weil ein neues
Ticket alle vorherigen ungültig macht.

### Nicht möglich (Protokoll-Grenzen)

F-Chat-Nachrichten haben keine IDs. Deshalb gibt es **keine Edits, Löschungen, Reaktionen,
Antworten oder Threads** und keinen Verlauf (Backfill). Der Server bestätigt erfolgreiche
Nachrichten nicht; Fehler (z. B. Flood-Control oder „Charakter nicht gefunden“) werden über ein
kurzes Zeitfenster (`error_wait_ms`) der gesendeten Nachricht zugeordnet.

## Bauen

Benötigt Go ≥ 1.26.

```sh
./build.sh
```

Das Skript baut mit `-tags goolm` (reine Go-Implementierung von Olm), daher wird `libolm` nicht benötigt.

## Einrichten

1. Beispielkonfiguration erzeugen und anpassen:
   ```sh
   ./matrix-fchat -e
   ```
   In `config.yaml` mindestens `homeserver.address`, `homeserver.domain`, `appservice.address`,
   `database` und `bridge.permissions` setzen. Empfohlen:
   ```yaml
   bridge:
       # F-Chat merkt sich Channels nicht dauerhaft – Raum verlassen = Channel verlassen
       bridge_matrix_leave: true
   ```
   Der F-Chat-spezifische Teil steht unter `network:` (Server-URLs, `member_sync_limit`, `bridge_ads` usw.).
2. Registrierung erzeugen und im Homeserver eintragen
   (siehe [mautrix-Doku](https://docs.mau.fi/bridges/general/registering-appservices.html)):
   ```sh
   ./matrix-fchat -g
   ```
3. Bridge starten: `./matrix-fchat`

## Benutzung

Im Chat mit dem Bridge-Bot (`@fchatbot:deine.domain`):

1. `login` – F-List-**Account**name und Passwort eingeben, dann Charakter wählen.
2. `join Frontpage` bzw. `join ADH-…` – Channel betreten; der Matrix-Raum wird automatisch erstellt.
3. `start-chat <Charakter>` – private Unterhaltung starten.

Weitere Befehle (in Portal-Räumen mit Präfix `!fchat`):

| Befehl                         | Beschreibung                                                     |
|--------------------------------|------------------------------------------------------------------|
| `join <Name/ID>`               | Channel betreten                                                 |
| `leave [Name/ID]`              | Channel verlassen (ohne Argument: aktueller Raum)                |
| `channels [Filter]`            | Öffentliche Channels auflisten                                   |
| `rooms [Filter]`               | Offene private Räume auflisten                                   |
| `create-room <Titel>`          | Neuen privaten Raum anlegen                                      |
| `status <Status> [Nachricht]`  | Status setzen (online, looking, busy, dnd, idle, away)           |
| `roll <Würfel>` / `bottle`     | Würfeln / Flasche drehen im aktuellen Channel                    |
| `ad <Text>`                    | Rollenspiel-Anzeige im aktuellen Channel posten                  |
| `invite <Charakter>`           | In den aktuellen privaten Raum einladen                          |
| `ignore [remove] <Charakter>`  | Ignore-Liste bearbeiten                                          |
| `reconnect`                    | Neu verbinden (z. B. nach „an anderer Stelle eingeloggt“)        |

Außerhalb von Portal-Räumen wirken die Befehle auf den Standard-Login; in einem Portal-Raum
auf den Charakter, dem der Raum gehört.

In Nachrichten aus Matrix kann direkt BBCode geschrieben werden (z. B. `[b]fett[/b]`), Formatierung
aus dem Matrix-Client wird ebenfalls in BBCode umgewandelt.

## Sicherheit und F-List-Richtlinien

- Das F-List-Passwort wird **im Klartext in der Bridge-Datenbank** gespeichert, weil F-Chat für jede
  Verbindung ein neues API-Ticket (30 Minuten gültig) braucht. Die Datenbank entsprechend schützen.
- F-List bittet darum, Clients zuerst auf dem Testserver zu testen (Zugang per Helpdesk-Ticket) und die
  [Developer Policy](https://wiki.f-list.net/Developer_Policy) einzuhalten. Die Server-URL lässt sich
  über `network.server_url` ändern.
- Die Bridge hält die Flood-Control des Servers ein (Abstand laut `msg_flood`) und verbindet sich bei
  den Fehlercodes 2, 9, 30, 31, 33, 39 und 40 nicht automatisch neu, wie es die Protokoll-Doku verlangt.

## Aufbau

```
cmd/matrix-fchat/   Einstiegspunkt (mxmain.BridgeMain)
pkg/fchat/           F-Chat-Protokoll: WebSocket-Client, Reconnect, Ping, Flood-Control, Ticket-API
pkg/bbcode/          BBCode → HTML/Text
pkg/connector/       bridgev2-Connector: Login, Portale, Ghosts, Event-Umwandlung, Befehle
```

Tests: `go test -tags goolm ./...` (der Protokoll-Client wird gegen einen simulierten F-Chat-Server getestet).
