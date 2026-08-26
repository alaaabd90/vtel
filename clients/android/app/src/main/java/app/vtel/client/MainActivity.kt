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
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.List
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Warning
import androidx.compose.ui.text.input.PasswordVisualTransformation
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
import java.io.File
import kotlinx.coroutines.delay
import org.json.JSONObject

private enum class Screen { STATUS, LINKS, ACCOUNT, SETTINGS, IMPORT, LOGS }

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
                    selected = screen == Screen.ACCOUNT,
                    onClick = { screen = Screen.ACCOUNT },
                    icon = { Icon(Icons.Filled.Person, contentDescription = "Account") },
                    label = { Text("Account") },
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
        // imePadding() reserves space for the on-screen keyboard explicitly.
        // targetSdk 35 forces edge-to-edge for every app, which makes the
        // old windowSoftInputMode="adjustResize"/"adjustPan" manifest flags
        // unreliable - without this, the keyboard just overlays as a fixed
        // panel and a scrollable Column has no extra space to scroll into,
        // permanently hiding whatever's underneath it (this is what made
        // the Account screen's code-entry field unreachable).
        Column(modifier = Modifier.fillMaxSize().padding(padding).imePadding().padding(16.dp)) {
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
                Screen.ACCOUNT -> AccountScreen(
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
    val links = config.optJSONArray("links")
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Text("Links", style = MaterialTheme.typography.titleMedium)
        LazyColumn(modifier = Modifier.weight(1f, fill = false)) {
            items(links?.length() ?: 0) { i ->
                val link = links!!.getJSONObject(i)
                val isAccount = link.optString("kind") == "account"
                Card(modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
                    Row(
                        Modifier.padding(12.dp).fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween,
                    ) {
                        Column {
                            if (isAccount) {
                                Text("#$i  account  ${File(link.optString("session")).name}")
                                Text("peer_user_id=${link.optLong("peer_user_id")}  channel=${link.optLong("channel_id")}")
                            } else {
                                Text("#$i  bot  ${VtelConfigStore.redactToken(link.optString("token"))}")
                                Text("peer_bot_id=${link.optLong("peer_bot_id")}  channel=${link.optLong("channel_id")}")
                            }
                        }
                        OutlinedButton(onClick = {
                            VtelConfigStore.removeLink(activity, config, i)
                            onConfigChanged()
                        }) { Text("Remove") }
                    }
                }
            }
        }
        if ((links?.length() ?: 0) == 0) {
            Card(modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp)) {
                    Text("No links yet. Add one from the Account tab: log a real phone number in, then add its link there.")
                }
            }
        }
    }
}

// AccountScreen is the app's only way to add a link: log a real Telegram
// account (a real phone number) in over MTProto - see VtelAccountLogin,
// which drives `libvtel.so account login ... -machine` as a subprocess the
// same way a terminal would drive it interactively - then fill in the
// peer's user ID (from the *other* side's own login) and the shared
// channel ID to turn the resulting session into a link.
@Composable
private fun AccountScreen(activity: ComponentActivity, config: JSONObject, onConfigChanged: () -> Unit) {
    var apiId by remember { mutableStateOf(VtelConfigStore.getApiId(config).let { if (it == 0) "" else it.toString() }) }
    var apiHash by remember { mutableStateOf(VtelConfigStore.getApiHash(config)) }
    var phone by remember { mutableStateOf("") }
    var codeInput by remember { mutableStateOf("") }
    var passwordInput by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }

    val login = remember { VtelAccountLogin(activity) }
    var loginState by remember { mutableStateOf<LoginState>(LoginState.Idle) }
    var knownAccounts by remember { mutableStateOf(VtelConfigStore.loadKnownAccounts(activity)) }

    LaunchedEffect(Unit) {
        while (true) {
            loginState = login.state
            delay(400)
        }
    }

    // Persist a completed login the instant it succeeds, independent of
    // this screen's own state - see VtelConfigStore.addKnownAccount's doc
    // comment for why: without this, the process getting killed before the
    // user gets to enter the peer_user_id/channel_id (a real possibility -
    // that data usually isn't available until someone else finishes their
    // own login, which can take a while) meant redoing the SMS code step
    // just to reach the "add link" form again, even though the login and
    // its session file were already done.
    LaunchedEffect(loginState) {
        val s = loginState
        if (s is LoginState.Success) {
            VtelConfigStore.addKnownAccount(activity, phone.trim(), s.sessionPath, s.userId)
            knownAccounts = VtelConfigStore.loadKnownAccounts(activity)
        }
    }

    Column(
        modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState()),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text("Account login", style = MaterialTheme.typography.titleMedium)
        Text(
            "vtel-android only connects through a real logged-in Telegram account, not a bot. " +
                "One api_id/api_hash pair (from https://my.telegram.org) covers every account you log in here.",
        )

        Card(modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(value = apiId, onValueChange = { apiId = it }, label = { Text("api_id") }, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(value = apiHash, onValueChange = { apiHash = it }, label = { Text("api_hash") }, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(
                    value = phone,
                    onValueChange = { phone = it },
                    label = { Text("Phone number") },
                    supportingText = { Text("With country code, e.g. +15551234567") },
                    enabled = loginState == LoginState.Idle || loginState is LoginState.Failed,
                    modifier = Modifier.fillMaxWidth(),
                )
                Button(
                    enabled = loginState == LoginState.Idle || loginState is LoginState.Failed,
                    onClick = {
                        val id = apiId.trim().toIntOrNull()
                        if (id == null || apiHash.isBlank() || phone.isBlank()) {
                            status = "Enter api_id, api_hash, and a phone number first"
                            return@Button
                        }
                        VtelConfigStore.setApiCredentials(activity, config, id, apiHash.trim())
                        status = ""
                        codeInput = ""
                        passwordInput = ""
                        login.start(phone.trim(), id.toString(), apiHash.trim(), VtelConfigStore.sessionPathFor(activity, phone.trim()))
                    },
                ) { Text("Log in") }
            }
        }

        when (val s = loginState) {
            LoginState.Running -> Text("Connecting to Telegram...")
            LoginState.NeedCode -> Card(modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text("Enter the code Telegram just sent to $phone")
                    OutlinedTextField(value = codeInput, onValueChange = { codeInput = it }, label = { Text("Code") }, modifier = Modifier.fillMaxWidth())
                    Button(onClick = { login.submitCode(codeInput.trim()); codeInput = "" }) { Text("Submit code") }
                }
            }
            LoginState.NeedPassword -> Card(modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text("This account has a 2FA password - enter it")
                    OutlinedTextField(
                        value = passwordInput,
                        onValueChange = { passwordInput = it },
                        label = { Text("2FA password") },
                        visualTransformation = PasswordVisualTransformation(),
                        modifier = Modifier.fillMaxWidth(),
                    )
                    Button(onClick = { login.submitPassword(passwordInput); passwordInput = "" }) { Text("Submit password") }
                }
            }
            is LoginState.Failed -> Card(modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp)) {
                    Text("Login failed: ${s.message}", color = MaterialTheme.colorScheme.error)
                }
            }
            is LoginState.Success -> Card(modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text("Logged in and saved below. This account's user ID - give it to whoever is setting up the peer side:")
                    Text(s.userId.toString(), style = MaterialTheme.typography.titleLarge)
                }
            }
            LoginState.Idle -> {}
        }
        if (status.isNotEmpty()) Text(status)

        if (knownAccounts.isNotEmpty()) {
            Text("Logged-in accounts", style = MaterialTheme.typography.titleMedium)
            Text("Add a link for any of these whenever you have the peer's user ID and the channel ID - this list survives even if a login gets interrupted before you get that far.")
            for (acc in knownAccounts) {
                KnownAccountCard(
                    account = acc,
                    onAddLink = { peer, chan ->
                        VtelConfigStore.addAccountLink(activity, config, acc.session, peer, chan)
                        status = "Link added for ${acc.phone}. Check the Links tab, then reconnect to apply."
                        onConfigChanged()
                    },
                )
            }
        }
    }
}

@Composable
private fun KnownAccountCard(account: VtelConfigStore.KnownAccount, onAddLink: (peer: Long, channel: Long) -> Unit) {
    var peerUserId by remember(account.phone) { mutableStateOf("") }
    var channelId by remember(account.phone) { mutableStateOf("") }
    var status by remember(account.phone) { mutableStateOf("") }

    Card(modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("${account.phone}  (user ID ${account.userId})", style = MaterialTheme.typography.titleSmall)
            OutlinedTextField(value = peerUserId, onValueChange = { peerUserId = it }, label = { Text("Peer account user ID") }, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(
                value = channelId,
                onValueChange = { channelId = it },
                label = { Text("Channel ID") },
                supportingText = { Text("Always negative, e.g. -1001234567890 - a private channel both accounts have joined") },
                modifier = Modifier.fillMaxWidth(),
            )
            Button(onClick = {
                val peer = peerUserId.trim().toLongOrNull()
                val chan = channelId.trim().toLongOrNull()
                if (peer == null || chan == null) {
                    status = "Enter valid numeric peer_user_id and channel_id"
                    return@Button
                }
                if (chan > 0) {
                    status = "Channel ID must be negative (Telegram channel/supergroup IDs always start with -100...) - did you drop the leading minus sign?"
                    return@Button
                }
                onAddLink(peer, chan)
                peerUserId = ""; channelId = ""
            }) { Text("Add link") }
            if (status.isNotEmpty()) Text(status)
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
    var debug by remember { mutableStateOf(config.optBoolean("debug", true)) }
    var status by remember { mutableStateOf("") }

    Column(
        modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState()),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
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
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            Switch(checked = debug, onCheckedChange = { debug = it })
            Text(" Verbose debug logging (Logs tab + adb logcat, tag VtelEngine)")
        }
        Button(onClick = {
            config.put("secret", secret)
            config.put("listen", listen)
            config.put("compression_level", compression)
            config.put("reject_ipv6", rejectIPv6)
            config.put("auto_connect", autoConnect)
            config.put("debug", debug)
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
    var showHev by remember { mutableStateOf(false) }
    var lines by remember { mutableStateOf(listOf<String>()) }
    LaunchedEffect(showHev) {
        val fileName = if (showHev) "hev-tun2socks.log" else "vtel.log"
        while (true) {
            val logFile = java.io.File(java.io.File(activity.filesDir, "logs"), fileName)
            lines = runCatching { logFile.readLines().takeLast(300) }.getOrDefault(emptyList())
            delay(1500)
        }
    }
    Column {
        Text("Logs", style = MaterialTheme.typography.titleMedium)
        Row(modifier = Modifier.padding(vertical = 8.dp)) {
            Button(onClick = { showHev = false }, enabled = showHev) { Text("vtel engine") }
            Spacer(modifier = Modifier.width(8.dp))
            Button(onClick = { showHev = true }, enabled = !showHev) { Text("hev tunnel") }
        }
        LazyColumn(modifier = Modifier.fillMaxSize()) {
            items(lines) { line -> Text(line, style = MaterialTheme.typography.bodySmall) }
        }
    }
}
