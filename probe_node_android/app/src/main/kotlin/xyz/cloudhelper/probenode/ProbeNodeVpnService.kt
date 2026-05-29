package xyz.cloudhelper.probenode

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import kotlin.concurrent.thread

class ProbeNodeVpnService : VpnService() {
    private var tun: ParcelFileDescriptor? = null

    override fun onCreate() {
        super.onCreate()
        ensureNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopVpn()
            stopSelf()
            return START_NOT_STICKY
        }
        startForeground(NOTIFICATION_ID, buildNotification("正在启动全局 VPN..."))
        startVpn()
        return START_STICKY
    }

    override fun onDestroy() {
        stopVpn()
        super.onDestroy()
    }

    private fun startVpn() {
        val config = ProbeNodeConfig.load(this)
        if (!config.isReady) {
            updateNotification("未配置主控或节点密钥")
            return
        }
        thread(name = "cloudhelper-android-vpn") {
            try {
                MobileCoreBridge.start(this, config)
                val builder = Builder()
                    .setSession("CloudHelper Probe Node")
                    .setMtu(1500)
                    .addAddress("10.111.0.2", 32)
                    .addAddress("fd00:111:111::2", 128)
                    .addRoute("0.0.0.0", 0)
                    .addRoute("::", 0)
                    .addDnsServer("1.1.1.1")
                    .addDnsServer("8.8.8.8")
                try {
                    builder.addDisallowedApplication(packageName)
                } catch (_: Exception) {
                }
                val descriptor = builder.establish()
                if (descriptor == null) {
                    updateNotification("VPN 建立失败：系统未返回 TUN")
                    return@thread
                }
                tun?.close()
                tun = descriptor
                val fd = descriptor.detachFd()
                val result = MobileCoreBridge.vpnStart(this, fd)
                updateNotification("全局 VPN：$result")
            } catch (e: Throwable) {
                updateNotification("VPN 启动失败：${e.message ?: e.javaClass.simpleName}")
            }
        }
    }

    private fun stopVpn() {
        val result = MobileCoreBridge.vpnStop()
        try {
            tun?.close()
        } catch (_: Throwable) {
        }
        tun = null
        updateNotification(result)
        stopForegroundCompat()
    }

    private fun ensureNotificationChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return
        }
        val channel = NotificationChannel(
            CHANNEL_ID,
            "CloudHelper VPN",
            NotificationManager.IMPORTANCE_LOW,
        )
        channel.description = "CloudHelper Android global VPN"
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
            .setContentTitle("CloudHelper VPN")
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
        private const val ACTION_START = "xyz.cloudhelper.probenode.action.VPN_START"
        private const val ACTION_STOP = "xyz.cloudhelper.probenode.action.VPN_STOP"
        private const val CHANNEL_ID = "probe_node_vpn"
        private const val NOTIFICATION_ID = 1002

        fun start(context: Context) {
            val intent = Intent(context, ProbeNodeVpnService::class.java).setAction(ACTION_START)
            ContextCompat.startForegroundService(context, intent)
        }

        fun stop(context: Context) {
            val intent = Intent(context, ProbeNodeVpnService::class.java).setAction(ACTION_STOP)
            context.startService(intent)
        }
    }
}
