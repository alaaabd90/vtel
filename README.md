# vtel

A SOCKS5 tunnel that ships traffic as Telegram bot document uploads
instead of raw sockets. A client exposes a local SOCKS5 proxy; traffic is
framed, batched, gzip-compressed, and AES-256-GCM encrypted before being
uploaded via `sendDocument` into a shared private Telegram channel/chat.
An exit process on the other end long-polls `getUpdates` for that traffic
and relays it to the real destination.

Multiple bot tokens ("lanes") are supported because Telegram's flood/rate
limits are tracked per bot, not per chat - more tokens configured in the
shared pool means more parallel upload throughput.

Sibling project to [gdrive](https://github.com/alaaabd90/gdrive), which
does the same thing over Google Drive uploads instead.

## Status

v1: a complete, working client/exit tunnel - stream multiplexing,
ordering/dedup across concurrent multi-bot delivery, AES-256-GCM
encryption, a real SOCKS5 (CONNECT-only) listener, and a single `vtel`
binary with `serve-client`/`serve-exit` subcommands. See
[`internal/vtel`](internal/vtel) for the implementation and its test
suite (`go test ./...`).

Not yet included (see the package doc comments for why): persisted
`getUpdates` offsets across restarts, backpressure/connection limits,
retry/backoff tuning, structured metrics, multi-client identity,
upstream-proxy chaining on exit dial, SOCKS5 UDP ASSOCIATE.

## Config

Both roles load the same JSON file - every bot token must be known to
both sides, since a lane's token is a shared secret between the client
and exit process, not something one side owns exclusively.

```json
{
  "bots": ["123456789:AAxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"],
  "chat_id": -1001234567890,
  "listen": "127.0.0.1:1080",
  "secret": "a shared passphrase only your client and exit know"
}
```

- `bots` - the fleet of bot tokens making up the lane pool. All of them
  must be admins of `chat_id`.
- `chat_id` - the private channel/supergroup all bots are admins of (a
  negative ID, as returned by `getChat`).
- `listen` - the client role's local SOCKS5 listen address.
- `secret` - the shared passphrase both sides derive the AES-256-GCM
  envelope key from. Required - without it, Telegram (and anyone holding
  a bot token) can read tunneled content in plaintext.

## Run

```
vtel serve-exit   -config exit.json
vtel serve-client -config client.json
```

Then point any SOCKS5-aware client at the `listen` address:

```
curl --socks5 127.0.0.1:1080 https://example.com
```

## Build

```
go build ./cmd/vtel
```

No external dependencies - the standard library only.
