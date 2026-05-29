package xyz.cloudhelper.probenode

import android.app.Activity
import android.os.Bundle
import android.webkit.JavascriptInterface
import android.webkit.WebView
import android.webkit.WebViewClient
import org.json.JSONObject

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
    }

    private fun emitStatus(message: String) {
        runOnUiThread {
            webView.evaluateJavascript(
                "window.CloudHelperUI && window.CloudHelperUI.setStatus(${JSONObject.quote(message)});",
                null,
            )
        }
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
                .toString()
        }

        @JavascriptInterface
        fun saveConfig(controllerUrl: String, nodeId: String, nodeSecret: String): String {
            ProbeNodeConfig.save(this@MainActivity, controllerUrl, nodeId, nodeSecret)
            return MobileCoreBridge.status()
        }

        @JavascriptInterface
        fun start(): String {
            return MobileCoreBridge.start(ProbeNodeConfig.load(this@MainActivity))
        }

        @JavascriptInterface
        fun stop(): String {
            return MobileCoreBridge.stop()
        }

        @JavascriptInterface
        fun status(): String {
            return MobileCoreBridge.status()
        }

        @JavascriptInterface
        fun checkUpgrade() {
            AndroidUpgrade.checkDownloadAndInstall(this@MainActivity) { message -> emitStatus(message) }
        }
    }
}
