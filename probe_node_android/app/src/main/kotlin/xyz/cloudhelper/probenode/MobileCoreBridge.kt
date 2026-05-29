package xyz.cloudhelper.probenode

import android.content.Context

object MobileCoreBridge {
    fun start(context: Context, config: ProbeNodeConfig): String {
        if (!config.isReady) {
            return "controller URL, node ID, and node secret are required"
        }
        return callString(
            methodName = "start",
            parameterTypes = arrayOf(String::class.java, String::class.java, String::class.java),
            args = arrayOf(config.controllerUrl, config.nodeId, config.nodeSecret),
        )
    }

    fun stop(): String {
        return callString("stop", emptyArray<Class<*>>(), emptyArray())
    }

    fun status(): String {
        return callString("status", emptyArray<Class<*>>(), emptyArray())
    }

    fun refreshConfig(context: Context, config: ProbeNodeConfig): String {
        if (!config.isReady) {
            return "controller URL, node ID, and node secret are required"
        }
        return callString(
            methodName = "refreshConfig",
            parameterTypes = arrayOf(String::class.java, String::class.java, String::class.java, String::class.java),
            args = arrayOf(config.controllerUrl, config.nodeId, config.nodeSecret, ProbeNodeConfig.configDir(context)),
        )
    }

    fun linkStatus(context: Context): String {
        return callString(
            methodName = "linkStatus",
            parameterTypes = arrayOf(String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context)),
        )
    }

    fun linkLatency(context: Context, chainID: String): String {
        return callString(
            methodName = "linkLatency",
            parameterTypes = arrayOf(String::class.java, String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context), chainID),
        )
    }

    fun linkSpeed(context: Context, chainID: String, protocol: String): String {
        return callString(
            methodName = "linkSpeed",
            parameterTypes = arrayOf(String::class.java, String::class.java, String::class.java),
            args = arrayOf(ProbeNodeConfig.configDir(context), chainID, protocol),
        )
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
}
