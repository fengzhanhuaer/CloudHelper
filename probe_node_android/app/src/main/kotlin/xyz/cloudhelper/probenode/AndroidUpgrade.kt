package xyz.cloudhelper.probenode

import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.provider.Settings
import androidx.core.content.FileProvider
import org.json.JSONObject
import java.io.InputStream
import java.io.File
import java.net.URLEncoder
import java.net.HttpURLConnection
import java.net.URL
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec
import java.util.Locale
import java.security.SecureRandom
import kotlin.concurrent.thread

object AndroidUpgrade {
    private const val PLATFORM = "android"
    private const val ARCH = "arm64"
    private const val ASSET_NAME = "cloudhelper-probe-node-android-arm64.apk"
    private const val RELEASE_REPO = "fengzhanhuaer/CloudHelper"
    private const val DEFAULT_RELEASE_API = "https://api.github.com/repos/fengzhanhuaer/CloudHelper/releases/latest"

    fun checkDownloadAndInstall(activity: Activity, mode: String, config: ProbeNodeConfig, sink: (String) -> Unit) {
        thread(name = "cloudhelper-android-upgrade") {
            try {
                val upgradeMode = if (mode == "proxy") "proxy" else "direct"
                AndroidLogStore.add("upgrade", "upgrade flow started: mode=$upgradeMode")
                if (upgradeMode == "proxy" && !config.isReady) {
                    error("controller URL, node ID, and node secret are required for proxy upgrade")
                }
                val currentVersion = currentAppVersion(activity)
                sink("Checking latest Android APK via $upgradeMode...")
                val release = fetchLatestAndroidRelease(upgradeMode, config)
                if (release == null) {
                    sink("No release metadata found.")
                    return@thread
                }
                if (!isRemoteVersionNewer(currentVersion.name, currentVersion.code, release.tagName)) {
                    sink("Already up to date. Current=${currentVersion.name} (${currentVersion.code}), latest=${release.tagName}.")
                    return@thread
                }
                val asset = release.asset
                if (asset == null) {
                    sink("No Android arm64 APK asset found.")
                    return@thread
                }
                sink("Downloading ${asset.name} via $upgradeMode. Current=${currentVersion.name}, latest=${release.tagName}.")
                val apk = downloadAsset(activity, asset, upgradeMode, config)
                sink("Opening Android installer...")
                openInstaller(activity, apk)
                sink("Installer opened for ${asset.name}.")
                AndroidLogStore.add("upgrade", "installer opened: asset=${asset.name}")
            } catch (e: Exception) {
                AndroidLogStore.add("upgrade", "upgrade failed: ${e.message}", "error")
                sink("Upgrade failed: ${e.message}")
            }
        }
    }

    private fun fetchLatestAndroidRelease(mode: String, config: ProbeNodeConfig): ReleaseInfo? {
        val requestUrl = if (mode == "proxy") {
            "${config.controllerUrl.trimEnd('/')}/api/probe/proxy/github/latest?project=${urlEncode(RELEASE_REPO)}"
        } else {
            DEFAULT_RELEASE_API
        }
        val conn = openGet(requestUrl, mode, config, "application/vnd.github+json")
        val body = readResponseText(conn, "release api")
        val json = JSONObject(body)
        val tagName = json.optString("tag_name", json.optString("tagName", ""))
        val assets = json.optJSONArray("assets") ?: return ReleaseInfo(tagName, null)
        for (i in 0 until assets.length()) {
            val item = assets.getJSONObject(i)
            val name = item.optString("name", "")
            val url = item.optString("browser_download_url", item.optString("download_url", ""))
            if (matchesAsset(name) && url.isNotBlank()) {
                return ReleaseInfo(tagName, Asset(name, url))
            }
        }
        return ReleaseInfo(tagName, null)
    }

    private fun matchesAsset(name: String): Boolean {
        val value = name.trim().lowercase(Locale.ROOT)
        return value == ASSET_NAME ||
            (value.contains("probe-node") && value.contains(PLATFORM) && value.contains(ARCH) && value.endsWith(".apk"))
    }

    private fun downloadAsset(activity: Activity, asset: Asset, mode: String, config: ProbeNodeConfig): File {
        val dir = File(activity.cacheDir, "upgrades")
        if (!dir.exists() && !dir.mkdirs()) {
            error("failed to create upgrade cache")
        }
        val apk = File(dir, ASSET_NAME)
        val part = File(dir, "$ASSET_NAME.part")
        val requestUrl = if (mode == "proxy") {
            "${config.controllerUrl.trimEnd('/')}/api/probe/proxy/download?url=${urlEncode(asset.url)}"
        } else {
            asset.url
        }
        val conn = openGet(requestUrl, mode, config, "application/octet-stream")
        if (conn.responseCode !in 200..299) {
            error("apk download status=${conn.responseCode}: ${readErrorText(conn)}")
        }
        responseStream(conn).use { input ->
            part.outputStream().use { output -> input.copyTo(output, 64 * 1024) }
        }
        if (apk.exists() && !apk.delete()) {
            error("failed to replace old apk")
        }
        if (!part.renameTo(apk)) {
            error("failed to stage apk")
        }
        return apk
    }

    private fun openInstaller(activity: Activity, apk: File) {
        if (!activity.packageManager.canRequestPackageInstalls()) {
            val intent = Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES)
                .setData(Uri.parse("package:${activity.packageName}"))
            activity.runOnUiThread { activity.startActivity(intent) }
            error("please allow installing unknown apps, then retry")
        }
        val uri = FileProvider.getUriForFile(activity, "${activity.packageName}.files", apk)
        val intent = Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, "application/vnd.android.package-archive")
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        activity.runOnUiThread { activity.startActivity(intent) }
    }

    private fun currentAppVersion(activity: Activity): AppVersion {
        val packageInfo = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            activity.packageManager.getPackageInfo(activity.packageName, PackageManager.PackageInfoFlags.of(0))
        } else {
            @Suppress("DEPRECATION")
            activity.packageManager.getPackageInfo(activity.packageName, 0)
        }
        val code = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            packageInfo.longVersionCode
        } else {
            @Suppress("DEPRECATION")
            packageInfo.versionCode.toLong()
        }
        return AppVersion(packageInfo.versionName ?: "0.0.0", code)
    }

    private fun openGet(requestUrl: String, mode: String, config: ProbeNodeConfig, accept: String): HttpURLConnection {
        val conn = URL(requestUrl).openConnection() as HttpURLConnection
        conn.requestMethod = "GET"
        conn.setRequestProperty("Accept", accept)
        conn.setRequestProperty("User-Agent", "cloudhelper-probe-node-android")
        if (mode == "proxy") {
            applyProbeAuthHeaders(conn, config)
        }
        conn.connectTimeout = 15000
        conn.readTimeout = 60000
        return conn
    }

    private fun readResponseText(conn: HttpURLConnection, label: String): String {
        if (conn.responseCode !in 200..299) {
            error("$label status=${conn.responseCode}: ${readErrorText(conn)}")
        }
        return responseStream(conn).bufferedReader().use { it.readText() }
    }

    private fun responseStream(conn: HttpURLConnection): InputStream {
        return conn.inputStream
    }

    private fun readErrorText(conn: HttpURLConnection): String {
        return try {
            (conn.errorStream ?: conn.inputStream).bufferedReader().use { it.readText() }.take(2048)
        } catch (_: Exception) {
            ""
        }
    }

    private fun applyProbeAuthHeaders(conn: HttpURLConnection, config: ProbeNodeConfig) {
        val timestamp = (System.currentTimeMillis() / 1000L).toString()
        val randomToken = randomHex(16)
        conn.setRequestProperty("X-Probe-Node-Id", config.nodeId.trim())
        conn.setRequestProperty("X-Probe-Timestamp", timestamp)
        conn.setRequestProperty("X-Probe-Rand", randomToken)
        conn.setRequestProperty("X-Probe-Signature", signConnect(config.nodeSecret, config.nodeId, timestamp, randomToken))
    }

    private fun signConnect(secret: String, nodeId: String, timestamp: String, randomToken: String): String {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(secret.trim().toByteArray(Charsets.UTF_8), "HmacSHA256"))
        return mac.doFinal("${nodeId.trim()}\n${timestamp.trim()}\n${randomToken.trim()}".toByteArray(Charsets.UTF_8))
            .joinToString("") { "%02x".format(it) }
    }

    private fun randomHex(size: Int): String {
        val bytes = ByteArray(size)
        SecureRandom().nextBytes(bytes)
        return bytes.joinToString("") { "%02x".format(it) }
    }

    private fun urlEncode(value: String): String {
        return URLEncoder.encode(value, Charsets.UTF_8.name())
    }

    private fun isRemoteVersionNewer(currentName: String, currentCode: Long, remoteTag: String): Boolean {
        val remoteCode = versionCodeFromTag(remoteTag)
        if (remoteCode <= 0) {
            return true
        }
        if (currentCode <= 1L && currentName.contains("dev", ignoreCase = true)) {
            return true
        }
        return remoteCode > currentCode
    }

    private fun versionCodeFromTag(tag: String): Long {
        val match = Regex("""v?(\d+)\.(\d+)\.(\d+).*""").matchEntire(tag.trim()) ?: return 0
        val major = match.groupValues[1].toLongOrNull() ?: return 0
        val minor = match.groupValues[2].toLongOrNull() ?: return 0
        val patch = match.groupValues[3].toLongOrNull() ?: return 0
        return major * 1_000_000L + minor * 1_000L + patch
    }

    private data class AppVersion(val name: String, val code: Long)
    private data class ReleaseInfo(val tagName: String, val asset: Asset?)
    private data class Asset(val name: String, val url: String)
}
