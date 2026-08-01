package me.strace.lotsman

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.os.Process
import android.system.OsConstants
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.io.File
import java.net.InetAddress
import java.net.InetSocketAddress
import java.util.concurrent.Executors
import java.net.NetworkInterface as JavaNetworkInterface

// --- the gomobile-bound Go side ---------------------------------------------
// ASSUMPTION (see README "Binding contract"): ONE `gomobile bind -javapkg=me.strace`
// binds both sing-box's libbox and our facade package `mobile`, giving
// me.strace.libbox.* and me.strace.mobile.Mobile. If the Go/build side picks a
// different -javapkg, only these imports change.
import me.strace.libbox.ConnectionOwner
import me.strace.libbox.InterfaceUpdateListener
import me.strace.libbox.LocalDNSTransport
import me.strace.libbox.NetworkInterfaceIterator
import me.strace.libbox.PlatformInterface
import me.strace.libbox.StringIterator
import me.strace.libbox.TunOptions
import me.strace.libbox.WIFIState
import me.strace.libbox.Notification as LibboxNotification
import me.strace.mobile.EventSink
import me.strace.mobile.Mobile

enum class TunnelState { STOPPED, STARTING, RUNNING, STOPPING, FAILED }

/**
 * The whole Android app, in one process and one service.
 *
 * Three roles live here on purpose:
 *  - the Android [VpnService] that owns the tun file descriptor,
 *  - the libbox [PlatformInterface] that sing-box calls back into for anything
 *    the Go runtime is not allowed to do itself on Android,
 *  - the owner of the Go facade lifecycle (`Mobile.start` / `Mobile.stop`).
 *
 * They are not separable: libbox asks for the tun fd through
 * [openTun] while `Mobile.start()` is still on the stack, so whatever answers
 * that call must already hold a live `VpnService.Builder`.
 *
 * ## Threading contract (the part that breaks first if ignored)
 *
 *  - **Every call INTO Go happens on [goThread]**, never on the main thread. Go
 *    calls here can block for seconds (config parse, first dial) and the JNI
 *    boundary offers no cancellation.
 *  - **Calls FROM Go run on Go's own threads** ([openTun], [autoDetectInterfaceControl],
 *    [onEvent]). They must return promptly and must not re-enter Go
 *    synchronously — the gomobile boundary is not reentrant. Anything that needs
 *    the facade gets posted to [goThread] instead.
 *  - [openTun] specifically must NOT be dispatched to [goThread]: that thread is
 *    the one blocked inside `Mobile.start()`, so waiting on it deadlocks the app.
 */
class LotsmanVpnService : VpnService(), PlatformInterface, EventSink {

    companion object {
        private const val TAG = "LotsmanVpn"

        const val ACTION_START = "me.strace.lotsman.action.START"
        const val ACTION_STOP = "me.strace.lotsman.action.STOP"

        private const val CHANNEL_ID = "lotsman.tunnel"
        private const val NOTIF_ID = 0x10757

        // Process-global because the app IS one process (see AndroidManifest: no
        // android:process on the service). The UI reads these directly rather
        // than binding to the service; Stage 3 can promote them to a repository.
        private val _state = MutableStateFlow(TunnelState.STOPPED)
        val state: StateFlow<TunnelState> = _state.asStateFlow()

        private val _lastError = MutableStateFlow<String?>(null)
        val lastError: StateFlow<String?> = _lastError.asStateFlow()

        private val _lastEvent = MutableStateFlow<String?>(null)
        val lastEvent: StateFlow<String?> = _lastEvent.asStateFlow()

        /** Caller must have completed [VpnService.prepare] first. */
        fun start(ctx: Context) {
            val i = Intent(ctx, LotsmanVpnService::class.java).setAction(ACTION_START)
            ContextCompat.startForegroundService(ctx, i)
        }

        fun stop(ctx: Context) {
            val i = Intent(ctx, LotsmanVpnService::class.java).setAction(ACTION_STOP)
            ctx.startService(i)
        }
    }

    /** The one and only thread allowed to call into Go. */
    private val goThread = Executors.newSingleThreadExecutor { r -> Thread(r, "lotsman-go") }
    private val main = Handler(Looper.getMainLooper())

    private val tunLock = Any()

    /**
     * STRONG reference to the tun descriptor, held for the whole life of the
     * tunnel.
     *
     * [openTun] returns `pfd.fd` — a bare int. Nothing on the Go side keeps the
     * Kotlin object alive, so if this field did not exist the
     * ParcelFileDescriptor would become garbage the moment openTun returned, and
     * its finalizer would close the descriptor underneath a running sing-box.
     * The symptom is a tunnel that establishes, passes a packet or two, then
     * dies with EBADF on read. Do not turn this into a local.
     */
    private var tunPfd: ParcelFileDescriptor? = null

    private var netCallback: ConnectivityManager.NetworkCallback? = null

    // ---------------------------------------------------------------- lifecycle

    override fun onCreate() {
        super.onCreate()
        createChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopTunnel()
            return START_NOT_STICKY
        }

        // Go foreground FIRST. startForegroundService() gives us ~5 s before the
        // system throws ForegroundServiceDidNotStartInTimeException, and starting
        // the Go core can take longer than that on a cold start.
        goForeground()

        when (_state.value) {
            TunnelState.STARTING, TunnelState.RUNNING -> return START_STICKY
            else -> Unit
        }
        _state.value = TunnelState.STARTING
        goThread.execute { startGo() }

        // STICKY: a VPN the system killed under memory pressure should come back.
        // The redelivered intent is null, which falls through to the start path
        // above — that is intended. VPN consent survives the restart.
        return START_STICKY
    }

    /** Runs on [goThread]. */
    private fun startGo() {
        try {
            val yaml = ensureConfig().readText()

            // ORDER MATTERS: the facade must be holding this PlatformInterface
            // before start() runs, because start() -> libbox -> openTun() happens
            // synchronously inside the start() call below.
            Mobile.setPlatform(this)
            Mobile.setEventSink(this)

            // filesDir is the ONLY writable base on Android. os.TempDir() resolves
            // to /data/local/tmp and os.UserCacheDir() to /sdcard, both unwritable,
            // and sing-box's RuleSetDir defaults to /etc/sing-box. The Go side
            // derives every path it needs (work dir, temp dir, rule-set cache,
            // state) from this string.
            Mobile.start(yaml, filesDir.absolutePath)

            _lastError.value = null
            _state.value = TunnelState.RUNNING
            Log.i(TAG, "Go facade started")
        } catch (t: Throwable) {
            // Catch Throwable, not Exception: a bad .aar surfaces as
            // UnsatisfiedLinkError / NoSuchMethodError, and letting that kill the
            // service thread would leave a foreground notification with no tunnel.
            Log.e(TAG, "Go facade failed to start", t)
            _lastError.value = t.message ?: t.toString()
            _state.value = TunnelState.FAILED
            main.post { stopTunnel() }
        }
    }

    private fun stopTunnel() {
        when (_state.value) {
            TunnelState.STOPPED, TunnelState.STOPPING -> Unit
            else -> _state.value = TunnelState.STOPPING
        }
        goThread.execute {
            runCatching { Mobile.stop() }.onFailure { Log.w(TAG, "Mobile.stop failed", it) }
            // Only AFTER Go has returned from stop(): sing-box is still reading
            // the tun until then, and closing the descriptor out from under it
            // races its own shutdown path.
            closeTun()
            _state.value = TunnelState.STOPPED
            main.post {
                ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
                stopSelf()
            }
        }
    }

    /**
     * The system revoked our tunnel — another VPN app took over, or the user
     * revoked consent in Settings. Called on the main thread; the tun fd is
     * already dead by the time we get here.
     */
    override fun onRevoke() {
        Log.w(TAG, "tunnel revoked by the system")
        stopTunnel()
        super.onRevoke()
    }

    override fun onDestroy() {
        runCatching { closeDefaultInterfaceMonitor(null) }
        goThread.shutdown()
        super.onDestroy()
    }

    private fun ensureConfig(): File {
        val f = File(filesDir, "config.yaml")
        if (!f.exists()) {
            assets.open("config.yaml").use { input ->
                f.outputStream().use { output -> input.copyTo(output) }
            }
        }
        return f
    }

    private fun closeTun() {
        synchronized(tunLock) {
            val pfd = tunPfd ?: return
            tunPfd = null
            // May already be closed from the Go side — see the ownership note in
            // openTun. Swallow rather than crash the teardown path.
            runCatching { pfd.close() }.onFailure { Log.d(TAG, "tun already closed: $it") }
        }
    }

    // ------------------------------------------------------------ notification

    private fun createChannel() {
        if (Build.VERSION.SDK_INT < 26) return
        val ch = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.notif_channel_name),
            NotificationManager.IMPORTANCE_LOW, // no sound; it is a status line
        ).apply { description = getString(R.string.notif_channel_desc) }
        getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
    }

    private fun goForeground() {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, LotsmanVpnService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val notif = NotificationCompat.Builder(this, CHANNEL_ID)
            // Platform drawable so Stage 1 needs no drawable resources at all.
            // Stage 3 ships a real monochrome status icon.
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentTitle(getString(R.string.notif_title))
            .setContentIntent(open)
            .addAction(0, getString(R.string.notif_stop), stop)
            .setOngoing(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()

        ServiceCompat.startForeground(
            this, NOTIF_ID, notif,
            if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0,
        )
    }

    // ================================================== libbox PlatformInterface
    //
    // VERSION-PINNED SURFACE. The exact method set of libbox's PlatformInterface
    // changes between sing-box releases (newer ones add writeLog, sendNotification,
    // systemCertificates, localDNSTransport, ...). Kotlin must implement the
    // interface exactly, so the first `assembleDebug` against a real .aar is what
    // proves this block. A missing override or an unknown-member error here is
    // version drift, not a design problem: add/drop the method to match the
    // sing-box version pinned in the Go module.

    /**
     * Called BY Go, from inside `Mobile.start()`, on [goThread] via JNI.
     *
     * Rules, all load-bearing:
     *  1. Do the work synchronously on the calling thread. Posting to [goThread]
     *     deadlocks — that thread is blocked in start() waiting for this to return.
     *  2. Do not call back into Go from here. The boundary is not reentrant.
     *  3. Store the ParcelFileDescriptor in a field before returning (see [tunPfd]).
     *
     * Every tun parameter comes from [options], never from a hardcoded default:
     * the addresses, routes and DNS were computed by the Go config generator, and
     * inventing them here would silently diverge from what sing-box believes it
     * configured. The tun fd itself never appears in the config JSON — this
     * callback IS the channel.
     */
    override fun openTun(options: TunOptions): Int {
        val builder = Builder()
            .setSession("Lotsman")
            .setMtu(options.getMTU())

        // NAMING: RoutePrefix.Address()/Prefix() are Go METHODS, so gobind emits
        // address()/prefix() (lowerFirst of the Go name) — NOT getAddress()/getPrefix().
        // Only struct FIELDS get the get*/set* prefix (see NetworkInterface below).
        var hasV4 = false
        var hasV6 = false
        options.getInet4Address().forEachPrefix { p ->
            builder.addAddress(p.address(), p.prefix()); hasV4 = true
        }
        options.getInet6Address().forEachPrefix { p ->
            builder.addAddress(p.address(), p.prefix()); hasV6 = true
        }
        if (!hasV4 && !hasV6) throw IllegalStateException("TunOptions carried no tun address")

        var routedV4 = false
        var routedV6 = false
        options.getInet4RouteAddress().forEachPrefix { p ->
            builder.addRoute(p.address(), p.prefix()); routedV4 = true
        }
        options.getInet6RouteAddress().forEachPrefix { p ->
            builder.addRoute(p.address(), p.prefix()); routedV6 = true
        }
        // auto_route with no explicit route set means "take the default route".
        // Only per family we actually have an address for: VpnService rejects a
        // route whose family has no address on the interface.
        if (options.getAutoRoute()) {
            if (!routedV4 && hasV4) builder.addRoute("0.0.0.0", 0)
            if (!routedV6 && hasV6) builder.addRoute("::", 0)
        }

        // Throws on the Go side when no DNS is configured, hence runCatching.
        // Returns libbox.StringBox (a Go struct), not a String: `Value` is an
        // exported FIELD, so gobind emits getValue() -> Kotlin `.value`.
        runCatching { options.getDNSServerAddress() }
            .getOrNull()
            ?.value
            ?.takeIf { it.isNotBlank() }
            ?.let { builder.addDnsServer(it) }

        // Per-app routing arrives HERE and nowhere else: libbox REJECTS
        // include_uid / exclude_uid inside the tun JSON, so the package lists
        // come through TunOptions and we translate them into VpnService's own uid
        // rules. Ignoring them would be a security hole — the user would believe
        // an app is excluded while its traffic still enters the tun.
        // VpnService forbids mixing allowed and disallowed apps, so include wins.
        var included = false
        options.getIncludePackage().forEachString { pkg ->
            runCatching { builder.addAllowedApplication(pkg) }
                .onSuccess { included = true }
                .onFailure { Log.w(TAG, "include package not installed, ignored: $pkg") }
        }
        if (!included) {
            options.getExcludePackage().forEachString { pkg ->
                runCatching { builder.addDisallowedApplication(pkg) }
                    .onFailure { Log.w(TAG, "exclude package not installed, ignored: $pkg") }
            }
        }

        if (hasV4) builder.allowFamily(OsConstants.AF_INET)
        if (hasV6) builder.allowFamily(OsConstants.AF_INET6)
        if (Build.VERSION.SDK_INT >= 29) builder.setMetered(false)

        val pfd = builder.establish()
            ?: throw IllegalStateException(
                "VpnService.establish() returned null — consent revoked, or another VPN owns the tun",
            )
        synchronized(tunLock) {
            tunPfd?.let { old -> runCatching { old.close() } } // never leak a previous tun
            tunPfd = pfd
        }

        // OWNERSHIP, UNVERIFIED: we hand out the raw fd and keep the
        // ParcelFileDescriptor, matching what the SagerNet clients do. If the
        // pinned sing-box closes the descriptor itself on shutdown, our later
        // close() is a harmless double close *provided* it happens right after
        // Mobile.stop() returns (it does — see stopTunnel). The alternative,
        // returning pfd.detachFd(), removes that race but leaks the descriptor
        // whenever Go does not close it. Decide this on a device, not here.
        return pfd.fd
    }

    /**
     * Called BY Go for every outbound socket before it connects. `protect()`
     * marks the socket to bypass our own tun; without it sing-box's outbound
     * traffic re-enters the tunnel and loops.
     */
    override fun autoDetectInterfaceControl(fd: Int) {
        if (!protect(fd)) throw IllegalStateException("VpnService.protect($fd) failed")
    }

    override fun usePlatformAutoDetectInterfaceControl(): Boolean = true

    // NOTE: sing-box 1.13's PlatformInterface has NO usePlatformDefaultInterfaceMonitor()
    // and NO usePlatformInterfaceGetter(). Both were removed — libbox's
    // platformInterfaceWrapper now returns true for UsePlatformDefaultInterfaceMonitor()
    // and UsePlatformNetworkInterfaces() unconditionally (experimental/libbox/service.go),
    // so the platform side is ALWAYS asked. Declaring those overrides here is a hard
    // compile error ("overrides nothing"); the behaviour they were asking for is the
    // default. Do not add them back.

    /**
     * Go's own default-interface monitor reads netlink RTM_GETLINK, which
     * Android 11+ denies to apps, so libbox delegates unconditionally.
     * ConnectivityManager is the only supported source of this signal.
     */
    override fun startDefaultInterfaceMonitor(listener: InterfaceUpdateListener) {
        val cm = getSystemService(ConnectivityManager::class.java)
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) {
                val name = lp.interfaceName ?: return
                val index = runCatching { JavaNetworkInterface.getByName(name)?.index ?: -1 }
                    .getOrDefault(-1)
                val expensive = cm.getNetworkCapabilities(network)
                    ?.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) == false
                // Kotlin -> Go from a ConnectivityManager thread. Safe: this is
                // not inside a Go -> Kotlin callback, so it cannot re-enter.
                listener.updateDefaultInterface(name, index, expensive, false)
            }

            override fun onLost(network: Network) {
                listener.updateDefaultInterface("", -1, false, false)
            }
        }
        cm.registerDefaultNetworkCallback(cb)
        netCallback = cb
    }

    override fun closeDefaultInterfaceMonitor(listener: InterfaceUpdateListener?) {
        val cb = netCallback ?: return
        netCallback = null
        runCatching { getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(cb) }
    }

    /**
     * Called for the same reason as the monitor: `net.Interfaces()` in Go fails
     * with EPERM on Android 11+ (netlink is restricted). java.net.NetworkInterface
     * goes through getifaddrs(3), which apps are still allowed to call.
     */
    override fun getInterfaces(): NetworkInterfaceIterator =
        AndroidNetworkInterfaceIterator(JavaNetworkInterface.getNetworkInterfaces())

    /** iOS Network Extension concept; always false on Android. */
    override fun underNetworkExtension(): Boolean = false

    override fun includeAllNetworks(): Boolean = false

    override fun clearDNSCache() {
        // Nothing to clear: we never populate an Android-side resolver cache.
    }

    /**
     * Reporting SSID/BSSID needs ACCESS_FINE_LOCATION on API 27+. We do not ask
     * for a location permission for a VPN app, so this stays null and the Go side
     * simply has no Wi-Fi identity signal. Note this weakens network-identity
     * (`pkg/netid`-style) fingerprinting on Android compared to desktop.
     */
    override fun readWIFIState(): WIFIState? = null

    /**
     * Hand sing-box an Android-side DNS resolver. Null means "use your own", which
     * is what Lotsman wants: the whole point of the split-DNS design is that the
     * config decides resolution, not the handset's DHCP servers.
     */
    override fun localDNSTransport(): LocalDNSTransport? = null

    /**
     * /proc/net/* was readable by apps up to Android 9. From Android 10 the kernel
     * hides other UIDs' sockets, so process lookup has to go through
     * ConnectivityManager instead — see [findConnectionOwner].
     */
    override fun useProcFS(): Boolean = Build.VERSION.SDK_INT < 29

    /**
     * Resolves the app that owns a connection. Only reached when the routing rules
     * actually match on process/package, which the v1 generator never emits — but
     * the method is mandatory (libbox calls `UseProcFS()` in NewCommandServer and
     * wires this in unconditionally), so it is implemented rather than stubbed.
     */
    override fun findConnectionOwner(
        ipProtocol: Int,
        sourceAddress: String,
        sourcePort: Int,
        destinationAddress: String,
        destinationPort: Int,
    ): ConnectionOwner {
        if (Build.VERSION.SDK_INT < 29) {
            throw UnsupportedOperationException("connection owner lookup needs API 29+ (procfs path is used below that)")
        }
        val cm = getSystemService(ConnectivityManager::class.java)
        val uid = cm.getConnectionOwnerUid(
            ipProtocol,
            InetSocketAddress(InetAddress.getByName(sourceAddress), sourcePort),
            InetSocketAddress(InetAddress.getByName(destinationAddress), destinationPort),
        )
        if (uid == Process.INVALID_UID) throw IllegalStateException("no owner for connection")
        return ConnectionOwner().apply {
            userId = uid
            packageManager.getNameForUid(uid)?.let { userName = it }
            setAndroidPackageNames(StringArray(packageManager.getPackagesForUid(uid)?.toList().orEmpty()))
        }
    }

    /**
     * Extra trust anchors for sing-box's TLS stack. Empty: sing-box falls back to
     * its own bundled roots, and injecting the device's user store here would let a
     * locally installed CA silently MITM the tunnel's control connections.
     */
    override fun systemCertificates(): StringIterator = StringArray(emptyList())

    /**
     * libbox raises these for out-of-band prompts (Tailscale auth URLs and the
     * like). v1 enables none of those protocols, so this is a log line rather than
     * an unfinished notification pipeline pretending to be one.
     */
    override fun sendNotification(notification: LibboxNotification) {
        Log.i(TAG, "libbox notification [${notification.typeName}] ${notification.title}: ${notification.body}")
    }

    // ============================================================== Go EventSink

    /**
     * Called BY Go on a Go-owned thread. Return fast; do not call back into Go
     * from here. Anything needing the facade goes through goThread.execute {}, so
     * the round trip is asynchronous and the boundary stays one-directional.
     */
    override fun onEvent(eventJSON: String) {
        _lastEvent.value = eventJSON
        Log.d(TAG, "event: $eventJSON")
    }
}
