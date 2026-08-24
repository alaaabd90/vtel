package app.vtel.client

import android.Manifest
import android.app.Activity
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.List
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay
import org.json.JSONObject

private enum class Screen { STATUS, LINKS, SETTINGS, IMPORT, LOGS }

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 0)
        }

        setContent {
            MaterialTheme {
                AppScreen(this)
            }
        }
    }
}

@Composable
private fun AppScreen(activity: ComponentActivity) {
    var screen by remember { mutableStateOf(Screen.STATUS) }
    var config by remember { mutableStateOf(VtelConfigStore.loadOrInit(activity)) }
    var connState by remember { mutableStateOf(VtelServiceState.state) }
    var errorMessage by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(Unit) {
        while (true) {
            connState = VtelServiceState.state
            errorMessage = VtelServiceState.errorMessage
            delay(1000)
        }
    }

    val vpnPermissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.StartActivityForResult(),
    ) { result ->
        if (result.resultCode == Activity.RESULT_OK) {
            VtelVpnService.start(activity, config.toString())
        }
    }

    fun connect() {
        val intent = VpnService.prepare(activity)
        if (intent != null) {
            vpnPermissionLauncher.launch(intent)
        } else {
            VtelVpnService.start(activity, config.toString())
        }
    }

    fun disconnect() {
        VtelVpnService.stop(activity)
    }

    fun refreshConfig() {
        config = VtelConfigStore.loadOrInit(activity)
    }

    Scaffold(
        bottomBar = {
            NavigationBar {
                NavigationBarItem(
                    selected = screen == Screen.STATUS,
                    onClick = { screen = Screen.STATUS },
                    icon = { Icon(Icons.Filled.Info, contentDescription = "Status") },
                    label = { Text("Status") },
                )
                NavigationBarItem(
                    selected = screen == Screen.LINKS,
                    onClick = { screen = Screen.LINKS },
                    icon = { Icon(Icons.Filled.List, contentDescription = "Links") },
                    label = { Text("Links") },
                )
                NavigationBarItem(
                    selected = screen == Screen.SETTINGS,
                    onClick = { screen = Screen.SETTINGS },
                    icon = { Icon(Icons.Filled.Settings, contentDescription = "Settings") },
                    label = { Text("Settings") },
                )
                NavigationBarItem(
                    selected = screen == Screen.IMPORT,
                    onClick = { screen = Screen.IMPORT },
                    icon = { Icon(Icons.Filled.Add, contentDescription = "Import") },
                    label = { Text("Import") },
                )
                NavigationBarItem(
                    selected = screen == Screen.LOGS,
                    onClick = { screen = Screen.LOGS },
                    icon = { Icon(Icons.Filled.Warning, contentDescription = "Logs") },
                    label = { Text("Logs") },
                )
            }
        },
    ) { padding ->
        Column(modifier = Modifier.fillMaxSize().padding(padding).padding(16.dp)) {
            when (screen) {
                Screen.STATUS -> StatusScreen(
                    config = config,
                    connState = connState,
                    errorMessage = errorMessage,
                    onConnect = ::connect,
                    onDisconnect = ::disconnect,
                )
                Screen.LINKS -> LinksScreen(
                    activity = activity,
                    config = config,
                    onConfigChanged = { refreshConfig() },
                )
                Screen.SETTINGS -> SettingsScreen(
                    activity = activity,
                    config = config,
                    onConfigChanged = { refreshConfig() },
                )
                Screen.IMPORT -> ImportScreen(
                    activity = activity,
                    onImported = { refreshConfig(); screen = Screen.STATUS },
                )
                Screen.LOGS -> LogsScreen(activity = activity)
            }
        }
    }
}

@Composable
private fun StatusScreen(
    config: JSONObject,
    connState: VtelConnState,
    errorMessage: String?,
    onConnect: () -> Unit,
    onDisconnect: () -> Unit,
) {
    val linkCount = VtelConfigStore.linkCount(config)
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Card(modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(
                    when (connState) {
                        VtelConnState.DISCONNECTED -> "○ Disconnected"
                        VtelConnState.CONNECTING -> "◐ Connecting…"
                        VtelConnState.CONNECTED -> "● Connected"
                        VtelConnState.ERROR -> "✕ Error"
                    },
                    style = MaterialTheme.typography.titleMedium,
                )
                Text("$linkCount link(s) configured")
                if (errorMessage != null && connState == VtelConnState.ERROR) {
                    Text("Error: $errorMessage", color = MaterialTheme.colorScheme.error)
                }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Button(onClick = onConnect, enabled = connState == VtelConnState.DISCONNECTED || connState == VtelConnState.ERROR) {
                        Text("Connect")
                    }
                    OutlinedButton(onClick = onDisconnect, enabled = connState == VtelConnState.CONNECTED || connState == VtelConnState.CONNECTING) {
                        Text("Disconnect")
                    }
                }
            }
        }
        if (linkCount == 0) {
            Card(modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp)) {
                    Text("No links configured yet.")
                    Text("Add at least one via the Links tab before connecting.")
                }
            }
        }
    }
}

@Composable
private fun LinksScreen(activity: ComponentActivity, config: JSONObject, onConfigChanged: () -> Unit) {
    var token by remember { mutableStateOf("") }
    var peerBotId by remember { mutableStateOf("") }
    var channelId by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }

    val links = config.optJSONArray("links")
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text("Links", style = MaterialTheme.typography.titleMedium)
        LazyColumn(modifier = Modifier.weight(1f, fill = false)) {
            items(links?.length() ?: 0) { i ->
                val link = links!!.getJSONObject(i)
                Card(modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
                    Row(
                        Modifier.padding(12.dp).fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween,
                    ) {
                        Column {
                            Text("#$i  ${VtelConfigStore.redactToken(link.optString("token"))}")
                            Text("peer=${link.optLong("peer_bot_id")}  channel=${link.optLong("channel_id")}")
                        }
                        OutlinedButton(onClick = {
                            VtelConfigStore.removeLink(activity, config, i)
                            onConfigChanged()
                        }) { Text("Remove") }
                    }
                }
            }
        }
        Card(modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("Add a link", style = MaterialTheme.typography.titleSmall)
                OutlinedTextField(value = token, onValueChange = { token = it }, label = { Text("Bot token") }, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(value = peerBotId, onValueChange = { peerBotId = it }, label = { Text("Peer bot user ID") }, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(value = channelId, onValueChange = { channelId = it }, label = { Text("Channel ID") }, modifier = Modifier.fillMaxWidth())
                Button(onClick = {
                    val peer = peerBotId.trim().toLongOrNull()
                    val chan = channelId.trim().toLongOrNull()
                    if (token.isBlank() || peer == null || chan == null) {
                        status = "Enter a token and valid numeric IDs"
                        return@Button
                    }
                    VtelConfigStore.addLink(activity, config, token.trim(), peer, chan)
                    token = ""; peerBotId = ""; channelId = ""
                    status = "Link added. Reconnect to apply."
                    onConfigChanged()
                }) { Text("Add link") }
                if (status.isNotEmpty()) Text(status)
            }
        }
    }
}

@Composable
private fun SettingsScreen(activity: ComponentActivity, config: JSONObject, onConfigChanged: () -> Unit) {
    var secret by remember { mutableStateOf(config.optString("secret")) }
    var listen by remember { mutableStateOf(config.optString("listen", "127.0.0.1:1080")) }
    var compression by remember { mutableStateOf(config.optString("compression_level", "fastest")) }
    var rejectIPv6 by remember { mutableStateOf(config.optBoolean("reject_ipv6", false)) }
    var autoConnect by remember { mutableStateOf(config.optBoolean("auto_connect", false)) }
    var status by remember { mutableStateOf("") }

    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text("Settings", style = MaterialTheme.typography.titleMedium)
        OutlinedTextField(value = secret, onValueChange = { secret = it }, label = { Text("Secret") }, modifier = Modifier.fillMaxWidth())
        OutlinedTextField(value = listen, onValueChange = { listen = it }, label = { Text("SOCKS5 listen address") }, modifier = Modifier.fillMaxWidth())
        OutlinedTextField(value = compression, onValueChange = { compression = it }, label = { Text("Compression (fastest/default/better/best)") }, modifier = Modifier.fillMaxWidth())
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            Switch(checked = rejectIPv6, onCheckedChange = { rejectIPv6 = it })
            Text(" Reject IPv6 literal targets")
        }
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            Switch(checked = autoConnect, onCheckedChange = { autoConnect = it })
            Text(" Auto-connect on boot (requires VPN permission already granted once)")
        }
        Button(onClick = {
            config.put("secret", secret)
            config.put("listen", listen)
            config.put("compression_level", compression)
            config.put("reject_ipv6", rejectIPv6)
            config.put("auto_connect", autoConnect)
            VtelConfigStore.save(activity, config)
            status = "Saved. Reconnect to apply."
            onConfigChanged()
        }) { Text("Save settings") }
        if (status.isNotEmpty()) Text(status)

        Card(modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp)) {
                Text("Battery optimization", style = MaterialTheme.typography.titleSmall)
                Text("Doze mode can throttle the background engine after the screen sleeps. Exempting this app keeps the tunnel responsive.")
                OutlinedButton(onClick = {
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                        runCatching {
                            activity.startActivity(
                                android.content.Intent(android.provider.Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS)
                                    .setData(android.net.Uri.parse("package:${activity.packageName}")),
                            )
                        }
                    }
                }) { Text("Request battery exemption") }
            }
        }
    }
}

@Composable
private fun ImportScreen(activity: ComponentActivity, onImported: () -> Unit) {
    var text by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text("Import config", style = MaterialTheme.typography.titleMedium)
        Text("Paste a complete vtel config.json (e.g. copied from the server side).")
        OutlinedTextField(
            value = text,
            onValueChange = { text = it },
            modifier = Modifier.fillMaxWidth(),
            minLines = 10,
        )
        Button(onClick = {
            val parsed = runCatching { JSONObject(text) }.getOrElse {
                status = "Invalid JSON: ${it.message}"
                return@Button
            }
            VtelConfigStore.save(activity, parsed)
            status = "Imported ${VtelConfigStore.linkCount(parsed)} link(s)."
            onImported()
        }) { Text("Import") }
        if (status.isNotEmpty()) Text(status)
    }
}

@Composable
private fun LogsScreen(activity: ComponentActivity) {
    var lines by remember { mutableStateOf(listOf<String>()) }
    LaunchedEffect(Unit) {
        while (true) {
            val logFile = java.io.File(java.io.File(activity.filesDir, "logs"), "vtel.log")
            lines = runCatching { logFile.readLines().takeLast(300) }.getOrDefault(emptyList())
            delay(1500)
        }
    }
    Column {
        Text("Logs", style = MaterialTheme.typography.titleMedium)
        LazyColumn(modifier = Modifier.fillMaxSize().padding(top = 8.dp)) {
            items(lines) { line -> Text(line, style = MaterialTheme.typography.bodySmall) }
        }
    }
}
