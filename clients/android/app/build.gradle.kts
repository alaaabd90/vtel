plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

val repoRoot = layout.projectDirectory.dir("../../..")
val generatedVtelJniLibs = layout.buildDirectory.dir("generated/vtel-go/jniLibs")
val generatedHevJniLibs = layout.buildDirectory.dir("generated/hev-tun2socks/jniLibs")
val hevSourceDir = repoRoot.dir("third_party/hev-socks5-tunnel")
val vtelAppVersion = providers.gradleProperty("vtel.version").orElse("1.0.0").get()
val releaseKeystorePath = providers.environmentVariable("VTEL_ANDROID_KEYSTORE_FILE").orNull
val releaseKeystorePassword = providers.environmentVariable("VTEL_ANDROID_KEYSTORE_PASSWORD").orNull
val releaseKeyAlias = providers.environmentVariable("VTEL_ANDROID_KEY_ALIAS").orNull
val releaseKeyPassword = providers.environmentVariable("VTEL_ANDROID_KEY_PASSWORD").orNull
val hasReleaseSigning = listOf(
    releaseKeystorePath,
    releaseKeystorePassword,
    releaseKeyAlias,
    releaseKeyPassword,
).all { !it.isNullOrBlank() }
val androidNativeAbis = listOf("arm64-v8a", "armeabi-v7a")
val goAndroidTargets = listOf(
    Triple("arm64-v8a", "arm64", ""),
    Triple("armeabi-v7a", "arm", "7"),
)

// buildVtelAndroidSidecar cross-compiles vtel's own cmd/vtel binary as an
// Android native executable, packaged as a JNI lib (Android's APK
// installer extracts anything under jniLibs/ with execute permission, a
// well-known trick for shipping an arbitrary native executable rather than
// a real shared library) - the same pattern gdrive's own Android app uses
// for its Go engine (buildGdriveAndroidSidecar in the sibling project).
// vtel's engine is simpler to invoke than gdrive's: `-config <path>` is
// its entire CLI surface, already exactly what VtelEngine.kt needs.
val buildVtelAndroidSidecar = tasks.register("buildVtelAndroidSidecar") {
    group = "build"
    description = "Build the vtel Go engine as Android native executables packaged as JNI libs."
    inputs.dir(repoRoot.dir("cmd"))
    inputs.dir(repoRoot.dir("protocol"))
    inputs.dir(repoRoot.dir("tunnel"))
    inputs.dir(repoRoot.dir("pool"))
    inputs.dir(repoRoot.dir("telegram"))
    inputs.dir(repoRoot.dir("socks5"))
    inputs.dir(repoRoot.dir("vtelconfig"))
    inputs.file(repoRoot.file("go.mod"))
    inputs.property("vtelAppVersion", vtelAppVersion)
    outputs.dir(generatedVtelJniLibs)

    doLast {
        val sdkRoot = androidSdkRoot()
        val ndkRoot = sdkRoot.resolve("ndk/${android.ndkVersion}")
        check(ndkRoot.exists()) { "Android NDK was not found at ${ndkRoot.absolutePath}" }
        val hostTag = when {
            org.gradle.internal.os.OperatingSystem.current().isLinux -> "linux-x86_64"
            org.gradle.internal.os.OperatingSystem.current().isMacOsX -> "darwin-x86_64"
            org.gradle.internal.os.OperatingSystem.current().isWindows -> "windows-x86_64"
            else -> error("Unsupported host OS for Android NDK clang")
        }
        val toolchainBin = ndkRoot.resolve("toolchains/llvm/prebuilt/$hostTag/bin")
        val clangSuffix = if (org.gradle.internal.os.OperatingSystem.current().isWindows) ".cmd" else ""
        val cCompilers = mapOf(
            "arm64-v8a" to toolchainBin.resolve("aarch64-linux-android26-clang$clangSuffix").absolutePath,
            "armeabi-v7a" to toolchainBin.resolve("armv7a-linux-androideabi26-clang$clangSuffix").absolutePath,
        )
        goAndroidTargets.forEach { (abi, goArch, goArm) ->
            val outputDir = generatedVtelJniLibs.get().dir(abi).asFile
            outputDir.mkdirs()
            exec {
                workingDir = repoRoot.asFile
                executable = "go"
                args(
                    "build",
                    "-trimpath",
                    "-buildmode=pie",
                    "-ldflags",
                    "-s -w -X main.version=android-$vtelAppVersion -linkmode=external -extldflags=-Wl,-z,max-page-size=16384,-z,common-page-size=16384",
                    "-o",
                    outputDir.resolve("libvtel.so").absolutePath,
                    "./cmd/vtel",
                )
                environment("GOOS", "android")
                environment("GOARCH", goArch)
                if (goArm.isNotBlank()) {
                    environment("GOARM", goArm)
                }
                environment("CGO_ENABLED", "1")
                environment("CC", requireNotNull(cCompilers[abi]) { "missing Android compiler for $abi" })
            }
        }
    }
}

fun androidSdkRoot(): File {
    val explicit = providers.gradleProperty("android.sdk.path").orNull
    val env = System.getenv("ANDROID_HOME") ?: System.getenv("ANDROID_SDK_ROOT")
    val local = rootProject.file("local.properties")
        .takeIf { it.exists() }
        ?.readLines()
        ?.firstOrNull { it.startsWith("sdk.dir=") }
        ?.substringAfter("sdk.dir=")
    return File(explicit ?: env ?: local ?: error("Android SDK path was not found"))
}

// buildHevTun2socks builds the vendored hev-socks5-tunnel (third_party/,
// MIT-licensed, from https://github.com/heiher/hev-socks5-tunnel) TUN-to-
// SOCKS5 bridge. Its JNI_OnLoad resolves the target Kotlin class by name at
// load time (FindClass(PKGNAME "/" CLSNAME)), baked in via these
// APP_CFLAGS - so it must be built fresh for vtel's own package/class,
// gdrive's prebuilt .so (compiled for app/gdrive/client.HevTun2Socks)
// cannot be reused as-is.
val buildHevTun2socks = tasks.register("buildHevTun2socks") {
    group = "build"
    description = "Build the Android TUN-to-SOCKS bridge used by VPN mode."
    inputs.dir(hevSourceDir)
    outputs.dir(generatedHevJniLibs)

    doLast {
        val sdkRoot = androidSdkRoot()
        val ndkBuild = sdkRoot.resolve("ndk/${android.ndkVersion}/ndk-build")
        check(ndkBuild.exists()) { "ndk-build was not found at ${ndkBuild.absolutePath}" }

        val appMk = temporaryDir.resolve("VtelApplication.mk")
        appMk.writeText(
            """
            APP_PLATFORM := android-26
            APP_OPTIM := release
            APP_ABI := ${androidNativeAbis.joinToString(" ")}
            APP_CFLAGS := -O3 -DPKGNAME=app/vtel/client -DCLSNAME=HevTun2Socks
            APP_SUPPORT_FLEXIBLE_PAGE_SIZES := true
            NDK_TOOLCHAIN_VERSION := clang
            """.trimIndent() + "\n",
        )

        exec {
            environment("ANDROID_HOME", sdkRoot.absolutePath)
            environment("ANDROID_SDK_ROOT", sdkRoot.absolutePath)
            workingDir = hevSourceDir.asFile
            commandLine(
                ndkBuild.absolutePath,
                "NDK_PROJECT_PATH=.",
                "NDK_APPLICATION_MK=${appMk.absolutePath}",
                "APP_BUILD_SCRIPT=${hevSourceDir.file("Android.mk").asFile.absolutePath}",
                "V=0",
            )
        }

        androidNativeAbis.forEach { abi ->
            val outputDir = generatedHevJniLibs.get().dir(abi).asFile
            outputDir.mkdirs()
            hevSourceDir.file("libs/$abi/libhev-socks5-tunnel.so").asFile
                .copyTo(outputDir.resolve("libhev-socks5-tunnel.so"), overwrite = true)
        }
    }
}

android {
    namespace = "app.vtel.client"
    compileSdk = 35
    ndkVersion = "27.0.12077973"

    defaultConfig {
        applicationId = "app.vtel.client"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = vtelAppVersion

        ndk {
            abiFilters += androidNativeAbis
        }
    }

    splits {
        abi {
            isEnable = true
            reset()
            include(*androidNativeAbis.toTypedArray())
            isUniversalApk = true
        }
    }

    signingConfigs {
        if (hasReleaseSigning) {
            create("release") {
                storeFile = file(requireNotNull(releaseKeystorePath))
                storePassword = releaseKeystorePassword
                keyAlias = releaseKeyAlias
                keyPassword = releaseKeyPassword
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            isShrinkResources = false
            if (hasReleaseSigning) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    sourceSets {
        getByName("main") {
            jniLibs.srcDir(generatedVtelJniLibs)
            jniLibs.srcDir(generatedHevJniLibs)
        }
    }

    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }
}

tasks.named("preBuild") {
    dependsOn(buildVtelAndroidSidecar)
    dependsOn(buildHevTun2socks)
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2024.12.01")
    implementation(composeBom)
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
    debugImplementation("androidx.compose.ui:ui-tooling")
}
