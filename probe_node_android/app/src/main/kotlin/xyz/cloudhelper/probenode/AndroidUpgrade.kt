package xyz.cloudhelper.probenode

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.core.content.FileProvider
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.util.Locale
import kotlin.concurrent.thread

object AndroidUpgrade {
    private const val PLATFORM = "android"
    private const val ARCH = "arm64"
    private const val ASSET_NAME = "cloudhelper-probe-node-android-arm64.apk"
    private const val DEFAULT_RELEASE_API = "https://api.github.com/repos/fengzhanhuaer/CloudHelper/releases/latest"

    fun checkDownloadAndInstall(activity: Activity, sink: (String) -> Unit) {
        thread(name = "cloudhelper-android-upgrade") {
            try {
                sink("Checking latest Android APK...")
                val asset = fetchLatestAndroidAsset()
                if (asset == null) {
                    sink("No Android arm64 APK asset found.")
                    return@thread
                }
                sink("Downloading ${asset.name}...")
                val apk = downloadAsset(activity, asset)
                sink("Opening Android installer...")
                openInstaller(activity, apk)
                sink("Installer opened for ${asset.name}.")
            } catch (e: Exception) {
                sink("Upgrade failed: ${e.message}")
            }
        }
    }

    private fun fetchLatestAndroidAsset(): Asset? {
        val conn = URL(DEFAULT_RELEASE_API).openConnection() as HttpURLConnection
        conn.setRequestProperty("Accept", "application/vnd.github+json")
        conn.setRequestProperty("User-Agent", "cloudhelper-probe-node-android")
        conn.connectTimeout = 12000
        conn.readTimeout = 12000
        if (conn.responseCode !in 200..299) {
            error("release api status=${conn.responseCode}")
        }
        val assets = JSONObject(conn.inputStream.bufferedReader().use { it.readText() }).optJSONArray("assets")
            ?: return null
        for (i in 0 until assets.length()) {
            val item = assets.getJSONObject(i)
            val name = item.optString("name", "")
            val url = item.optString("browser_download_url", "")
            if (matchesAsset(name) && url.isNotBlank()) {
                return Asset(name, url)
            }
        }
        return null
    }

    private fun matchesAsset(name: String): Boolean {
        val value = name.trim().lowercase(Locale.ROOT)
        return value == ASSET_NAME ||
            (value.contains("probe-node") && value.contains(PLATFORM) && value.contains(ARCH) && value.endsWith(".apk"))
    }

    private fun downloadAsset(activity: Activity, asset: Asset): File {
        val dir = File(activity.cacheDir, "upgrades")
        if (!dir.exists() && !dir.mkdirs()) {
            error("failed to create upgrade cache")
        }
        val apk = File(dir, ASSET_NAME)
        val part = File(dir, "$ASSET_NAME.part")
        val conn = URL(asset.url).openConnection() as HttpURLConnection
        conn.setRequestProperty("Accept", "application/octet-stream")
        conn.setRequestProperty("User-Agent", "cloudhelper-probe-node-android")
        conn.connectTimeout = 15000
        conn.readTimeout = 60000
        if (conn.responseCode !in 200..299) {
            error("apk download status=${conn.responseCode}")
        }
        conn.inputStream.use { input ->
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

    private data class Asset(val name: String, val url: String)
}
