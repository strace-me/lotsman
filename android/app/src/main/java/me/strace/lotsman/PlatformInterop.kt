package me.strace.lotsman

import android.system.OsConstants
import me.strace.libbox.RoutePrefix
import me.strace.libbox.RoutePrefixIterator
import me.strace.libbox.StringIterator
import me.strace.libbox.NetworkInterface as LibboxNetworkInterface
import me.strace.libbox.NetworkInterfaceIterator
import java.net.NetworkInterface as JavaNetworkInterface

/**
 * Adapters between Android/JDK types and the iterator shapes gomobile generates.
 *
 * gomobile cannot bind Go slices, so libbox exposes every list as a hand-rolled
 * `HasNext()/Next()` interface. Nothing here is clever; it exists so the call
 * sites in [LotsmanVpnService] read like normal Kotlin.
 *
 * UNVERIFIED: the member names below (getAddress/getPrefix on RoutePrefix, the
 * setters on NetworkInterface) are the gomobile mapping of libbox's Go structs
 * and are pinned to a sing-box version. The first real compile against the .aar
 * is what proves them.
 */

internal inline fun RoutePrefixIterator?.forEachPrefix(action: (RoutePrefix) -> Unit) {
    val iter = this ?: return
    while (iter.hasNext()) action(iter.next())
}

internal inline fun StringIterator?.forEachString(action: (String) -> Unit) {
    val iter = this ?: return
    while (iter.hasNext()) action(iter.next())
}

/** Backs a libbox StringIterator with a plain Kotlin list. */
internal class StringArray(private val items: List<String>) : StringIterator {
    private var i = 0
    override fun len(): Int = items.size
    override fun hasNext(): Boolean = i < items.size
    override fun next(): String = items[i++]
}

/**
 * Feeds sing-box the host's interface list from java.net instead of from Go.
 *
 * This is the fix for a named Android landmine: Go's `net.Interfaces()` uses a
 * netlink RTM_GETLINK dump, which Android 11+ blocks for apps (EPERM), so
 * anything in sing-box that enumerates interfaces silently sees nothing.
 * java.net.NetworkInterface reads getifaddrs(3), which is still permitted.
 */
internal class AndroidNetworkInterfaceIterator(
    interfaces: java.util.Enumeration<JavaNetworkInterface>?,
) : NetworkInterfaceIterator {

    private val iter = (interfaces?.toList() ?: emptyList()).iterator()

    override fun hasNext(): Boolean = iter.hasNext()

    override fun next(): LibboxNetworkInterface {
        val ni = iter.next()
        val addrs = runCatching {
            ni.interfaceAddresses.mapNotNull { ia ->
                val host = ia.address?.hostAddress ?: return@mapNotNull null
                // Strip the IPv6 scope suffix ("fe80::1%wlan0"); Go parses a bare
                // CIDR here and chokes on the zone.
                "${host.substringBefore('%')}/${ia.networkPrefixLength}"
            }
        }.getOrDefault(emptyList())

        // RAW LINUX IFF_* FLAGS, not Go's net.Flags. libbox runs this field through
        // its own `linkFlags(uint32)` (experimental/libbox/link_flags_unix.go), a
        // verbatim copy of net.linkFlags that reads syscall.IFF_*. The two layouts
        // only agree on UP and BROADCAST: Go's FlagLoopback is 4 (= IFF_DEBUG) and
        // its FlagPointToPoint is 8 (= IFF_LOOPBACK), so feeding net.Flags here
        // makes every PPP/cellular link arrive on the Go side as a loopback and
        // drops RUNNING entirely. OsConstants gives us the kernel's own values.
        var flags = 0
        runCatching {
            if (ni.isUp) flags = flags or OsConstants.IFF_UP or OsConstants.IFF_RUNNING
            if (ni.isLoopback) flags = flags or OsConstants.IFF_LOOPBACK
            if (ni.isPointToPoint) flags = flags or OsConstants.IFF_POINTOPOINT
            if (ni.supportsMulticast()) flags = flags or OsConstants.IFF_MULTICAST
            if (ni.interfaceAddresses.any { it.broadcast != null }) flags = flags or OsConstants.IFF_BROADCAST
        }

        return LibboxNetworkInterface().apply {
            setName(ni.name)
            setIndex(ni.index)
            setMTU(runCatching { ni.mtu }.getOrDefault(0))
            setFlags(flags)
            setAddresses(StringArray(addrs))
        }
    }
}
