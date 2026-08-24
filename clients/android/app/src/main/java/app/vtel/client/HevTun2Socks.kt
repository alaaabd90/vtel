package app.vtel.client

// JNI wrapper for the vendored hev-socks5-tunnel (third_party/, MIT-licensed,
// https://github.com/heiher/hev-socks5-tunnel) - copied verbatim from
// gdrive's own wrapper of the same library (sibling project). Its
// JNI_OnLoad resolves this exact class by name (package app.vtel.client,
// class HevTun2Socks) via the PKGNAME/CLSNAME preprocessor defines baked in
// at native-build time (see app/build.gradle.kts's buildHevTun2socks task)
// - this class's package/name must stay in sync with those build flags.
class HevTun2Socks {
    external fun TProxyStartService(configPath: String, fd: Int)
    external fun TProxyStopService()
    external fun TProxyIsRunning(): Boolean
    external fun TProxyGetStats(): LongArray

    companion object {
        init {
            System.loadLibrary("hev-socks5-tunnel")
        }
    }
}
