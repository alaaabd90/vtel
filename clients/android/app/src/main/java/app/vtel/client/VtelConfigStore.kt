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

    private fun randomSecret(): String {
        val bytes = ByteArray(32)
        SecureRandom().nextBytes(bytes)
        return bytes.joinToString("") { "%02x".format(it) }
    }
}
