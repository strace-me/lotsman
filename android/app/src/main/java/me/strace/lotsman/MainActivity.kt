package me.strace.lotsman

import android.app.Activity
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import me.strace.mobile.Mobile
import org.json.JSONObject

/**
 * Stage-1 UI: one button, the consent flow, and a status line. Deliberately
 * nothing else — Stage 3 owns the real UI, and anything built here would be
 * thrown away.
 */
class MainActivity : ComponentActivity() {

    private val vpnConsent =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
            if (result.resultCode == Activity.RESULT_OK) {
                LotsmanVpnService.start(this)
            } else {
                Toast.makeText(this, "VPN permission denied", Toast.LENGTH_SHORT).show()
            }
        }

    private val notificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { /* advisory */ }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            MaterialTheme {
                Surface(modifier = Modifier.fillMaxSize()) {
                    LotsmanScreen(onToggle = ::toggle)
                }
            }
        }
    }

    private fun toggle(turnOn: Boolean) {
        if (!turnOn) {
            LotsmanVpnService.stop(this)
            return
        }

        // Advisory, not a gate: on API 33+ the foreground notification is
        // suppressed without this, but the tunnel itself still comes up.
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            notificationPermission.launch(android.Manifest.permission.POST_NOTIFICATIONS)
        }

        // The system consent dialog. Returns null once the user has already
        // granted this app the tunnel, so the common path starts immediately.
        val consentIntent = VpnService.prepare(this)
        if (consentIntent == null) LotsmanVpnService.start(this) else vpnConsent.launch(consentIntent)
    }
}

@Composable
private fun LotsmanScreen(onToggle: (Boolean) -> Unit) {
    val state by LotsmanVpnService.state.collectAsStateWithLifecycle()
    val error by LotsmanVpnService.lastError.collectAsStateWithLifecycle()
    val event by LotsmanVpnService.lastEvent.collectAsStateWithLifecycle()
    val status = rememberGoStatus(state)

    val on = state == TunnelState.RUNNING || state == TunnelState.STARTING

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(24.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text("Lotsman", style = MaterialTheme.typography.headlineMedium)
        Text(state.name, style = MaterialTheme.typography.titleMedium)

        Button(
            onClick = { onToggle(!on) },
            enabled = state != TunnelState.STARTING && state != TunnelState.STOPPING,
            modifier = Modifier.fillMaxWidth(),
        ) { Text(if (on) "Turn off" else "Turn on") }

        error?.let { Text("error: $it", color = MaterialTheme.colorScheme.error) }
        Text(status, style = MaterialTheme.typography.bodySmall)
        event?.let { Text("last event: $it", style = MaterialTheme.typography.bodySmall) }
    }
}

/**
 * Polls the Go facade once a second while the tunnel is up.
 *
 * `Mobile.statusJSON()` is a JNI call into the same process that hosts the
 * VpnService (one process, no IPC — that is the whole point of the Android
 * design), so the Activity may call it directly. It must NOT run on the main
 * thread: it takes a Go-side lock and can block behind a running reconcile.
 *
 * ASSUMPTION: the facade tolerates being polled and never panics. We only poll
 * while the service reports STARTING/RUNNING, so a facade that is not safe to
 * call before Start() still works.
 */
@Composable
private fun rememberGoStatus(state: TunnelState): String {
    var text by remember { mutableStateOf("idle") }
    LaunchedEffect(state) {
        if (state != TunnelState.RUNNING && state != TunnelState.STARTING) {
            text = "idle"
            return@LaunchedEffect
        }
        while (true) {
            text = withContext(Dispatchers.IO) {
                runCatching { summarize(Mobile.statusJSON()) }
                    .getOrElse { "status unavailable: ${it.message}" }
            }
            delay(1_000)
        }
    }
    return text
}

/** Pull the two fields worth showing out of client/core's Report JSON. */
private fun summarize(json: String): String = runCatching {
    val o = JSONObject(json)
    val running = o.optBoolean("running", false)
    val verdict = o.optJSONObject("verdict")
    val svc = verdict?.let {
        "${it.optString("state", "?")} " +
            "(${it.optInt("working")}/${it.optInt("total")} working, " +
            "${it.optInt("failing")} failing, ${it.optInt("broken")} broken)"
    } ?: "no verdict"
    "running=$running  $svc"
}.getOrElse { json.take(300) }
