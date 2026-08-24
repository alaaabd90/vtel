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

    fun addLink(context: Context, config: JSONObject, token: String, peerBotId: Long, channelId: Long): JSONObject {
        val links = config.optJSONArray("links") ?: JSONArray()
        links.put(
            JSONObject().apply {
                put("token", token)
                put("peer_bot_id", peerBotId)
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

    private fun randomSecret(): String {
        val bytes = ByteArray(32)
        SecureRandom().nextBytes(bytes)
        return bytes.joinToString("") { "%02x".format(it) }
    }
}
