package xyz.cloudhelper.probenode

import android.content.Context
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.os.Build
import org.json.JSONArray
import java.net.Inet4Address
import java.net.Inet6Address
import java.net.NetworkInterface

object MobileCoreBridge {
    fun start(context: Context, config: ProbeNodeConfig): String {
        if (!config.isReady) {
            return "controller URL, node ID, and node secret are required"
        }
        prepareRuntime(context, config)
        return recordResult("mobilecore", callString(
            methodName = "startWithConfigDir",
            parameterTypes = arrayOf(String::class.java, String::class.java, String::class.java, String::class.java),
            args = arrayOf(config.controllerUrl, config.nodeId, config.nodeSecret, ProbeNodeConfig.configDir(context)),
        ))
    }

    // Prepares shared native state without opening the controller reporter session.
    // VPN startup uses this path so data-plane startup cannot restart yamux/reporting
    // or re-apply virtual route runtimes.
    fun prepareRuntime(context: Context, config: ProbeNodeConfig): String {
        if (!config.isReady) {
            return "controller URL, node ID, and node secret are required"
        }
        setControllerURL(config.controllerUrl)
        setVersion(currentLocalVersion(context))
        setNativeIPs(context)
        return "mobilecore runtime prepared"
    }

    fun setVersion(version: String): String {
        return callString(
            methodName = "setVersion",
            parameterTypes = arrayOf(String::class.java),
            args = arrayOf(version),
        )
    }

    fun setNativeIPs(context: Context): String {
        val ips = collectNativeIPs(context)
        return callString(
            methodName = "setNativeIPs",
            parameterTypes = arrayOf(String::class.java, String::class.java),
            args = arrayOf(JSONArray(ips.first).toString(), JSONArray(ips.second).toString()),
        )
    }

    fun stop(): String {
        return recordResult("mobilecore", callString("stop", emptyArray<Class<*>>(), emptyArray()))
    }

    fun status(): String {
        return callString("status", emptyArray<Class<*>>(), emptyArray())
    }

    fun refreshConfig(context: Context, config: ProbeNodeConfig): String {
        if (!config.isReady) {
            return "controller URL, node ID, and node secret are required"
        }
        setControllerURL(config.controllerUrl)
        return recordResult("mobilecore", callString(
            methodName = "refreshConfig",
            parameterTypes = arrayOf(String::class.java, String::class.java, String::class.java, String::class.java),
            args = arrayOf(config.controllerUrl, config.nodeId, config.nodeSecret, ProbeNodeConfig.configDir(context)),
        ))
    }

    fun linkStatus(context: Context): String {
        return callString(
            methodName = "linkStatus",
            parameterTypes = arrayOf(String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context)),
        )
    }

    fun linkLatency(context: Context, chainID: String): String {
        return recordResult("mobilecore", callString(
            methodName = "linkLatency",
            parameterTypes = arrayOf(String::class.java, String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context), chainID),
        ))
    }

    fun linkSpeed(context: Context, chainID: String, protocol: String): String {
        return recordResult("mobilecore", callString(
            methodName = "linkSpeed",
            parameterTypes = arrayOf(String::class.java, String::class.java, String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context), chainID, protocol),
        ))
    }

    fun vpnStart(context: Context, fd: Int): String {
        return recordResult("mobilecore", callString(
            methodName = "vpnStart",
            parameterTypes = arrayOf(Long::class.javaPrimitiveType!!, String::class.java),
            args = arrayOf(fd.toLong(), ProbeNodeConfig.configDir(context)),
        ))
    }

    fun vpnStop(): String {
        return recordResult("mobilecore", callString("vpnStop", emptyArray<Class<*>>(), emptyArray()))
    }

    fun vpnStatus(): String {
        return callString("vpnStatus", emptyArray<Class<*>>(), emptyArray())
    }

    fun vRoutePathRTT(targetNodeID: String): String {
        return callString(
            methodName = "vRoutePathRTT",
            parameterTypes = arrayOf(String::class.java),
            args = arrayOf(targetNodeID),
        )
    }

    fun vpnSelfCheck(context: Context): String {
        return recordResult("mobilecore", callString(
            methodName = "vpnSelfCheck",
            parameterTypes = arrayOf(String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context)),
        ))
    }

    fun setControllerURL(controllerURL: String): String {
        return callString(
            methodName = "setControllerURL",
            parameterTypes = arrayOf(String::class.java),
            args = arrayOf(controllerURL),
        )
    }

    fun appendAppLog(source: String, level: String, message: String): String {
        return callString(
            methodName = "appendAppLog",
            parameterTypes = arrayOf(String::class.java, String::class.java, String::class.java),
            args = arrayOf(source, level, message),
        )
    }

    private fun recordResult(source: String, result: String): String {
        AndroidLogStore.add(source, result, if (result.contains("failed", ignoreCase = true) || result.contains("失败")) "error" else "info")
        return result
    }

    private fun callString(methodName: String, parameterTypes: Array<Class<*>>, args: Array<Any>): String {
        return try {
            val cls = Class.forName("mobilecore.Mobilecore")
            val exportName = methodName.replaceFirstChar { it.uppercaseChar() }
            val method = cls.methods.firstOrNull {
                it.name.equals(exportName, ignoreCase = true) && it.parameterTypes.contentEquals(parameterTypes)
            } ?: error("method $exportName not found")
            method.invoke(null, *args)?.toString() ?: ""
        } catch (e: ClassNotFoundException) {
            "mobilecore AAR is not packaged"
        } catch (e: Throwable) {
            "mobilecore call failed: ${e.cause?.message ?: e.message ?: e.javaClass.simpleName}"
        }
    }

    private fun currentLocalVersion(context: Context): String {
        val packageInfo = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            context.packageManager.getPackageInfo(context.packageName, PackageManager.PackageInfoFlags.of(0))
        } else {
            @Suppress("DEPRECATION")
            context.packageManager.getPackageInfo(context.packageName, 0)
        }
        val code = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            packageInfo.longVersionCode
        } else {
            @Suppress("DEPRECATION")
            packageInfo.versionCode.toLong()
        }
        return "${packageInfo.versionName ?: "0.0.0"} ($code)"
    }

    private fun collectNativeIPs(context: Context): Pair<List<String>, List<String>> {
        val ipv4 = linkedSetOf<String>()
        val ipv6 = linkedSetOf<String>()
        collectNetworkInterfaceIPs(ipv4, ipv6)
        collectConnectivityIPs(context, ipv4, ipv6)
        return Pair(ipv4.toList(), ipv6.toList())
    }

    private fun collectNetworkInterfaceIPs(ipv4: MutableSet<String>, ipv6: MutableSet<String>) {
        try {
            val interfaces = NetworkInterface.getNetworkInterfaces()
            while (interfaces.hasMoreElements()) {
                val item = interfaces.nextElement()
                if (!item.isUp || item.isLoopback) {
                    continue
                }
                val addrs = item.inetAddresses
                while (addrs.hasMoreElements()) {
                    addNativeIP(addrs.nextElement(), ipv4, ipv6)
                }
            }
        } catch (_: Throwable) {
        }
    }

    private fun collectConnectivityIPs(context: Context, ipv4: MutableSet<String>, ipv6: MutableSet<String>) {
        try {
            val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
            val networks = cm.allNetworks
            networks.forEach { network ->
                val props: LinkProperties = cm.getLinkProperties(network) ?: return@forEach
                props.linkAddresses.forEach { address ->
                    addNativeIP(address.address, ipv4, ipv6)
                }
            }
        } catch (_: Throwable) {
        }
    }

    private fun addNativeIP(address: java.net.InetAddress?, ipv4: MutableSet<String>, ipv6: MutableSet<String>) {
        if (address == null || address.isLoopbackAddress || address.isAnyLocalAddress) {
            return
        }
        when (address) {
            is Inet4Address -> ipv4.add(address.hostAddress ?: return)
            is Inet6Address -> {
                if (address.isLinkLocalAddress) {
                    return
                }
                val value = (address.hostAddress ?: return).substringBefore("%")
                if (value.isNotBlank()) {
                    ipv6.add(value)
                }
            }
        }
    }
}
