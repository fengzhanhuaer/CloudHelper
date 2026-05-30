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
        setControllerURL(config.controllerUrl)
        setVersion(currentLocalVersion(context))
        setNativeIPs(context)
        return recordResult("mobilecore", callString(
            methodName = "start",
            parameterTypes = arrayOf(String::class.java, String::class.java, String::class.java),
            args = arrayOf(config.controllerUrl, config.nodeId, config.nodeSecret),
        ))
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

    fun setControllerURL(controllerURL: String): String {
        return callString(
            methodName = "setControllerURL",
            parameterTypes = arrayOf(String::class.java),
            args = arrayOf(controllerURL),
        )
    }

    fun proxyStart(context: Context, controllerURL: String = ""): String {
        if (controllerURL.isNotBlank()) {
            setControllerURL(controllerURL)
        }
        return recordResult("mobilecore", callString(
            methodName = "proxyStart",
            parameterTypes = arrayOf(String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context)),
        ))
    }

    fun proxyStop(): String {
        return recordResult("mobilecore", callString("proxyStop", emptyArray<Class<*>>(), emptyArray()))
    }

    fun proxyStatus(context: Context): String {
        return callString(
            methodName = "proxyStatus",
            parameterTypes = arrayOf(String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context)),
        )
    }

    fun proxySetGroup(context: Context, group: String, action: String, selectedChainID: String): String {
        return recordResult("mobilecore", callString(
            methodName = "proxySetGroup",
            parameterTypes = arrayOf(String::class.java, String::class.java, String::class.java, String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context), group, action, selectedChainID),
        ))
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
