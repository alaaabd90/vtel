package app.vtel.client

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.VpnService

// BootReceiver auto-starts the VPN after a reboot if the user opted in
// (config's auto_connect flag) and VPN permission was already granted in a
// previous session (VpnService.prepare returns null once granted - it's
// per-app, persists across reboots, and can't be re-requested from a
// BroadcastReceiver with no UI anyway).
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return

        val config = VtelConfigStore.loadOrInit(context)
        if (!config.optBoolean("auto_connect", false)) return
        if (VtelConfigStore.linkCount(config) == 0) return
        if (VpnService.prepare(context) != null) return // permission not yet granted, can't auto-start

        VtelVpnService.start(context, config.toString())
    }
}
