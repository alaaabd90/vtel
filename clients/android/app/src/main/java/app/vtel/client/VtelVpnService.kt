package app.vtel.client

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.graphics.drawable.Icon
import android.net.IpPrefix
import android.net.VpnService
import android.os.Build
import android.os.IBinder
import android.os.ParcelFileDescriptor
import android.util.Log
import java.io.File
import java.net.InetAddress
import kotlin.concurrent.thread
import org.json.JSONObject

// vtel-android's connect state, shared in-process between the service and
// MainActivity (both run in the same process, so a plain object is enough -
// no Binder/Messenger IPC needed for v1).
enum class VtelConnState { DISCONNECTED, CONNECTING, CONNECTED, ERROR }

object VtelServiceState {
    @Volatile
    var state: VtelConnState = VtelConnState.DISCONNECTED

    @Volatile
    var errorMessage: String? = null
}

// VtelVpnService establishes the TUN interface and bridges it to vtel's own
// local SOCKS5 client via hev-socks5-tunnel, then starts VtelEngine (the
// vtel Go binary itself) so it has something to bridge to. Modeled on
// gdrive's own GdriveVpnService (sibling project) but deliberately much
// simpler: no kill switch, no multi-account fan-out, no
// ConnectivityManager network-rebind callback, no per-app routing
// exclusions - those are real hardening gdrive's Android app has earned
// over time, not required for a first working vtel version. TUN/DNS
// addresses and the mapdns fake-IP range are reused verbatim from gdrive's
// own proven values (198.18.0.1/.2, 240.0.0.0/4) rather than invented
// fresh: vtel's own socks5/reject.go (ported from gdrive's socksserver.go
// in an earlier stage of this rebuild) already handles exactly this
// combination correctly - isBenchmarkIP/isMapDNSFakeIP reject stray raw
// fake-IP/benchmark-range CONNECTs (the cache-miss edge case, not the
// normal path - mapdns's whole point is translating those into ordinary
// domain-name CONNECTs before they ever reach vtel's SOCKS5 server), so
// there's no self-confusion risk in reusing gdrive's exact constants.
class VtelVpnService : VpnService() {
    private val engine by lazy { VtelEngine(this) { code -> handleUnexpectedExit(code) } }
    private val tunnel by lazy { HevTun2Socks() }
    private val lock = Any()
    private var vpnInterface: ParcelFileDescriptor? = null

    @Volatile
    private var running = false

    override fun onBind(intent: Intent?): IBinder? = super.onBind(intent)

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopSelfSafely()
            return START_NOT_STICKY
        }
        val configJson = intent?.getStringExtra(EXTRA_CONFIG_JSON)
        if (configJson == null) {
            stopSelf()
            return START_NOT_STICKY
        }
        VtelServiceState.state = VtelConnState.CONNECTING
        VtelServiceState.errorMessage = null
        startForegroundCompat("Connecting")
        thread(name = "vtel-vpn-start") { connect(configJson) }
        return START_STICKY
    }

    private fun connect(configJson: String) {
        synchronized(lock) {
            if (running) return
            try {
                val cfg = JSONObject(configJson)
                val listen = cfg.optString("listen", "127.0.0.1:1080")
                val parts = listen.split(":", limit = 2)
                val listenHost = parts.getOrElse(0) { "127.0.0.1" }
                val listenPort = parts.getOrNull(1)?.toIntOrNull() ?: 1080
                val readinessHost = if (listenHost == "0.0.0.0") "127.0.0.1" else listenHost

                engine.start(configJson, listenHost, listenPort)
                engine.waitUntilReady(readinessHost, listenPort)

                val configureIntent = PendingIntent.getActivity(
                    this,
                    0,
                    Intent(this, MainActivity::class.java),
                    PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
                )
                val builder = Builder()
                    .setSession("vtel")
                    .setMtu(DEFAULT_MTU)
                    .addAddress(TUN_IPV4_ADDRESS, 30)
                    .addRoute("0.0.0.0", 0)
                    .addDnsServer(MAP_DNS_ADDRESS)
                    .setConfigureIntent(configureIntent)

                runCatching {
                    builder.addAddress(TUN_IPV6_ADDRESS, 128)
                    builder.addRoute("::", 0)
                }.onFailure { Log.w(TAG, "IPv6 block failed, device may not support IPv6 on VPN", it) }

                addLocalNetworkExclusions(builder)
                runCatching { builder.addDisallowedApplication(packageName) }
                    .onFailure { Log.w(TAG, "Could not exclude vtel app from its own VPN route", it) }

                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                    builder.setMetered(true)
                }
                builder.setBlocking(true)

                val established = builder.establish()
                    ?: error("VpnService.Builder.establish() returned null (permission revoked?)")
                vpnInterface = established

                val tunFd = established.detachFd()
                val hevConfigFile = writeTunnelConfig(listenPort)
                tunnel.TProxyStartService(hevConfigFile.absolutePath, tunFd)

                running = true
                VtelServiceState.state = VtelConnState.CONNECTED
                startForegroundCompat("Connected")
            } catch (t: Throwable) {
                Log.e(TAG, "connect failed", t)
                VtelServiceState.state = VtelConnState.ERROR
                VtelServiceState.errorMessage = t.message ?: t.toString()
                stopSelfSafely()
            }
        }
    }

    private fun handleUnexpectedExit(code: Int) {
        Log.w(TAG, "vtel engine exited unexpectedly (code=$code), tearing down VPN")
        VtelServiceState.errorMessage = "vtel engine exited unexpectedly (code=$code)"
        stopSelfSafely()
    }

    override fun onRevoke() {
        stopSelfSafely()
        super.onRevoke()
    }

    override fun onDestroy() {
        stopSelfSafely()
        super.onDestroy()
    }

    private fun stopSelfSafely() {
        synchronized(lock) {
            runCatching { tunnel.TProxyStopService() }
            runCatching { vpnInterface?.close() }
            vpnInterface = null
            engine.stop()
            running = false
        }
        if (VtelServiceState.state != VtelConnState.ERROR) {
            VtelServiceState.state = VtelConnState.DISCONNECTED
        }
        runCatching { stopForeground(STOP_FOREGROUND_REMOVE) }
        stopSelf()
    }

    private fun addLocalNetworkExclusions(builder: Builder) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        listOf(
            "10.0.0.0/8",
            "172.16.0.0/12",
            "192.168.0.0/16",
            "169.254.0.0/16",
            "fc00::/7",
            "fe80::/10",
        ).forEach { cidr ->
            runCatching {
                val (address, prefix) = cidr.split("/", limit = 2)
                builder.excludeRoute(IpPrefix(InetAddress.getByName(address), prefix.toInt()))
            }.onFailure { error -> Log.w(TAG, "Could not exclude local route $cidr", error) }
        }
    }

    private fun writeTunnelConfig(socksPort: Int): File {
        val configFile = File(cacheDir, "vtel-tun2socks.yml")
        configFile.writeText(
            """
            tunnel:
              mtu: $DEFAULT_MTU
              ipv4: $TUN_IPV4_ADDRESS

            socks5:
              address: 127.0.0.1
              port: $socksPort
              udp: 'tcp'
              pipeline: true

            mapdns:
              address: $MAP_DNS_ADDRESS
              port: 53
              network: 240.0.0.0
              netmask: 240.0.0.0
              cache-size: 10000

            misc:
              log-level: warn
            """.trimIndent() + "\n",
        )
        return configFile
    }

    private fun startForegroundCompat(status: String) {
        ensureNotificationChannel()
        val notification = buildNotification(status)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun buildNotification(status: String): Notification {
        val contentIntent = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val stopIntent = PendingIntent.getService(
            this,
            1,
            Intent(this, VtelVpnService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.stat_sys_upload_done)
            .setContentTitle("vtel")
            .setContentText(status)
            .setContentIntent(contentIntent)
            .addAction(
                Notification.Action.Builder(
                    Icon.createWithResource(this, android.R.drawable.ic_menu_close_clear_cancel),
                    "Disconnect",
                    stopIntent,
                ).build(),
            )
            .setOngoing(true)
            .build()
    }

    private fun ensureNotificationChannel() {
        val manager = getSystemService(NotificationManager::class.java)
        if (manager.getNotificationChannel(CHANNEL_ID) == null) {
            manager.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "vtel VPN", NotificationManager.IMPORTANCE_LOW),
            )
        }
    }

    companion object {
        private const val TAG = "VtelVpn"
        private const val ACTION_STOP = "app.vtel.client.STOP_VPN"
        private const val EXTRA_CONFIG_JSON = "configJson"
        private const val CHANNEL_ID = "vtel_vpn"
        private const val NOTIFICATION_ID = 1908
        private const val DEFAULT_MTU = 1500
        private const val TUN_IPV4_ADDRESS = "198.18.0.1"
        private const val TUN_IPV6_ADDRESS = "fdeb:446c:912d::2"
        private const val MAP_DNS_ADDRESS = "198.18.0.2"

        fun start(context: Context, configJson: String) {
            val intent = Intent(context, VtelVpnService::class.java).putExtra(EXTRA_CONFIG_JSON, configJson)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
        }

        fun stop(context: Context) {
            context.startService(Intent(context, VtelVpnService::class.java).setAction(ACTION_STOP))
        }
    }
}
