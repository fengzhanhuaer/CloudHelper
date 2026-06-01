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
        AndroidLogStore.add("vpn", "ProbeNodeVpnService created")
        ensureNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            AndroidLogStore.add("vpn", "VPN service stop action received")
            stopVpn()
            stopSelf()
            return START_NOT_STICKY
        }
        startForeground(NOTIFICATION_ID, buildNotification("正在启动全局 VPN..."))
        AndroidLogStore.add("vpn", "VPN service start action received")
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
            AndroidLogStore.add("vpn", "VPN start rejected: config is not ready", "warn")
            updateNotification("未配置主控或节点密钥")
            return
        }
        thread(name = "cloudhelper-android-vpn") {
            try {
                val startResult = MobileCoreBridge.start(this, config)
                AndroidLogStore.add("vpn", "long connection while VPN starts: $startResult")
                val ipResult = MobileCoreBridge.setNativeIPs(this)
                AndroidLogStore.add("vpn", ipResult)
                val proxyResult = MobileCoreBridge.proxyStart(this, config.controllerUrl)
                AndroidLogStore.add("vpn", "local proxy while VPN starts: $proxyResult")
                val builder = Builder()
                    .setSession("CloudHelper Probe Node")
                    .setMtu(1500)
                    .addAddress("10.111.0.2", 32)
                    .addAddress("fd00:111:111::2", 128)
                    .addRoute("0.0.0.0", 0)
                    .addRoute("::", 0)
                    .addDnsServer("10.111.0.1")
                    .addDnsServer("fd00:111:111::1")
                try {
                    builder.addDisallowedApplication(packageName)
                    AndroidLogStore.add("vpn", "excluded own package from VPN routing: $packageName")
                } catch (e: Exception) {
                    AndroidLogStore.add("vpn", "exclude own package from VPN routing failed: ${e.message ?: e.javaClass.simpleName}", "warn")
                }
                val descriptor = builder.establish()
                if (descriptor == null) {
                    AndroidLogStore.add("vpn", "VPN establish failed: descriptor is null", "error")
                    updateNotification("VPN 建立失败：系统未返回 TUN")
                    return@thread
                }
                tun?.close()
                tun = descriptor
                val fd = descriptor.detachFd()
                val result = MobileCoreBridge.vpnStart(this, fd)
                AndroidLogStore.add("vpn", "VPN mobilecore start result: $result")
                updateNotification("全局 VPN：$result")
            } catch (e: Throwable) {
                AndroidLogStore.add("vpn", "VPN start failed: ${e.message ?: e.javaClass.simpleName}", "error")
                updateNotification("VPN 启动失败：${e.message ?: e.javaClass.simpleName}")
            }
        }
    }

    private fun stopVpn() {
        val result = MobileCoreBridge.vpnStop()
        val proxyResult = MobileCoreBridge.proxyStop()
        AndroidLogStore.add("vpn", "VPN stop result: $result")
        AndroidLogStore.add("vpn", "local proxy stop result: $proxyResult")
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
