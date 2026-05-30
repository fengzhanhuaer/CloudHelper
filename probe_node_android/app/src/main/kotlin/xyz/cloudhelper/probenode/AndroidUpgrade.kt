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
import java.io.FileOutputStream
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
    private val statusLock = Any()
    private var status = UpgradeStatus(
        state = "idle",
        phase = "",
        percent = 0,
        message = "尚未执行升级。",
        updatedAt = isoNow(),
    )

    fun checkDownloadAndInstall(activity: Activity, mode: String, config: ProbeNodeConfig, sink: (String) -> Unit) {
        thread(name = "cloudhelper-android-upgrade") {
            val upgradeMode = if (mode == "proxy") "proxy" else "direct"
            try {
                AndroidLogStore.add("upgrade", "upgrade flow started: mode=$upgradeMode")
                updateStatus(state = "running", phase = "prepare", percent = 2, message = "准备 ${upgradeMode} 升级", mode = upgradeMode)
                if (upgradeMode == "proxy" && !config.isReady) {
                    error("controller URL, node ID, and node secret are required for proxy upgrade")
                }
                val currentVersion = currentAppVersion(activity)
                updateStatus(
                    state = "running",
                    phase = "check",
                    percent = 8,
                    message = "正在检查最新 Android APK",
                    mode = upgradeMode,
                    currentVersion = "${currentVersion.name} (${currentVersion.code})",
                )
                sink("Checking latest Android APK via $upgradeMode...")
                val release = fetchLatestAndroidRelease(upgradeMode, config)
                if (release == null) {
                    updateStatus(state = "done", phase = "check", percent = 100, message = "未获取到发布版本信息", mode = upgradeMode)
                    sink("No release metadata found.")
                    return@thread
                }
                if (!isRemoteVersionNewer(currentVersion.name, currentVersion.code, release.tagName)) {
                    val message = "Already up to date. Current=${currentVersion.name} (${currentVersion.code}), latest=${release.tagName}."
                    updateStatus(
                        state = "done",
                        phase = "done",
                        percent = 100,
                        message = message,
                        mode = upgradeMode,
                        currentVersion = "${currentVersion.name} (${currentVersion.code})",
                        latestVersion = release.tagName,
                    )
                    sink(message)
                    return@thread
                }
                val asset = release.asset
                if (asset == null) {
                    updateStatus(
                        state = "failed",
                        phase = "select_asset",
                        percent = 16,
                        message = "No Android arm64 APK asset found.",
                        error = "No Android arm64 APK asset found.",
                        mode = upgradeMode,
                        currentVersion = "${currentVersion.name} (${currentVersion.code})",
                        latestVersion = release.tagName,
                    )
                    sink("No Android arm64 APK asset found.")
                    return@thread
                }
                updateStatus(
                    state = "running",
                    phase = "download",
                    percent = 20,
                    message = "准备下载 ${asset.name}",
                    mode = upgradeMode,
                    currentVersion = "${currentVersion.name} (${currentVersion.code})",
                    latestVersion = release.tagName,
                    assetName = asset.name,
                )
                sink("Downloading ${asset.name} via $upgradeMode. Current=${currentVersion.name}, latest=${release.tagName}.")
                val apk = downloadAsset(activity, asset, upgradeMode, config) { downloaded, total, speed ->
                    val percent = if (total > 0) 20 + ((downloaded.toDouble() / total.toDouble()) * 65.0).toInt().coerceIn(0, 65) else 20
                    updateStatus(
                        state = "running",
                        phase = "download",
                        percent = percent,
                        message = formatDownloadMessage(downloaded, total, speed),
                        mode = upgradeMode,
                        currentVersion = "${currentVersion.name} (${currentVersion.code})",
                        latestVersion = release.tagName,
                        assetName = asset.name,
                        downloadedBytes = downloaded,
                        totalBytes = total,
                        speedBps = speed,
                    )
                }
                updateStatus(
                    state = "running",
                    phase = "install",
                    percent = 92,
                    message = "正在打开 Android 安装器",
                    mode = upgradeMode,
                    currentVersion = "${currentVersion.name} (${currentVersion.code})",
                    latestVersion = release.tagName,
                    assetName = asset.name,
                )
                sink("Opening Android installer...")
                openInstaller(activity, apk)
                updateStatus(
                    state = "done",
                    phase = "install",
                    percent = 100,
                    message = "Installer opened for ${asset.name}.",
                    mode = upgradeMode,
                    currentVersion = "${currentVersion.name} (${currentVersion.code})",
                    latestVersion = release.tagName,
                    assetName = asset.name,
                )
                sink("Installer opened for ${asset.name}.")
                AndroidLogStore.add("upgrade", "installer opened: asset=${asset.name}")
            } catch (e: Exception) {
                AndroidLogStore.add("upgrade", "upgrade failed: ${e.message}", "error")
                updateStatus(state = "failed", phase = "failed", percent = 0, message = "Upgrade failed: ${e.message}", error = e.message ?: "unknown", mode = upgradeMode)
                sink("Upgrade failed: ${e.message}")
            }
        }
    }

    fun statusJSON(): String {
        val current = synchronized(statusLock) { status }
        return JSONObject()
            .put("state", current.state)
            .put("phase", current.phase)
            .put("percent", current.percent)
            .put("message", current.message)
            .put("error", current.error)
            .put("mode", current.mode)
            .put("current_version", current.currentVersion)
            .put("latest_version", current.latestVersion)
            .put("asset_name", current.assetName)
            .put("downloaded_bytes", current.downloadedBytes)
            .put("total_bytes", current.totalBytes)
            .put("speed_bps", current.speedBps)
            .put("updated_at", current.updatedAt)
            .toString()
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

    private fun downloadAsset(activity: Activity, asset: Asset, mode: String, config: ProbeNodeConfig, onProgress: (Long, Long, Long) -> Unit): File {
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
        val total = conn.contentLengthLong
        val startedAt = System.nanoTime()
        responseStream(conn).use { input ->
            FileOutputStream(part, false).use { output ->
                copyWithProgress(input, output) { downloaded ->
                    val elapsedSec = (System.nanoTime() - startedAt).toDouble() / 1_000_000_000.0
                    val speed = if (elapsedSec > 0) (downloaded.toDouble() / elapsedSec).toLong() else 0L
                    onProgress(downloaded, total, speed)
                }
            }
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

    private fun copyWithProgress(input: InputStream, output: FileOutputStream, onProgress: (Long) -> Unit) {
        val buffer = ByteArray(128 * 1024)
        var written = 0L
        var lastReport = 0L
        while (true) {
            val read = input.read(buffer)
            if (read < 0) {
                onProgress(written)
                return
            }
            if (read == 0) {
                continue
            }
            output.write(buffer, 0, read)
            written += read.toLong()
            val now = System.currentTimeMillis()
            if (now - lastReport >= 500L) {
                onProgress(written)
                lastReport = now
            }
        }
    }

    private fun updateStatus(
        state: String,
        phase: String,
        percent: Int,
        message: String,
        error: String = "",
        mode: String = "",
        currentVersion: String = "",
        latestVersion: String = "",
        assetName: String = "",
        downloadedBytes: Long = 0,
        totalBytes: Long = 0,
        speedBps: Long = 0,
    ) {
        synchronized(statusLock) {
            status = status.copy(
                state = state,
                phase = phase,
                percent = percent.coerceIn(0, 100),
                message = message,
                error = error,
                mode = mode.ifBlank { status.mode },
                currentVersion = currentVersion.ifBlank { status.currentVersion },
                latestVersion = latestVersion.ifBlank { status.latestVersion },
                assetName = assetName.ifBlank { status.assetName },
                downloadedBytes = downloadedBytes,
                totalBytes = totalBytes,
                speedBps = speedBps,
                updatedAt = isoNow(),
            )
        }
    }

    private fun formatDownloadMessage(downloaded: Long, total: Long, speed: Long): String {
        return if (total > 0) {
            val percent = ((downloaded.toDouble() * 100.0) / total.toDouble()).toInt().coerceIn(0, 100)
            "下载升级包 ${formatBytes(downloaded)} / ${formatBytes(total)} (${percent}%)，${formatBytes(speed)}/s"
        } else {
            "下载升级包 ${formatBytes(downloaded)}，${formatBytes(speed)}/s"
        }
    }

    private fun formatBytes(value: Long): String {
        var n = if (value < 0) 0.0 else value.toDouble()
        val units = arrayOf("B", "KB", "MB", "GB", "TB")
        var unit = 0
        while (n >= 1024.0 && unit < units.size - 1) {
            n /= 1024.0
            unit += 1
        }
        return if (unit == 0) "${n.toLong()} ${units[unit]}" else String.format(Locale.ROOT, "%.1f %s", n, units[unit])
    }

    private fun isoNow(): String {
        return java.time.Instant.now().toString()
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
    private data class UpgradeStatus(
        val state: String,
        val phase: String,
        val percent: Int,
        val message: String,
        val error: String = "",
        val mode: String = "",
        val currentVersion: String = "",
        val latestVersion: String = "",
        val assetName: String = "",
        val downloadedBytes: Long = 0,
        val totalBytes: Long = 0,
        val speedBps: Long = 0,
        val updatedAt: String = "",
    )
}
