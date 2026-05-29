package xyz.cloudhelper.probenode

import android.content.Context
import java.io.File

data class ProbeNodeConfig(
    val controllerUrl: String,
    val nodeId: String,
    val nodeSecret: String,
) {
    val isReady: Boolean
        get() = controllerUrl.isNotBlank() && nodeId.isNotBlank() && nodeSecret.isNotBlank()

    companion object {
        private const val PREFS_NAME = "probe_node_config"
        private const val KEY_CONTROLLER_URL = "controller_url"
        private const val KEY_NODE_ID = "node_id"
        private const val KEY_NODE_SECRET = "node_secret"

        fun load(context: Context): ProbeNodeConfig {
            val prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            return ProbeNodeConfig(
                prefs.getString(KEY_CONTROLLER_URL, "") ?: "",
                prefs.getString(KEY_NODE_ID, "") ?: "",
                prefs.getString(KEY_NODE_SECRET, "") ?: "",
            )
        }

        fun save(context: Context, controllerUrl: String, nodeId: String, nodeSecret: String) {
            context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
                .edit()
                .putString(KEY_CONTROLLER_URL, controllerUrl.trim())
                .putString(KEY_NODE_ID, nodeId.trim())
                .putString(KEY_NODE_SECRET, nodeSecret.trim())
                .apply()
        }

        fun configDir(context: Context): String {
            return File(context.filesDir, "cloudhelper_config").absolutePath
        }
    }
}
