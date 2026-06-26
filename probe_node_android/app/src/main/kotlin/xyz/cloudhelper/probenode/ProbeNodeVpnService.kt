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
import org.json.JSONObject
import kotlin.concurrent.thread

class ProbeNodeVpnService : VpnService() {
    private var tun: ParcelFileDescriptor? = null
    @Volatile private var starting = false
    @Volatile private var vpnEstablished = false
    @Volatile private var dataPlaneRunning = false
    @Volatile private var startGeneration = 0

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
        if (dataPlaneRunning) {
            updateRuntimeState(running = true, starting = false, phase = "connected", message = "全局 VPN 已连接")
            startForeground(NOTIFICATION_ID, buildNotification("全局 VPN 已连接"))
            AndroidLogStore.add("vpn", "VPN service start ignored: already connected")
            return START_STICKY
        }
        if (starting) {
            updateRuntimeState(running = false, starting = true, phase = "starting_data_plane", message = "全局 VPN 正在启动数据面...")
            startForeground(NOTIFICATION_ID, buildNotification("全局 VPN 正在启动数据面..."))
            AndroidLogStore.add("vpn", "VPN service start ignored: startup already running")
            return START_STICKY
        }
        updateRuntimeState(running = false, starting = true, phase = "starting", message = "正在启动全局 VPN...")
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
            updateRuntimeState(running = false, starting = false, phase = "not_configured", message = "未配置主控或节点密钥")
            updateNotification("未配置主控或节点密钥")
            return
        }
        val generation = nextStartGeneration()
        starting = true
        thread(name = "cloudhelper-android-vpn") {
            try {
                if (!isStartGenerationCurrent(generation)) {
                    return@thread
                }
                dataPlaneRunning = false
                if (vpnEstablished) {
                    AndroidLogStore.add("vpn", "restarting VPN data plane from non-running established state", "warn")
                    val stopResult = MobileCoreBridge.vpnStop()
                    AndroidLogStore.add("vpn", "previous VPN data plane stop before restart: $stopResult")
                    vpnEstablished = false
                }
                updateRuntimeState(running = false, starting = true, phase = "connect_controller", message = "正在连接主控...")
                updateNotification("正在连接主控...")
                val startResult = MobileCoreBridge.start(this@ProbeNodeVpnService, config)
                AndroidLogStore.add("vpn", "long connection while VPN starts: $startResult")
                updateRuntimeState(running = false, starting = true, phase = "prepare_network", message = "正在准备本地网络...")
                updateNotification("正在准备本地网络...")
                val ipResult = MobileCoreBridge.setNativeIPs(this@ProbeNodeVpnService)
                AndroidLogStore.add("vpn", ipResult)
                updateRuntimeState(running = false, starting = true, phase = "start_proxy", message = "正在启动本地代理...")
                updateNotification("正在启动本地代理...")
                val proxyResult = MobileCoreBridge.proxyStart(this@ProbeNodeVpnService, config.controllerUrl)
                AndroidLogStore.add("vpn", "local proxy while VPN starts: $proxyResult")
                updateRuntimeState(running = false, starting = true, phase = "establish_vpn", message = "正在建立 Android VPN...")
                updateNotification("正在建立 Android VPN...")
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
                    updateRuntimeState(running = false, starting = false, phase = "establish_failed", message = "VPN 建立失败：系统未返回 TUN")
                    updateNotification("VPN 建立失败：系统未返回 TUN")
                    return@thread
                }
                closePendingDescriptor()
                tun = descriptor
                vpnEstablished = true
                updateRuntimeState(running = true, starting = true, phase = "start_data_plane", message = "全局 VPN 已建立，正在启动数据面...")
                updateNotification("全局 VPN 已建立，正在启动数据面...")
                val fd = descriptor.detachFd()
                tun = null
                AndroidLogStore.add("vpn", "VPN tun fd detached for mobilecore data plane: fd=$fd")
                val nativeThread = thread(name = "cloudhelper-android-vpn-native-start") {
                    try {
                        val result = MobileCoreBridge.vpnStart(this@ProbeNodeVpnService, fd)
                        if (!isStartGenerationCurrent(generation)) {
                            AndroidLogStore.add("vpn", "VPN mobilecore start result ignored for stale generation: $result")
                            return@thread
                        }
                        AndroidLogStore.add("vpn", "VPN mobilecore start result: $result")
                        if (result.contains("failed", ignoreCase = true) || result.contains("失败")) {
                            vpnEstablished = false
                            dataPlaneRunning = false
                            updateRuntimeState(running = false, starting = false, phase = "data_plane_failed", message = "VPN 数据面启动失败：$result", error = result)
                            updateNotification("VPN 数据面启动失败：$result")
                            return@thread
                        }
                        dataPlaneRunning = true
                        updateRuntimeState(running = true, starting = false, phase = "running", message = "全局 VPN：$result")
                        updateNotification("全局 VPN：$result")
                    } catch (e: Throwable) {
                        if (!isStartGenerationCurrent(generation)) {
                            AndroidLogStore.add("vpn", "VPN mobilecore start failure ignored for stale generation: ${e.message ?: e.javaClass.simpleName}", "warn")
                            return@thread
                        }
                        vpnEstablished = false
                        dataPlaneRunning = false
                        AndroidLogStore.add("vpn", "VPN mobilecore start failed: ${e.message ?: e.javaClass.simpleName}", "error")
                        updateRuntimeState(running = false, starting = false, phase = "data_plane_failed", message = "VPN 数据面启动失败：${e.message ?: e.javaClass.simpleName}", error = e.message ?: e.javaClass.simpleName)
                        updateNotification("VPN 数据面启动失败：${e.message ?: e.javaClass.simpleName}")
                    }
                }
                nativeThread.join(DATA_PLANE_START_CONFIRM_TIMEOUT_MS)
                if (nativeThread.isAlive) {
                    if (!isStartGenerationCurrent(generation)) {
                        return@thread
                    }
                    if (isCoreVpnRunning()) {
                        dataPlaneRunning = true
                        AndroidLogStore.add("vpn", "VPN mobilecore start still running but core status is running", "warn")
                        updateRuntimeState(running = true, starting = false, phase = "running", message = "全局 VPN：vpn running")
                        updateNotification("全局 VPN：vpn running")
                        return@thread
                    }
                    AndroidLogStore.add("vpn", "VPN mobilecore start is still running after ${DATA_PLANE_START_CONFIRM_TIMEOUT_MS}ms", "warn")
                    updateRuntimeState(running = true, starting = false, phase = "data_plane_pending", message = "全局 VPN 已建立，数据面启动确认超时，正在后台继续确认...")
                    updateNotification("全局 VPN 已建立，数据面仍在确认...")
                    return@thread
                }
            } catch (e: Throwable) {
                if (!isStartGenerationCurrent(generation)) {
                    AndroidLogStore.add("vpn", "VPN start failure ignored for stale generation: ${e.message ?: e.javaClass.simpleName}", "warn")
                    return@thread
                }
                AndroidLogStore.add("vpn", "VPN start failed: ${e.message ?: e.javaClass.simpleName}", "error")
                updateRuntimeState(running = vpnEstablished, starting = false, phase = "failed", message = "VPN 启动失败：${e.message ?: e.javaClass.simpleName}", error = e.message ?: e.javaClass.simpleName)
                updateNotification("VPN 启动失败：${e.message ?: e.javaClass.simpleName}")
            } finally {
                if (isStartGenerationCurrent(generation)) {
                    starting = false
                }
            }
        }
    }

    private fun stopVpn() {
        invalidateStartGeneration()
        starting = false
        vpnEstablished = false
        dataPlaneRunning = false
        val result = MobileCoreBridge.vpnStop()
        val proxyResult = MobileCoreBridge.proxyStop()
        AndroidLogStore.add("vpn", "VPN stop result: $result")
        AndroidLogStore.add("vpn", "local proxy stop result: $proxyResult")
        closePendingDescriptor()
        tun = null
        updateRuntimeState(running = false, starting = false, phase = "stopped", message = result)
        updateNotification(result)
        stopForegroundCompat()
    }

    private fun closePendingDescriptor() {
        try {
            tun?.close()
        } catch (_: Throwable) {
        }
    }

    private fun nextStartGeneration(): Int = synchronized(this) {
        startGeneration += 1
        startGeneration
    }

    private fun invalidateStartGeneration() {
        synchronized(this) {
            startGeneration += 1
        }
    }

    private fun isStartGenerationCurrent(generation: Int): Boolean {
        return startGeneration == generation
    }

    private fun isCoreVpnRunning(): Boolean {
        return try {
            val status = JSONObject(MobileCoreBridge.vpnStatus())
            status.optBoolean("running", false) || status.optString("status").equals("running", ignoreCase = true)
        } catch (_: Throwable) {
            false
        }
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
        private const val DATA_PLANE_START_CONFIRM_TIMEOUT_MS = 5000L
        @Volatile private var runtimeRunning: Boolean = false
        @Volatile private var runtimeStarting: Boolean = false
        @Volatile private var runtimeDataPlaneRunning: Boolean = false
        @Volatile private var runtimePhase: String = "stopped"
        @Volatile private var runtimeMessage: String = "未启动"
        @Volatile private var runtimeError: String = ""

        fun start(context: Context) {
            updateRuntimeState(running = false, starting = true, phase = "starting", message = "正在启动全局 VPN...")
            val intent = Intent(context, ProbeNodeVpnService::class.java).setAction(ACTION_START)
            ContextCompat.startForegroundService(context, intent)
        }

        fun stop(context: Context) {
            val intent = Intent(context, ProbeNodeVpnService::class.java).setAction(ACTION_STOP)
            context.startService(intent)
        }

        fun mergedStatusJSON(corePayload: String): String {
            val json = try {
                JSONObject(corePayload.ifBlank { "{}" })
            } catch (_: Throwable) {
                JSONObject().put("core_status", corePayload)
            }
            json.put("android_running", runtimeRunning)
            json.put("android_starting", runtimeStarting)
            json.put("android_data_plane_running", runtimeDataPlaneRunning)
            json.put("android_phase", runtimePhase)
            json.put("android_message", runtimeMessage)
            if (runtimeError.isNotBlank()) {
                json.put("android_error", runtimeError)
            }
            if (runtimeRunning) {
                json.put("running", true)
                if (json.optString("status").isBlank() || json.optString("status") == "stopped") {
                    json.put("status", if (runtimeStarting) "starting" else "running")
                }
            } else if (runtimeStarting) {
                json.put("status", "starting")
            }
            return json.toString()
        }

        fun updateRuntimeState(running: Boolean, starting: Boolean, phase: String, message: String, error: String = "") {
            runtimeRunning = running
            runtimeStarting = starting
            runtimeDataPlaneRunning = running && !starting && (phase == "running" || phase == "connected")
            runtimePhase = phase
            runtimeMessage = message
            runtimeError = error
        }
    }
}
