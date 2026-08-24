# vtel

A multi-bot SOCKS5 proxy that tunnels traffic through Telegram. The client
runs a local SOCKS5 server; traffic is encrypted, compressed, batched, and
relayed as Telegram bot messages/documents through a pool of private
channels to a server with internet access.

Built on top of [teltun](https://github.com/alaaabd90/teltun) (a single-bot
version of the same idea), generalized to a health-aware load-balanced pool
of bots/channels and hardened with patterns ported from
[gdrive](https://github.com/alaaabd90/gdrive), a sibling project doing the
same thing over Google Drive — see "What's ported from gdrive, and what
isn't" below.

## How it works

Each **link** is one pair of bots (a client-side bot and a server-side bot)
sharing one private Telegram channel. The client SOCKS5-accepts a
connection, picks the least-loaded healthy link from the pool, and sends an
encrypted, compressed batch of frames over it; the server-side bot receives
it, dials the real target, and relays data back the same way. A pool of N
links behaves like N independent lanes: a stream picks one link for its
whole lifetime, and the pool automatically avoids/retries around links that
are failing or stalled.

## Quick install (VPS)

```
curl -sL https://raw.githubusercontent.com/alaaabd90/vtel/main/scripts/install.sh | bash
```

Installs the `vtel` binary + CLI manager, and sets up the **server** role by
default (the side with internet access — matching gdrive's own installer,
which sets up its exit-node role the same way; this is what you'd typically
put on a VPS). To install the **client** role instead:

```
curl -sL https://raw.githubusercontent.com/alaaabd90/vtel/main/scripts/install.sh | VTEL_MODE=client bash
```

vtel can't provision bot links itself — each one needs a real token from
[@BotFather](https://t.me/BotFather) — so a fresh install writes an empty
config skeleton with a random secret and tells you to add at least one link
before the systemd service has anything to do:

```
vtel links add      # prompts for token / peer_bot_id / channel_id
vtel install         # creates and starts the systemd service
```

Run `vtel` with no arguments for the interactive manager (status, restart,
logs, link management, settings, update, rollback, uninstall) — see
"CLI reference" below for every subcommand.

## Setup

For each link you want in the pool:

1. Create two bots via [@BotFather](https://t.me/BotFather) — one for the
   client role, one for the server role.
2. Create a private Telegram channel and add both bots as admins.
3. Get the channel's chat ID (post a message in the channel, then check
   `getUpdates`):
   ```
   curl -s "https://api.telegram.org/bot<TOKEN>/getUpdates" | jq '.result[].channel_post.chat.id'
   ```
4. Get each bot's user ID:
   ```
   curl -s "https://api.telegram.org/bot<TOKEN>/getMe" | jq '.result.id'
   ```

Repeat for as many links as you want in the pool — a handful is already a
meaningful independent backup channel; you don't need 20 to get value from
this. See `config.example.json` for the config shape (it shows the spec's
originally-discussed default of 5 channels × 4 bots = 20 links, purely to
illustrate the format at scale).

## Build

```
go build -o vtel ./cmd/vtel
```

or via Docker:

```
docker build -t vtel .
```

(The Docker build itself hasn't been exercised in this environment — no
Docker daemon was available while developing this — though the underlying
`go build ./cmd/vtel` command it runs has been run and verified directly
many times.)

## Desktop app (Windows / Linux)

`cmd/vtel-desktop` is a client-role GUI, modeled on gdrive's own desktop app
(`cmd/gkdrive-desktop` in the sibling project) — same Fyne-based structure
and sidebar-of-views layout, adapted to vtel's much simpler model: since a
`tunnel.Client` already load-balances across every configured link
internally, there's no multi-profile load-balancer layer to build (gdrive's
desktop app runs one profile = one account per local port and balances
across them itself; vtel-desktop is just one config, one client).

```
go build -o vtel-desktop ./cmd/vtel-desktop
```

**Requires CGO** (`CGO_ENABLED=1` and a C compiler) — Fyne's default
renderer binds to OpenGL/GLFW, which is a cgo dependency with no pure-Go
fallback. On Windows, a mingw-w64 toolchain works; on Linux you'll also
need the X11/GL dev packages (`libgl1-mesa-dev xorg-dev libxcursor-dev
libxrandr-dev libxinerama-dev libxi-dev libxxf86vm-dev` on Debian/Ubuntu —
the same list gdrive's own release workflow installs for its desktop
build).

Config lives in a per-user directory (`%AppData%\vtel\config.json` on
Windows, `~/.config/vtel/config.json` on Linux) — a fresh launch creates an
empty client-mode skeleton with a random secret automatically. Add links
via the Links tab, or paste a complete config (e.g. copied from the server
side) via Import. vtel's internal logging has no pluggable logger
interface (it's plain `fmt.Printf` to stdout throughout); the desktop app
redirects `os.Stdout` to its own Logs tab at startup to capture it without
touching any core package.

Verified in this environment: builds clean with CGO enabled, launches
without a startup crash, and (after fixing two real background-goroutine
UI-update violations Fyne's threading-model checker caught on first run)
produces no more threading warnings on relaunch. Not yet verified: the
actual connect/disconnect/Links/Settings/Import flows against a live
Telegram bot pool — same as everything else in this project, that needs
real bot tokens and the real network.

## Usage

Both client and server take one JSON config file via `-config`:

```
./vtel -config config.json
```

**Server** config (`mode: "server"`) needs one `LinkConfig` per link, each
with that link's *server-side* bot token, the *client-side* bot's user ID as
`peer_bot_id`, and the shared channel ID.

**Client** config (`mode: "client"`) is the mirror image: each link's
*client-side* bot token, the *server-side* bot's user ID as `peer_bot_id`,
the same channel ID, plus `listen` (SOCKS5 address, default
`127.0.0.1:1080`).

Both sides must set the same `secret` — it derives the AES-256-GCM key used
to encrypt every batch (see Security below).

```json
{
  "mode": "client",
  "listen": "127.0.0.1:1080",
  "secret": "a long random shared secret",
  "compression_level": "fastest",
  "reject_ipv6": false,
  "links": [
    { "token": "...", "peer_bot_id": 123, "channel_id": -100456 },
    { "token": "...", "peer_bot_id": 124, "channel_id": -100457 }
  ]
}
```

Then point any SOCKS5 client at the listen address:
```
curl -x socks5h://127.0.0.1:1080 https://ipv4.ident.me
```

### Config fields

| Field | Description | Default |
|---|---|---|
| `mode` | `"client"` or `"server"` | required |
| `listen` | SOCKS5 listen address (client only) | `127.0.0.1:1080` |
| `secret` | Shared secret both sides derive the AEAD key from | required |
| `compression_level` | `fastest` \| `default` \| `better` \| `best` | `fastest` |
| `reject_ipv6` | Client only: immediately reject IPv6 literal SOCKS targets | `false` |
| `quiet_hours` | `{start_hour, end_hour, timezone}` — widens the flush cadence during this daily window instead of pausing (see Traffic shaping below) | disabled |
| `links` | Array of `{token, peer_bot_id, channel_id}` | required, ≥1 |

## CLI reference

`vtel` doubles as both the running tunnel process (`vtel -config <path>`,
what systemd's `ExecStart` invokes) and a management CLI, the same
dual-purpose pattern as gdrive's own binary. Every CLI subcommand reads/
writes `/root/vtel/config.json` by default (override with the `VTEL_CONFIG`
env var) and manages a systemd unit named `vtel`.

| Command | Description |
|---|---|
| `vtel` | interactive menu |
| `vtel status` | service state + config summary |
| `vtel restart` | restart the systemd service |
| `vtel logs` | follow live journal (`journalctl -u vtel -f`) |
| `vtel links` / `links add` / `links remove <N>` | list/add/remove bot links |
| `vtel config` / `config --reveal-secret` | show current config (secret redacted by default) |
| `vtel export [file]` | print (or write) the full config JSON |
| `vtel update` | download and install the latest GitHub release |
| `vtel rollback <tag>` | install a specific previous release (no arg lists available tags) |
| `vtel install` | (re)create and start the systemd service from the current config |
| `vtel uninstall [--force]` | permanently remove vtel: service, config, binary |

Unlike gdrive, there's no "restart single account" / per-account service
equivalent: vtel's links all run as goroutines inside one process (client
or server, per `mode`), not as separate OS processes, so there's only ever
one service to restart. There's also no "Google IP" / SNI / DNS-routing /
account-proxy equivalents — those are Drive/Google-specific concepts with
no counterpart in vtel's architecture, so the menu doesn't fabricate
matching items for them; see "What's ported from gdrive" above for what
*does* carry over.

## Security

Every batch is AES-256-GCM sealed (random 12-byte nonce, key derived from
`secret` via SHA-256) before it's compressed and uploaded, and verified
before being decompressed on receive. This is a real gap teltun itself
doesn't close — without it, confidentiality rests entirely on the channel
being private. Wrong-key or tampered/corrupt data is silently skipped
rather than surfaced as an error, the same way non-vtel content posted to
the shared chat is silently skipped.

## Traffic shaping ("look normal" features)

**Honest framing: these reduce *pattern* detectability, not *volume*
visibility.** An observer who already sees total bytes/day sent through
your Telegram bots learns nothing new is hidden by any of the following —
none of it reduces how much data moves or when, only how regular/labeled it
looks:

- **Timing jitter**: ±15% randomness on the batching timers, so flushes
  don't happen at an exact fixed cadence.
- **Quiet hours**: during a configured daily window, the flush cadence
  widens (3×) rather than traffic stopping entirely — a full on/off pattern
  is itself a detectable signal.
- **Filename rotation**: document uploads rotate among generic-but-honest
  base names (`backup_`, `export_`, `archive_...bin.zst`) rather than one
  fixed name. Deliberately **not** faked as real media (e.g. naming a zstd
  blob `IMG_1234.jpg`) — a fake media extension on non-media content is
  itself a worse fingerprint than an honest generic name.
- **Size padding**: deliberately **not** implemented. Artificial padding
  costs real bandwidth for no confidentiality gain, and jitter plus the
  existing batching size thresholds already vary batch sizes naturally.

## What's ported from gdrive, and what isn't

vtel harvests specific, already-debugged mechanisms from gdrive rather than
re-deriving them from scratch — but only the subset that actually fits
vtel's much smaller scale (5-20 bots, not Drive-account-scale parallelism).

**Ported:**
- **Health-aware load balancing** (`pool/pool.go`, from
  `cmd/gdrive-exit/lb.go`): least-connections selection among healthy
  links, consecutive-failure blacklisting with cooldown, graceful
  degradation when every link looks unhealthy rather than hard-refusing.
- **Seq-based frame ordering/dedup** (`tunnel/mux.go`): out-of-order frames
  buffer and drain in order; stale/duplicate seqs are dropped.
- **Throughput-adaptive batching** (`protocol/batch.go`, from
  `muxLane.adaptiveCorkDelay`/`updateBytesPerSec`): the real 5/10/15ms
  tiers, with a deliberate deviation for the low-throughput default (see
  below).
- **Dual-path urgent/normal concurrency gate** (from
  `adaptiveLimiter`/priority-reserve), right-sized to a 2-slot semaphore.
- **Connection warmup** (from `ConnectionWarmerStore`): periodic pings to
  keep idle links' connections/health current.
- **Channel-based buffer pooling** (from `getBatchBuf`/`putBatchBuf`, the
  v1.0.65 fix): not `sync.Pool`, which drops its contents every GC cycle
  under real load.
- **Graceful shutdown with peer notification**: TypeClose sent for every
  open stream before local teardown, instead of the peer discovering a
  disconnect only via a stall timeout.
- **Immediate SOCKS-layer rejects** (`socks5/reject.go`): fake-IP/benchmark-
  range/DNS-over-TLS-probe targets rejected before a wasted dial attempt.
- **Exit-side DNS cache** (`tunnel/dnscache.go`, from `dnscache.go`): a
  size-bounded (2048 entries) pos/neg-TTL cache in front of the resolver, so
  a repeat target on the same server-side link skips a fresh DNS lookup.
  Not ported: gdrive's address-family preference logic — that exists for
  gdrive's mobile-tethering Happy-Eyeballs context, and vtel's
  `RejectIPv6` default already made the opposite call.
- **Throughput as a load-balancer tiebreak** (`pool/pool.go`, from a
  historical `bytesScore` fix in `cmd/gdrive-exit/lb.go`, v1.0.91): pure
  least-connections degenerates to round-robin once load fans out evenly,
  since every link ends up with the same connection count regardless of
  actual throughput. `Link.ThroughputBytesPerSec` (fed by the existing
  per-link `Batcher.bytesPerSec` measurement) is consulted only as a
  tiebreak among links already tied on active-stream count — deliberately
  narrow. gdrive itself later reverted the *full* feature, not because the
  tiebreak was wrong, but because it got bundled with heavier adaptive
  bandwidth-cap/burst-controller machinery that caused real regressions;
  none of that extra machinery is ported here.
- **Queued+in-flight byte budget (backpressure)** (`protocol/batch.go`, from
  `normalLaneBudgetBytes` in `internal/gdrive/mux.go`): a confirmed
  production bug in gdrive — batch bytes were released from accounting the
  instant they were dequeued for upload, even though the payload stays
  alive in memory for the whole compress+seal+upload(+retries) lifecycle,
  heap-profiled to reach multiple GB for one busy account. `Batcher.Add`
  now blocks (bounded, polling) until `maxQueuedAndInFlightBytes` has room,
  and reports failure to its caller on timeout rather than either blocking
  forever or silently dropping a frame — `Mux.Relay` treats that failure
  the same as a real read/write error and tears the one stuck stream down.
  Stage 5's async per-flush goroutine dispatch made vtel structurally more
  exposed to this failure shape than gdrive's original bug, not less, so
  this was worth doing proactively rather than waiting to hit it under
  real load. One caution carried over from gdrive's own history: this
  exact class of bound got mistuned too tight at least once there
  (v1.0.33/34) and had to be loosened — the budget here (192MB) is set
  generously for the same reason.

**Deliberately not ported**, with reasons:
- **Upload-ID pre-reservation pool**: solves a specific Google Drive API
  shape (`files.generateIds` before `files.create`); Telegram's send calls
  return their own ID synchronously, no equivalent primitive exists.
- **`bulkPacerWait`**: confirmed dead/harmful in gdrive itself (a real
  regression, self-throttling on a signal its own bulk traffic produced) —
  not ported under any name.
- **Dedicated per-priority worker pools / per-worker urgent budgets**: no
  shared worker pool exists to split at vtel's per-link scale; a 2-slot
  semaphore does the same job.
- **`fleet.go`'s process-restart supervision**: no separate OS processes
  here to restart — vtel's "links" are goroutines within one process, not
  child processes.
- **Prefetch/pipelining**: gdrive's version decouples upload-confirmation
  from starting to poll for a response; vtel's poller already runs
  continuously and independently of the sender from the start, so there's
  no coupling to decouple.
- **One deliberate deviation, not an omission**: gdrive's low-throughput
  batching default is 10ms (a Drive PUT is cheap); vtel keeps a 250ms
  floor instead, since a Telegram flush is a real rate-limited API call —
  flushing an idle trickle every 10ms would just queue tiny calls behind
  the rate limiter for no gain.

## Honest limits

- **Telegram's real ceilings apply**: 50MB `sendDocument`, ~20MB
  `getFile`/download (batches are split before hitting this), 4096-char
  text messages.
- **This will not beat gdrive on raw speed.** A realistic tuned target is
  roughly **150-250 Mbps aggregate across ~20 bots** — Telegram's Bot API
  rate limits are the hard ceiling, not vtel's own code. If you need
  maximum throughput, use gdrive.
- **The real value of vtel is redundancy**: an independent backup channel
  on completely different infrastructure (Telegram's API, not Google's),
  useful when your primary tunnel is blocked/down but Telegram isn't (or
  vice versa) — not a faster primary path.
- **Traffic shaping reduces pattern detectability only** (see above), not
  volume visibility — don't rely on it to hide *that* you're moving data,
  only to reduce how mechanically regular the moving looks.
- **What still needs real-network testing**: everything in this repo has
  been verified against `faketelegram` (an in-memory fake of the Bot API)
  via `cmd/smoketest` and `cmd/vtel-bench`, plus unit tests per stage — but
  none of it has run against live bot tokens and the real Telegram network
  yet. Real-world concerns a fake transport can't surface: actual Bot API
  rate-limit behavior and 429 frequency under sustained load (the bench
  harness's 429 counter reads 0 against the fake, by design — see its
  `-live-config` flag, which is accepted but not yet wired to a working
  harness), real network latency's effect on the adaptive batching tiers,
  and whether 20 concurrent bots on one channel trigger any Telegram-side
  throttling this design doesn't yet account for.
- **`go test -race` now passes clean across the whole suite** (a C compiler
  wasn't available earlier in development; once one was, `-race` — including
  the concurrent flush-dispatch/buffer-pool/backpressure code from later
  stages — and a `-race`-built `cmd/smoketest` run both came back clean).

## Disclaimer

This project is for **educational purposes only**. It is intended to
demonstrate network tunneling concepts and Telegram Bot API usage. Do not
use this software for any illegal or unauthorized activities. The authors
are not responsible for any misuse.
