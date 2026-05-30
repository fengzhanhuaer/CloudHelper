package xyz.cloudhelper.probenode

import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.util.ArrayDeque

object AndroidLogStore {
    private const val MAX_ENTRIES = 300
    private val entries = ArrayDeque<LogEntry>()

    @Synchronized
    fun add(source: String, message: String, level: String = "info") {
        val text = message.trim()
        if (text.isEmpty()) {
            return
        }
        entries.addLast(
            LogEntry(
                time = Instant.now().toString(),
                level = level.trim().ifEmpty { "info" },
                source = source.trim().ifEmpty { "android" },
                message = text.take(4096),
            ),
        )
        while (entries.size > MAX_ENTRIES) {
            entries.removeFirst()
        }
    }

    @Synchronized
    fun exportJSON(): String {
        val items = JSONArray()
        entries.forEach { entry ->
            items.put(
                JSONObject()
                    .put("time", entry.time)
                    .put("level", entry.level)
                    .put("source", entry.source)
                    .put("message", entry.message),
            )
        }
        return JSONObject()
            .put("ok", true)
            .put("entries", items)
            .toString()
    }

    @Synchronized
    fun clear() {
        entries.clear()
    }

    private data class LogEntry(
        val time: String,
        val level: String,
        val source: String,
        val message: String,
    )
}
