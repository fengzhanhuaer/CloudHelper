package xyz.cloudhelper.probenode

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
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
        AndroidLogStore.add("ui", "MainActivity created")
        webView = WebView(this)
        webView.webViewClient = WebViewClient()
        webView.settings.javaScriptEnabled = true
        webView.addJavascriptInterface(AppBridge(), "CloudHelper")
        setContentView(webView)
        webView.loadUrl("file:///android_asset/index.html")
        requestNotificationPermissionIfNeeded()
        startReportServiceIfConfigured()
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == VPN_REQUEST_CODE && resultCode == RESULT_OK) {
            ProbeNodeVpnService.start(this)
            emitStatus("VPN 权限已授权，正在启动全局 VPN...")
        }
    }

    private fun emitStatus(message: String) {
        AndroidLogStore.add("ui", message, if (message.contains("失败") || message.contains("failed", ignoreCase = true)) "error" else "info")
        runOnUiThread {
            webView.evaluateJavascript(
                "window.CloudHelperUI && window.CloudHelperUI.setStatus(${JSONObject.quote(message)});",
                null,
            )
        }
    }

    private fun emitLinkStatus(payload: String) {
        AndroidLogStore.add("link", payload, if (payload.contains("\"ok\":false") || payload.contains("failed", ignoreCase = true) || payload.contains("失败")) "error" else "info")
        runOnUiThread {
            webView.evaluateJavascript(
                "window.CloudHelperUI && window.CloudHelperUI.setLinkStatus(${JSONObject.quote(payload)});",
                null,
            )
        }
    }

    private fun emitVpnStatus(payload: String) {
        AndroidLogStore.add("vpn", payload, if (payload.contains("\"ok\":false") || payload.contains("failed", ignoreCase = true) || payload.contains("失败")) "error" else "info")
        runOnUiThread {
            webView.evaluateJavascript(
                "window.CloudHelperUI && window.CloudHelperUI.setVPNStatus(${JSONObject.quote(payload)});",
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
            AndroidLogStore.add("settings", "config saved: node=${nodeId.trim()}")
            startReportServiceIfConfigured()
            return MobileCoreBridge.status()
        }

        @JavascriptInterface
        fun start(): String {
            AndroidLogStore.add("service", "report service start requested")
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
                AndroidLogStore.add("vpn", "start rejected: controller URL, node ID, and node secret are required", "warn")
                return "controller URL, node ID, and node secret are required"
            }
            val proxyResult = MobileCoreBridge.proxyStart(this@MainActivity, config.controllerUrl)
            val prepareIntent = VpnService.prepare(this@MainActivity)
            if (prepareIntent != null) {
                AndroidLogStore.add("vpn", "VPN permission requested; $proxyResult")
                startActivityForResult(prepareIntent, VPN_REQUEST_CODE)
                return "$proxyResult；需要授权 Android VPN，授权后会自动启动全局 VPN"
            }
            AndroidLogStore.add("vpn", "VPN start requested; $proxyResult")
            ProbeNodeVpnService.start(this@MainActivity)
            return "$proxyResult；全局 VPN 正在启动"
        }

        @JavascriptInterface
        fun stopProxy(): String {
            AndroidLogStore.add("vpn", "VPN stop requested")
            ProbeNodeVpnService.stop(this@MainActivity)
            val proxyResult = MobileCoreBridge.proxyStop()
            return "$proxyResult；全局 VPN 正在停止"
        }

        @JavascriptInterface
        fun status(): String {
            return MobileCoreBridge.status()
        }

        @JavascriptInterface
        fun checkUpgrade(mode: String) {
            AndroidLogStore.add("upgrade", "upgrade check requested: mode=${mode.trim()}")
            AndroidUpgrade.checkDownloadAndInstall(this@MainActivity, mode, ProbeNodeConfig.load(this@MainActivity)) { message -> emitStatus(message) }
        }

        @JavascriptInterface
        fun upgradeStatus(): String {
            return AndroidUpgrade.statusJSON()
        }

        @JavascriptInterface
        fun refreshConfig() {
            AndroidLogStore.add("settings", "manual config refresh requested")
            refreshConfigAsync("手动刷新配置", ProbeNodeConfig.load(this@MainActivity))
        }

        @JavascriptInterface
        fun linkStatus(): String {
            return MobileCoreBridge.linkStatus(this@MainActivity)
        }

        @JavascriptInterface
        fun linkLatency(chainId: String) {
            AndroidLogStore.add("link", "latency test requested: chain=$chainId")
            thread(name = "cloudhelper-android-link-latency") {
                emitLinkStatus(MobileCoreBridge.linkLatency(this@MainActivity, chainId))
            }
        }

        @JavascriptInterface
        fun linkSpeed(chainId: String, protocol: String) {
            AndroidLogStore.add("link", "speed test requested: chain=$chainId protocol=${protocol.ifBlank { "default" }}")
            thread(name = "cloudhelper-android-link-speed") {
                emitLinkStatus(MobileCoreBridge.linkSpeed(this@MainActivity, chainId, protocol))
            }
        }

        @JavascriptInterface
        fun proxyStatus(): String {
            return MobileCoreBridge.proxyStatus(this@MainActivity)
        }

        @JavascriptInterface
        fun vpnStatus(): String {
            return MobileCoreBridge.vpnStatus()
        }

        @JavascriptInterface
        fun vpnSelfCheck(): String {
            AndroidLogStore.add("vpn", "manual VPN self-check requested")
            thread(name = "cloudhelper-android-vpn-self-check") {
                emitVpnStatus(MobileCoreBridge.vpnSelfCheck(this@MainActivity))
            }
            return "VPN 自检已开始"
        }

        @JavascriptInterface
        fun proxySetGroup(group: String, action: String, selectedChainId: String): String {
            AndroidLogStore.add("proxy", "proxy group selection: group=$group action=$action chain=$selectedChainId")
            return MobileCoreBridge.proxySetGroup(this@MainActivity, group, action, selectedChainId)
        }

        @JavascriptInterface
        fun logs(): String {
            return AndroidLogStore.exportJSON()
        }

        @JavascriptInterface
        fun clearLogs(): String {
            AndroidLogStore.clear()
            AndroidLogStore.add("ui", "logs cleared")
            return AndroidLogStore.exportJSON()
        }

        @JavascriptInterface
        fun logEvent(source: String, message: String) {
            AndroidLogStore.add(source, message, if (message.contains("失败") || message.contains("failed", ignoreCase = true)) "error" else "info")
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
            AndroidLogStore.add("service", "starting report service for configured node=${config.nodeId}")
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

    companion object {
        private const val VPN_REQUEST_CODE = 2001
    }
}
