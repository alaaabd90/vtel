package app.vtel.client

import android.content.Context
import android.util.Log
import java.io.File
import java.io.OutputStreamWriter
import kotlin.concurrent.thread

// LoginState mirrors the "EVENT ..." protocol `vtel account login -machine`
// speaks on stdout (see cmd/vtel/cmd_account.go's terminalAuth/oneLine) -
// this is the same interactive login flow the CLI drives from a terminal,
// just driven here from Kotlin instead of a human typing at a prompt.
sealed class LoginState {
    object Idle : LoginState()
    object Running : LoginState()
    object NeedCode : LoginState()
    object NeedPassword : LoginState()
    data class Success(val sessionPath: String, val userId: Long) : LoginState()
    data class Failed(val message: String) : LoginState()
}

// VtelAccountLogin runs `libvtel.so account login ... -machine` as a
// subprocess and drives it interactively: it blocks on stdin exactly like a
// terminal session would, so this class supplies the code/password by
// writing to the process's stdin once the UI collects them from the user,
// the same handoff a person retyping the SMS code into a terminal would do.
class VtelAccountLogin(private val context: Context) {
    @Volatile
    var state: LoginState = LoginState.Idle
        private set

    private var process: Process? = null
    private var writer: OutputStreamWriter? = null

    @Synchronized
    fun start(phone: String, apiId: String, apiHash: String, sessionPath: String) {
        cancel()
        state = LoginState.Running

        val engine = VtelEngine.engineBinary(context)
        File(sessionPath).parentFile?.mkdirs()

        val pb = ProcessBuilder(
            engine.absolutePath, "account", "login",
            "-phone", phone,
            "-api-id", apiId,
            "-api-hash", apiHash,
            "-session", sessionPath,
            "-machine",
        ).directory(context.filesDir).redirectErrorStream(true)

        val child = pb.start()
        process = child
        writer = OutputStreamWriter(child.outputStream)

        thread(name = "vtel-account-login", start = true) { pump(child) }
    }

    private fun pump(child: Process) {
        var sessionOut: String? = null
        var userIdOut: Long? = null
        runCatching {
            child.inputStream.bufferedReader().forEachLine { line ->
                Log.d(TAG, line)
                when {
                    line == "EVENT need_code" -> state = LoginState.NeedCode
                    line == "EVENT need_password" -> state = LoginState.NeedPassword
                    line.startsWith("EVENT session ") -> sessionOut = line.removePrefix("EVENT session ").trim()
                    line.startsWith("EVENT user_id ") -> userIdOut = line.removePrefix("EVENT user_id ").trim().toLongOrNull()
                    line == "EVENT done" -> {
                        val s = sessionOut
                        val u = userIdOut
                        state = if (s != null && u != null) {
                            LoginState.Success(s, u)
                        } else {
                            LoginState.Failed("login reported done but session/user_id was missing")
                        }
                    }
                    line.startsWith("EVENT error ") -> state = LoginState.Failed(line.removePrefix("EVENT error ").trim())
                }
            }
        }
        val code = runCatching { child.waitFor() }.getOrDefault(-1)
        if (state == LoginState.Running || state == LoginState.NeedCode || state == LoginState.NeedPassword) {
            state = LoginState.Failed("login process exited (code $code) before completing")
        }
    }

    // submitCode/submitPassword feed one line to the waiting process's
    // stdin - exactly what typing the code/password and pressing enter at a
    // terminal would send. An empty password line is a valid answer (means
    // "this account has no 2FA password"), matching terminalAuth.Password's
    // own stdin contract on the Go side.
    fun submitCode(code: String) = submitLine(code)

    fun submitPassword(password: String) = submitLine(password)

    private fun submitLine(text: String) {
        runCatching {
            writer?.write(text)
            writer?.write("\n")
            writer?.flush()
        }
        state = LoginState.Running
    }

    @Synchronized
    fun cancel() {
        process?.destroy()
        process = null
        writer = null
    }

    companion object {
        private const val TAG = "VtelAccountLogin"
    }
}
