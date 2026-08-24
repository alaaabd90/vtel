# vtel Android Client

Modeled on gdrive's own Android app (the sibling project's `clients/android`)
but scoped much smaller: VPN-only (no separate proxy mode, no multi-account
manager, no IP scanner, no per-app routing, no HWID device-locking, no LAN
sharing) - see the repo's top-level README's "What's ported from gdrive, and
what isn't" section for the reasoning behind each of those omissions.

The app packages vtel's own `cmd/vtel` binary and runs it as a subprocess
inside a foreground `VpnService`, bridging the device's whole-device TUN
interface to it via [hev-socks5-tunnel](https://github.com/heiher/hev-socks5-tunnel)
(vendored at `third_party/hev-socks5-tunnel`, MIT-licensed - the same
TUN-to-SOCKS bridge gdrive's Android app uses, rebuilt here for this app's
own package/class name since its JNI registration is resolved by name at
native-library-load time).

vtel-android is **client-role only** - one config, one running engine
process. Unlike gdrive's per-Google-account fan-out, vtel's own pool already
balances across every configured link internally, so there's nothing to
multiplex on the Android side either.

## Build

```bash
cd clients/android
./gradlew :app:assembleDebug --console=plain
```

Requires: JDK 17, Android SDK (compileSdk 35), NDK `27.0.12077973` (matches
`android.ndkVersion` in `app/build.gradle.kts`), and the Go toolchain vtel's
own `go.mod` specifies - the build cross-compiles `cmd/vtel` itself as part
of `preBuild` (see the `buildVtelAndroidSidecar` task), no separate Go build
step needed first.

The Gradle build compiles two native pieces for both `arm64-v8a` and
`armeabi-v7a`, then emits a universal APK plus per-ABI APKs:

- `./cmd/vtel` as an Android PIE executable packaged as `lib/<abi>/libvtel.so`
  (`buildVtelAndroidSidecar` task);
- the vendored TUN-to-SOCKS bridge packaged as
  `lib/<abi>/libhev-socks5-tunnel.so` (`buildHevTun2socks` task).

The app launches the sidecar with just `libvtel.so -config <path>` - vtel's
entire CLI surface for this purpose, no per-flag tuning needed since
everything else (listen address, links, secret, compression, quiet hours)
lives in the JSON config itself.

## Manual use

Install the APK, open the Links tab and add at least one link (bot token,
peer bot user ID, channel ID - same three fields as the VPS CLI's
`vtel links add` and the desktop app's Links tab), then tap Connect on the
Status tab. Android shows the standard VPN consent dialog the first time.
The Import tab accepts a complete `config.json` pasted from another vtel
front end (VPS, desktop) instead of adding links one at a time.

Settings match the VPS/desktop config fields exactly (secret, listen
address, compression level, reject IPv6) plus one Android-only field,
auto-connect-on-boot (requires VPN permission to have already been granted
in a previous session - `BootReceiver` can't prompt for it itself).

## Honest status

Written and (structurally) reviewed against gdrive's own proven Android
architecture, but **not built or run on a device in the environment this was
developed in** - no Android SDK/NDK/emulator were available there. The
CI workflow (`.github/workflows/release.yml`'s `build-android` job) will
either produce a real APK or surface real build errors on push; that's the
first real compile-verification this code gets. Treat it accordingly until
confirmed working end to end on a real phone.
