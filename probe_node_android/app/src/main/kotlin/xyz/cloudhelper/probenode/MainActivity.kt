package xyz.cloudhelper.probenode

import android.Manifest
import android.app.Activity
import android.content.pm.ApplicationInfo
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.webkit.ConsoleMessage
import android.webkit.WebChromeClient
import android.webkit.JavascriptInterface
import android.webkit.WebView
import android.webkit.WebViewClient
import org.json.JSONObject
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.concurrent.thread

class MainActivity : Activity() {
    private lateinit var webView: WebView
    @Volatile private var cachedStatus: String = "正在读取运行状态..."
    @Volatile private var cachedVpnStatus: String = "{}"
    @Volatile private var cachedLinkStatus: String = JSONObject()
        .put("ok", false)
        .put("error", "正在读取链路配置...")
        .toString()
    private val statusRefreshRunning = AtomicBoolean(false)
    private val linkRefreshRunning = AtomicBoolean(false)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        AndroidLogStore.add("ui", "MainActivity created")
        webView = WebView(this)
        webView.webViewClient = WebViewClient()
        webView.webChromeClient = object : WebChromeClient() {
            override fun onConsoleMessage(consoleMessage: ConsoleMessage): Boolean {
                AndroidLogStore.add(
                    "ui",
                    "console ${consoleMessage.messageLevel()}: ${consoleMessage.message()} @${consoleMessage.sourceId()}:${consoleMessage.lineNumber()}",
                    if (consoleMessage.messageLevel() == ConsoleMessage.MessageLevel.ERROR) "error" else "info",
                )
                return true
            }
        }
        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = true
            allowContentAccess = true
            @Suppress("DEPRECATION")
            allowFileAccessFromFileURLs = true
            @Suppress("DEPRECATION")
            allowUniversalAccessFromFileURLs = true
        }
        webView.addJavascriptInterface(AppBridge(), "CloudHelper")
        setContentView(webView)
        WebView.setWebContentsDebuggingEnabled((applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE) != 0)
        webView.loadUrl("file:///android_asset/status.html")
        requestNotificationPermissionIfNeeded()
        startReportServiceIfConfigured()
        refreshCachedStatusAsync()
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == VPN_REQUEST_CODE && resultCode == RESULT_OK) {
            ProbeNodeVpnService.start(this)
            emitStatus("VPN 权限已授权，正在启动全局 VPN...")
            refreshCachedStatusAsync()
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

    private fun evaluatePageScript(script: String) {
        runOnUiThread {
            webView.evaluateJavascript(script, null)
        }
    }

    private fun refreshCachedStatusAsync() {
        if (!statusRefreshRunning.compareAndSet(false, true)) {
            return
        }
        thread(name = "cloudhelper-android-status-refresh") {
            try {
                cachedStatus = MobileCoreBridge.status()
                cachedVpnStatus = ProbeNodeVpnService.mergedStatusJSON(MobileCoreBridge.vpnStatus())
                evaluatePageScript(
                    """
                    if (window.setText) {
                      setText('summaryRuntime', ${JSONObject.quote(cachedStatus)});
                      setText('status', ${JSONObject.quote(cachedStatus)});
                      setText('settingsStatus', ${JSONObject.quote(cachedStatus)});
                    }
                    if (window.setRuntimeStatus) setRuntimeStatus('运行：' + ${JSONObject.quote(cachedStatus)});
                    if (window.renderVPNDiagnostics && window.parseJSON) renderVPNDiagnostics(parseJSON(${JSONObject.quote(cachedVpnStatus)}));
                    """.trimIndent(),
                )
            } finally {
                statusRefreshRunning.set(false)
            }
        }
    }

    private fun refreshCachedLinkStatusAsync() {
        if (!linkRefreshRunning.compareAndSet(false, true)) {
            return
        }
        thread(name = "cloudhelper-android-link-status-refresh") {
            try {
                cachedLinkStatus = MobileCoreBridge.linkStatus(this@MainActivity)
                evaluatePageScript("if (document.body && document.body.dataset.page === 'link' && window.renderLinkStatus) window.renderLinkStatus(${JSONObject.quote(cachedLinkStatus)});")
            } finally {
                linkRefreshRunning.set(false)
            }
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
                .put("status", cachedStatus)
                .put("configDir", ProbeNodeConfig.configDir(this@MainActivity))
                .put("localVersion", currentLocalVersion())
                .toString()
        }

        @JavascriptInterface
        fun saveConfig(controllerUrl: String, nodeId: String, nodeSecret: String): String {
            ProbeNodeConfig.save(this@MainActivity, controllerUrl, nodeId, nodeSecret)
            AndroidLogStore.add("settings", "config saved: node=${nodeId.trim()}")
            startReportServiceIfConfigured()
            refreshCachedStatusAsync()
            return cachedStatus
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
        fun startVpn(): String {
            val config = ProbeNodeConfig.load(this@MainActivity)
            if (!config.isReady) {
                AndroidLogStore.add("vpn", "start rejected: controller URL, node ID, and node secret are required", "warn")
                return "controller URL, node ID, and node secret are required"
            }
            val prepareIntent = VpnService.prepare(this@MainActivity)
            if (prepareIntent != null) {
                AndroidLogStore.add("vpn", "VPN permission requested")
                startActivityForResult(prepareIntent, VPN_REQUEST_CODE)
                return "需要授权 Android VPN，授权后会自动启动全局 VPN"
            }
            AndroidLogStore.add("vpn", "VPN service start requested")
            ProbeNodeVpnService.start(this@MainActivity)
            refreshCachedStatusAsync()
            return "全局 VPN 正在启动..."
        }

        @JavascriptInterface
        fun stopVpn(): String {
            AndroidLogStore.add("vpn", "VPN stop requested")
            ProbeNodeVpnService.stop(this@MainActivity)
            refreshCachedStatusAsync()
            return "全局 VPN 正在停止..."
        }

        @JavascriptInterface
        fun status(): String {
            refreshCachedStatusAsync()
            return cachedStatus
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
            refreshCachedLinkStatusAsync()
            return cachedLinkStatus
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
        fun vpnStatus(): String {
            refreshCachedStatusAsync()
            return ProbeNodeVpnService.mergedStatusJSON(cachedVpnStatus)
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
