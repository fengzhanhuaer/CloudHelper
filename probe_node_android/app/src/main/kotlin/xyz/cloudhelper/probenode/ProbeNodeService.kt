package xyz.cloudhelper.probenode

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import kotlin.concurrent.thread

class ProbeNodeService : Service() {
    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        ensureNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            val result = MobileCoreBridge.stop()
            updateNotification("已停止：$result")
            stopForegroundCompat()
            stopSelf()
            return START_NOT_STICKY
        }

        startForeground(NOTIFICATION_ID, buildNotification("正在启动长连接..."))
        startLongConnection()
        return START_STICKY
    }

    private fun startLongConnection() {
        val config = ProbeNodeConfig.load(this)
        if (!config.isReady) {
            updateNotification("未配置主控或节点密钥")
            stopForegroundCompat()
            stopSelf()
            return
        }
        thread(name = "cloudhelper-probe-node-service") {
            val startResult = MobileCoreBridge.start(this, config)
            updateNotification("长连接：$startResult")
            val refreshResult = MobileCoreBridge.refreshConfig(this, config)
            updateNotification("长连接：${MobileCoreBridge.status()}；$refreshResult")
        }
    }

    private fun ensureNotificationChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return
        }
        val channel = NotificationChannel(
            CHANNEL_ID,
            "CloudHelper Probe Node",
            NotificationManager.IMPORTANCE_LOW,
        )
        channel.description = "CloudHelper probe node connection status"
        notificationManager().createNotificationChannel(channel)
    }

    private fun updateNotification(status: String) {
        try {
            notificationManager().notify(NOTIFICATION_ID, buildNotification(status))
        } catch (_: SecurityException) {
        }
    }

    private fun buildNotification(status: String): Notification {
        val intent = Intent(this, MainActivity::class.java)
        val pendingIntent = PendingIntent.getActivity(
            this,
            0,
            intent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.stat_sys_upload_done)
            .setContentTitle("CloudHelper Probe Node")
            .setContentText(status)
            .setContentIntent(pendingIntent)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .build()
    }

    private fun notificationManager(): NotificationManager {
        return getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
    }

    private fun stopForegroundCompat() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            stopForeground(STOP_FOREGROUND_REMOVE)
        } else {
            @Suppress("DEPRECATION")
            stopForeground(true)
        }
    }

    companion object {
        private const val ACTION_START = "xyz.cloudhelper.probenode.action.START"
        private const val ACTION_STOP = "xyz.cloudhelper.probenode.action.STOP"
        private const val CHANNEL_ID = "probe_node_service"
        private const val NOTIFICATION_ID = 1001

        fun start(context: Context) {
            val intent = Intent(context, ProbeNodeService::class.java).setAction(ACTION_START)
            ContextCompat.startForegroundService(context, intent)
        }

        fun stop(context: Context) {
            val intent = Intent(context, ProbeNodeService::class.java).setAction(ACTION_STOP)
            context.startService(intent)
        }
    }
}
