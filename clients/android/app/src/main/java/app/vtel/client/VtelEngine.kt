package app.vtel.client

import android.content.Context
import android.util.Log
import java.io.File
import java.net.InetSocketAddress
import java.net.Socket
import java.time.Instant
import java.util.concurrent.TimeUnit
import kotlin.concurrent.thread

// VtelEngine runs vtel's own cmd/vtel binary as a subprocess - the exact
// pattern gdrive's own Android app uses for its Go engine
// (AndroidGdriveEngine.kt in the sibling project): the binary is
// cross-compiled GOOS=android and packaged under jniLibs/<abi>/libvtel.so,
// which Android's APK installer extracts with execute permission (a
// well-known trick for shipping an arbitrary native executable rather than
// a real shared library). Much simpler than gdrive's version: vtel's
// entire CLI surface for this purpose is `-config <path>` - no per-flag
// tuning, GOMAXPROCS/GOMEMLIMIT division across concurrent engines, or
// metrics/observe flags, since vtel-android only ever runs one engine
// process (vtel's own pool already balances across every configured link
// internally - no per-account fan-out the way gdrive has).
class VtelEngine(
    private val context: Context,
    private val onUnexpectedExit: ((Int) -> Unit)? = null,
) {
    private var process: Process? = null

    @Synchronized
    fun start(configJson: String, listenHost: String, listenPort: Int) {
        stop()
        waitForPortRelease(listenHost, listenPort)

        val configFile = writeRuntimeConfig(configJson)
        val engine = File(context.applicationInfo.nativeLibraryDir, ENGINE_NAME)
        check(engine.exists()) { "vtel engine was not packaged at ${engine.absolutePath}" }

        val logsDir = File(context.filesDir, "logs").apply { mkdirs() }
        val logFile = File(logsDir, LOG_FILE_NAME)
        logFile.writeText("")
        Log.i(TAG, "Starting ${engine.absolutePath} on $listenHost:$listenPort")
        appendLogLine(logFile, "android starting listen=$listenHost:$listenPort")

        val processBuilder = ProcessBuilder(engine.absolutePath, "-config", configFile.absolutePath)
            .directory(context.filesDir)
            .redirectErrorStream(true)
            .redirectOutput(ProcessBuilder.Redirect.appendTo(logFile))
        applyGoRuntimeLimits(processBuilder)
        process = processBuilder.start().also { child -> watchProcessExit(child, logFile) }

        Thread.sleep(250)
        process?.let { child ->
            try {
                val code = child.exitValue()
                val tail = tailOf(logFile)
                error("vtel engine exited with code $code\n$tail")
            } catch (_: IllegalThreadStateException) {
                // Still running - good.
            }
        }
    }

    fun waitUntilReady(host: String, port: Int, timeoutMs: Long = 60_000L) {
        val deadline = System.currentTimeMillis() + timeoutMs
        var lastError: Throwable? = null
        while (System.currentTimeMillis() < deadline) {
            ensureProcessAlive()
            try {
                Socket().use { it.connect(InetSocketAddress(host, port), 300) }
                Thread.sleep(300L)
                ensureProcessAlive()
                return
            } catch (error: Throwable) {
                lastError = error
                Thread.sleep(200L)
            }
        }
        error("vtel SOCKS5 listener did not start on $host:$port: ${lastError?.message ?: "timeout"}")
    }

    @Synchronized
    fun isRunning(): Boolean = process?.isAlive == true

    @Synchronized
    fun stop() {
        val child = process
        process = null
        child?.destroy()
        runCatching {
            // vtel's own Client.Stop() notifies the peer (TypeClose per open
            // stream) before tearing down - give it a window comfortably
            // above protocol.shutdownFlushTimeout (10s) before SIGKILL,
            // which can't be caught and would skip that notification.
            if (child?.waitFor(12, TimeUnit.SECONDS) == false) {
                child.destroyForcibly()
                child.waitFor(1, TimeUnit.SECONDS)
            }
        }
    }

    private fun ensureProcessAlive() {
        val child = synchronized(this) { process ?: error("vtel engine is not running") }
        if (child.isAlive) return
        val code = runCatching { child.exitValue() }.getOrDefault(-1)
        val tail = tailOf(File(File(context.filesDir, "logs"), LOG_FILE_NAME))
        synchronized(this) { if (process === child) process = null }
        error("vtel engine exited with code $code\n$tail")
    }

    private fun waitForPortRelease(host: String, port: Int, timeoutMs: Long = 3_000L) {
        val bindHost = if (host == "0.0.0.0") "127.0.0.1" else host
        val deadline = System.currentTimeMillis() + timeoutMs
        while (System.currentTimeMillis() < deadline) {
            val busy = runCatching {
                Socket().use { it.connect(InetSocketAddress(bindHost, port), 150) }
            }.isSuccess
            if (!busy) return
            Thread.sleep(100L)
        }
        error("local SOCKS port is still in use on $bindHost:$port")
    }

    private fun watchProcessExit(child: Process, logFile: File) {
        thread(name = "vtel-engine-watch", start = true) {
            val code = runCatching { child.waitFor() }.getOrNull() ?: return@thread
            val unexpected = synchronized(this) {
                if (process !== child) {
                    false
                } else {
                    process = null
                    true
                }
            }
            if (!unexpected) {
                appendLogLine(logFile, "android stopped code=$code")
                Log.i(TAG, "vtel engine stopped code=$code")
                return@thread
            }
            val tail = tailOf(logFile)
            appendLogLine(logFile, "android exited unexpectedly code=$code")
            Log.w(TAG, "vtel engine exited unexpectedly code=$code\n$tail")
            onUnexpectedExit?.invoke(code)
        }
    }

    // One engine process per app instance (unlike gdrive's per-account
    // fan-out), so no division across concurrent engines is needed - but
    // still avoid Go's default "use every core, no memory ceiling"
    // behavior stepping on the rest of the device (this app's own UI
    // process, the OS, everything else running).
    private fun applyGoRuntimeLimits(processBuilder: ProcessBuilder) {
        val cores = Runtime.getRuntime().availableProcessors().coerceAtLeast(1)
        val activityManager = context.getSystemService(Context.ACTIVITY_SERVICE) as? android.app.ActivityManager
        val memInfo = android.app.ActivityManager.MemoryInfo()
        activityManager?.getMemoryInfo(memInfo)
        val totalMemMB = if (memInfo.totalMem > 0) memInfo.totalMem / (1024 * 1024) else 3072L
        val env = processBuilder.environment()
        env["GOMAXPROCS"] = cores.toString()
        env["GOMEMLIMIT"] = "${(totalMemMB / 2).coerceAtLeast(64L)}MiB"
    }

    private fun writeRuntimeConfig(configJson: String): File {
        val configsDir = File(context.filesDir, "configs").apply { mkdirs() }
        val configFile = File(configsDir, "config.json")
        configFile.writeText(configJson)
        return configFile
    }

    companion object {
        private const val TAG = "VtelEngine"
        private const val ENGINE_NAME = "libvtel.so"
        private const val LOG_FILE_NAME = "vtel.log"

        private fun tailOf(logFile: File): String =
            logFile.takeIf { it.exists() }
                ?.readLines()
                ?.takeLast(12)
                ?.joinToString("\n")
                .orEmpty()

        private fun appendLogLine(logFile: File, message: String) {
            runCatching { logFile.appendText("${Instant.now()} $message\n") }
        }
    }
}
