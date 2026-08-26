package app.vtel.client

import android.content.Context
import java.io.File
import java.security.SecureRandom
import org.json.JSONArray
import org.json.JSONObject

// VtelConfigStore reads/writes the same JSON config shape vtel's Go side
// uses (see vtelconfig.Config) - stored in the app's private files dir, one
// config per app install, mirroring cmd/vtel-desktop's single-config model
// (vtel's own pool already load-balances across every configured link
// internally, so there's no multi-profile concept to build here either).
object VtelConfigStore {
    private const val FILE_NAME = "vtel-config.json"

    fun path(context: Context): File = File(context.filesDir, FILE_NAME)

    fun loadOrInit(context: Context): JSONObject {
        val file = path(context)
        if (!file.exists()) {
            val fresh = JSONObject().apply {
                put("mode", "client")
                put("listen", "127.0.0.1:1080")
                put("secret", randomSecret())
                put("compression_level", "fastest")
                put("reject_ipv6", false)
                put("quiet_hours", JSONObject.NULL)
                put("auto_connect", false)
                // On by default for this testing phase (see the Settings
                // screen's toggle to turn it off later) - surfaces every
                // vtel core package's [debug] trace via VtelEngine's log
                // tee to both the Logs tab and adb logcat.
                put("debug", true)
                put("links", JSONArray())
            }
            file.writeText(fresh.toString(2))
            return fresh
        }
        return JSONObject(file.readText())
    }

    fun save(context: Context, config: JSONObject) {
        path(context).writeText(config.toString(2))
    }

    // addAccountLink is the only guided way this app builds a link: a real
    // MTProto account (see VtelAccountLogin) rather than a bot. kind:
    // "account" matches vtelconfig.LinkConfig.IsAccount on the Go side - a
    // bot-kind link (token/peer_bot_id) is still a valid shape the Go side
    // accepts (e.g. from a config pasted via Import), just not one this
    // app's own UI offers to build anymore.
    fun addAccountLink(context: Context, config: JSONObject, session: String, peerUserId: Long, channelId: Long): JSONObject {
        val links = config.optJSONArray("links") ?: JSONArray()
        links.put(
            JSONObject().apply {
                put("kind", "account")
                put("session", session)
                put("peer_user_id", peerUserId)
                put("channel_id", channelId)
            },
        )
        config.put("links", links)
        save(context, config)
        return config
    }

    fun removeLink(context: Context, config: JSONObject, index: Int): JSONObject {
        val links = config.optJSONArray("links") ?: JSONArray()
        val kept = JSONArray()
        for (i in 0 until links.length()) {
            if (i != index) kept.put(links.getJSONObject(i))
        }
        config.put("links", kept)
        save(context, config)
        return config
    }

    fun linkCount(config: JSONObject): Int = config.optJSONArray("links")?.length() ?: 0

    fun redactToken(token: String): String =
        if (token.length <= 10) "****" else token.take(6) + "..." + token.takeLast(4)

    // getApiId/getApiHash/setApiCredentials manage telegram_api_id/
    // telegram_api_hash - the MTProto app credentials from
    // https://my.telegram.org every account-kind link needs (see
    // vtelconfig.Config.TelegramAPIID/TelegramAPIHash), one pair shared by
    // every account link in this config. The Account screen saves them here
    // once so a second login doesn't require re-entering them.
    fun getApiId(config: JSONObject): Int = config.optInt("telegram_api_id", 0)

    fun getApiHash(config: JSONObject): String = config.optString("telegram_api_hash", "")

    fun setApiCredentials(context: Context, config: JSONObject, apiId: Int, apiHash: String): JSONObject {
        config.put("telegram_api_id", apiId)
        config.put("telegram_api_hash", apiHash)
        save(context, config)
        return config
    }

    // sessionPathFor is where a phone number's session file (written by
    // VtelAccountLogin) lives - the app's own private storage, so no
    // cross-app file access is needed the way it would be if this pointed
    // at a path like /root/vtel/accounts/... (that path doesn't exist and
    // wouldn't be accessible from this app's sandbox anyway).
    fun sessionPathFor(context: Context, phone: String): String {
        val dir = File(context.filesDir, "accounts").apply { mkdirs() }
        return File(dir, sanitizePhoneForFilename(phone) + ".session").absolutePath
    }

    private fun sanitizePhoneForFilename(phone: String): String =
        phone.filter { it.isDigit() || it == '+' }

    // Known accounts: every phone number successfully logged in through
    // this app, recorded the instant login succeeds - independent of
    // vtel-config.json's links array and independent of the Account
    // screen's own transient state. This is what lets the app remember a
    // completed login even if the process is killed (backgrounding, low
    // memory) before the user gets to fill in the peer_user_id/channel_id
    // and actually add the link - without it, a reset mid-flow forced
    // redoing the SMS code just to get back to the "add link" form, even
    // though the login itself, and its session file, were already done.
    private const val ACCOUNTS_FILE_NAME = "vtel-known-accounts.json"

    data class KnownAccount(val phone: String, val session: String, val userId: Long)

    private fun accountsFile(context: Context): File = File(context.filesDir, ACCOUNTS_FILE_NAME)

    fun loadKnownAccounts(context: Context): List<KnownAccount> {
        val file = accountsFile(context)
        if (!file.exists()) return emptyList()
        val arr = runCatching { JSONArray(file.readText()) }.getOrElse { return emptyList() }
        return (0 until arr.length()).map { i ->
            val o = arr.getJSONObject(i)
            KnownAccount(o.optString("phone"), o.optString("session"), o.optLong("user_id"))
        }
    }

    fun addKnownAccount(context: Context, phone: String, session: String, userId: Long) {
        // A re-login of the same phone replaces its old entry rather than
        // duplicating it.
        val updated = loadKnownAccounts(context).filterNot { it.phone == phone } +
            KnownAccount(phone, session, userId)
        val arr = JSONArray()
        updated.forEach { acc ->
            arr.put(
                JSONObject().apply {
                    put("phone", acc.phone)
                    put("session", acc.session)
                    put("user_id", acc.userId)
                },
            )
        }
        accountsFile(context).writeText(arr.toString(2))
    }

    private fun randomSecret(): String {
        val bytes = ByteArray(32)
        SecureRandom().nextBytes(bytes)
        return bytes.joinToString("") { "%02x".format(it) }
    }
}
