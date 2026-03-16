# teltun

A SOCKS5 proxy that tunnels traffic through Telegram. Client runs a local SOCKS5 server; traffic is relayed as compressed documents through a private Telegram channel to a server with internet access.

## How it works

Two Telegram bots communicate via a shared private channel. Bot A (client) accepts local SOCKS5 connections and sends data upstream. Bot B (server) receives it, dials the target, and sends responses back. Traffic is batched, gzip-compressed, and sent as Telegram documents for efficiency.

## Setup

1. Create two bots via [@BotFather](https://t.me/BotFather)
2. Create a private Telegram channel and add both bots as admins
3. Get the channel's API chat ID (post a message in the channel, then check `getUpdates`):
   ```
   curl -s "https://api.telegram.org/bot<TOKEN>/getUpdates" | jq '.result[].channel_post.chat.id'
   ```
4. Get each bot's user ID:
   ```
   curl -s "https://api.telegram.org/bot<TOKEN>/getMe" | jq '.result.id'
   ```

## Build

```
go build -o teltun .
```

## Usage

**Server** (on the machine with internet access):
```
./teltun -mode server -token $BOT_B_TOKEN -peer-bot-id $BOT_A_ID -channel-id $CHANNEL_ID
```

**Client** (on the local machine):
```
./teltun -mode client -token $BOT_A_TOKEN -peer-bot-id $BOT_B_ID -channel-id $CHANNEL_ID -listen 127.0.0.1:1080
```

Then point any SOCKS5 client at `127.0.0.1:1080`:
```
curl -x socks5h://127.0.0.1:1080 https://ipv4.ident.me
```

### Flags

| Flag | Description | Default |
|------|-------------|---------|
| `-mode` | `client` or `server` | required |
| `-token` | This bot's API token | required |
| `-peer-bot-id` | The other bot's user ID | required |
| `-channel-id` | Private channel chat ID | required |
| `-listen` | SOCKS5 listen address (client only) | `127.0.0.1:1080` |

## Disclaimer

This project is for **educational purposes only**. It is intended to demonstrate network tunneling concepts and Telegram Bot API usage. Do not use this software for any illegal or unauthorized activities. The authors are not responsible for any misuse.
