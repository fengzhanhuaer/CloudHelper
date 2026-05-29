package xyz.cloudhelper.probenode

import android.Manifest
import android.app.Activity
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.webkit.JavascriptInterface
import android.webkit.WebView
import android.webkit.WebViewClient
import org.json.JSONObject
import kotlin.concurrent.thread

class MainActivity : Activity() {
    private lateinit var webView: WebView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        webView = WebView(this)
        webView.webViewClient = WebViewClient()
        webView.settings.javaScriptEnabled = true
        webView.addJavascriptInterface(AppBridge(), "CloudHelper")
        setContentView(webView)
        webView.loadUrl("file:///android_asset/index.html")
        requestNotificationPermissionIfNeeded()
        startReportServiceIfConfigured()
    }

    private fun emitStatus(message: String) {
        runOnUiThread {
            webView.evaluateJavascript(
                "window.CloudHelperUI && window.CloudHelperUI.setStatus(${JSONObject.quote(message)});",
                null,
            )
        }
    }

    private fun emitLinkStatus(payload: String) {
        runOnUiThread {
            webView.evaluateJavascript(
                "window.CloudHelperUI && window.CloudHelperUI.setLinkStatus(${JSONObject.quote(payload)});",
                null,
            )
        }
    }

    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            return
        }
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) {
            return
        }
        requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1001)
    }

    inner class AppBridge {
        @JavascriptInterface
        fun loadConfig(): String {
            val config = ProbeNodeConfig.load(this@MainActivity)
            return JSONObject()
                .put("controllerUrl", config.controllerUrl)
                .put("nodeId", config.nodeId)
                .put("nodeSecret", config.nodeSecret)
                .put("ready", config.isReady)
                .put("status", MobileCoreBridge.status())
                .put("configDir", ProbeNodeConfig.configDir(this@MainActivity))
                .put("localVersion", currentLocalVersion())
                .toString()
        }

        @JavascriptInterface
        fun saveConfig(controllerUrl: String, nodeId: String, nodeSecret: String): String {
            ProbeNodeConfig.save(this@MainActivity, controllerUrl, nodeId, nodeSecret)
            startReportServiceIfConfigured()
            return MobileCoreBridge.status()
        }

        @JavascriptInterface
        fun start(): String {
            startReportServiceIfConfigured()
            return "report service starting"
        }

        @JavascriptInterface
        fun stop(): String {
            return "report service is managed by Android service"
        }

        @JavascriptInterface
        fun startProxy(): String {
            val config = ProbeNodeConfig.load(this@MainActivity)
            if (!config.isReady) {
                return "controller URL, node ID, and node secret are required"
            }
            return "proxy runtime is not implemented yet; report=${MobileCoreBridge.status()}"
        }

        @JavascriptInterface
        fun stopProxy(): String {
            return "proxy runtime is not implemented yet; report=${MobileCoreBridge.status()}"
        }

        @JavascriptInterface
        fun status(): String {
            return MobileCoreBridge.status()
        }

        @JavascriptInterface
        fun checkUpgrade(mode: String) {
            AndroidUpgrade.checkDownloadAndInstall(this@MainActivity, mode, ProbeNodeConfig.load(this@MainActivity)) { message -> emitStatus(message) }
        }

        @JavascriptInterface
        fun refreshConfig() {
            refreshConfigAsync("手动刷新配置", ProbeNodeConfig.load(this@MainActivity))
        }

        @JavascriptInterface
        fun linkStatus(): String {
            return MobileCoreBridge.linkStatus(this@MainActivity)
        }

        @JavascriptInterface
        fun linkLatency(chainId: String) {
            thread(name = "cloudhelper-android-link-latency") {
                emitLinkStatus(MobileCoreBridge.linkLatency(this@MainActivity, chainId))
            }
        }

        @JavascriptInterface
        fun linkSpeed(chainId: String, protocol: String) {
            thread(name = "cloudhelper-android-link-speed") {
                emitLinkStatus(MobileCoreBridge.linkSpeed(this@MainActivity, chainId, protocol))
            }
        }
    }

    private fun refreshConfigAsync(reason: String, config: ProbeNodeConfig) {
        if (!config.isReady) {
            emitStatus("刷新配置失败：请先保存主控地址、节点 ID 和节点密钥。")
            return
        }
        thread(name = "cloudhelper-android-config-refresh") {
            emitStatus("${reason}：正在从主控拉取配置...")
            val result = MobileCoreBridge.refreshConfig(this@MainActivity, config)
            emitStatus(result)
        }
    }

    private fun startReportServiceIfConfigured() {
        val config = ProbeNodeConfig.load(this)
        if (config.isReady) {
            ProbeNodeService.start(this)
        }
    }

    private fun currentLocalVersion(): String {
        val packageInfo = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            packageManager.getPackageInfo(packageName, PackageManager.PackageInfoFlags.of(0))
        } else {
            @Suppress("DEPRECATION")
            packageManager.getPackageInfo(packageName, 0)
        }
        val code = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            packageInfo.longVersionCode
        } else {
            @Suppress("DEPRECATION")
            packageInfo.versionCode.toLong()
        }
        return "${packageInfo.versionName ?: "0.0.0"} ($code)"
    }
}
