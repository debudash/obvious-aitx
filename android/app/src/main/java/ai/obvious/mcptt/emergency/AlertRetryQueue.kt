package ai.obvious.mcptt.emergency

import ai.obvious.mcptt.api.ApiException
import ai.obvious.mcptt.signal.BackoffPolicy
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executor
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicReference

/**
 * A pending emergency alert waiting to reach the server. Survives process
 * restarts through [AlertStore].
 */
data class PendingAlert(
    val alertId: String,
    val imminentPeril: Boolean,
    val lat: Double?,
    val lon: Double?,
    val note: String,
    val createdAtMs: Long,
    val attemptCount: Int = 0,
)

/** Persistence seam: in-memory for tests, SharedPreferences in the app. */
interface AlertStore {
    fun load(): List<PendingAlert>
    fun save(alerts: List<PendingAlert>)
}

class InMemoryAlertStore : AlertStore {
    private val alerts = mutableListOf<PendingAlert>()
    override fun load(): List<PendingAlert> = alerts.toList()
    override fun save(alerts: List<PendingAlert>) {
        this.alerts.clear()
        this.alerts.addAll(alerts)
    }
}

/** What the queue is doing with an alert — the Alerts screen renders this. */
sealed interface AlertDelivery {
    data class Retrying(val alert: PendingAlert, val nextDelayMs: Long) : AlertDelivery
    data class DeliveredAwaitingAck(val alert: PendingAlert, val serverId: String) : AlertDelivery

    /** Permanent failure (non-retryable 4xx): visible, never silently dropped. */
    data class Failed(val alert: PendingAlert, val reason: String) : AlertDelivery
}

/**
 * Emergency alerts retry FOREVER until the server accepts them, then the
 * queue holds them until a dispatcher's EmergencyAlertAck arrives (spec:
 * "an emergency alert lost to a network drop must retry until acknowledged
 * server-side"). Dedupe by alertId; persistence across process restarts.
 *
 * The queue is pure JVM: HTTP goes through the injected [Transport],
 * time through the injected [Executor] + caller-supplied clock — fully
 * unit-testable.
 */
class AlertRetryQueue(
    private val transport: Transport,
    private val store: AlertStore,
    private val backoff: BackoffPolicy = BackoffPolicy(),
    private val retryExecutor: Executor = Executors.newSingleThreadExecutor { r ->
        Thread(r, "mcptt-alert-retry").apply { isDaemon = true }
    },
    private val onChanged: (AlertDelivery) -> Unit = {},
) {
    /** Minimal HTTP seam matching McpttApi.raiseAlert. */
    fun interface Transport {
        /** @throws ApiException on non-2xx, IOException on network failure. */
        fun raiseAlert(alert: PendingAlert): String
    }

    private val pending = CopyOnWriteArrayList<PendingAlert>()
    private val awaitingAck = AtomicReference<Set<String>>(emptySet())
    private val failed = CopyOnWriteArrayList<AlertDelivery.Failed>()

    fun start() {
        // Reload anything a previous process died with — the alert must
        // still land.
        val recovered = store.load()
        if (recovered.isNotEmpty()) {
            pending.addAll(recovered)
            kick()
        }
    }

    /** Enqueue (or dedupe) and start driving it to the server. */
    fun submit(alert: PendingAlert) {
        if (pending.none { it.alertId == alert.alertId } &&
            alert.alertId !in awaitingAck.get()
        ) {
            pending.add(alert)
            persist()
            kick()
        }
    }

    /** EmergencyAlertAck frame from the dispatcher — the loop closes. */
    fun onAlertAck(alertId: String) {
        awaitingAck.updateAndGet { it + alertId }
        failed.removeAll { it.alert.alertId == alertId }
        onChanged(AlertDelivery.DeliveredAwaitingAck(pendingAlertOrSynthetic(alertId), alertId))
    }

    /** Snapshot for the Alerts screen. */
    fun snapshot(): List<AlertDelivery> =
        pending.map { AlertDelivery.Retrying(it, backoff.nextDelayMs()) } +
            failed.toList()

    fun pendingCount(): Int = pending.size

    private fun pendingAlertOrSynthetic(alertId: String): PendingAlert =
        pending.firstOrNull { it.alertId == alertId }
            ?: PendingAlert(alertId, false, null, null, "", 0)

    private fun persist() {
        store.save(pending.toList())
    }

    private fun kick() {
        retryExecutor.execute { attemptAll() }
    }

    private fun attemptAll() {
        val snapshot = pending.toList()
        for (alert in snapshot) {
            attempt(alert)
        }
    }

    private fun attempt(alert: PendingAlert) {
        try {
            transport.raiseAlert(alert)
            // Accepted: 2xx. Stop retrying, wait for the ack frame.
            pending.remove(alert)
            awaitingAck.updateAndGet { it + alert.alertId }
            persist()
            onChanged(AlertDelivery.DeliveredAwaitingAck(alert, alert.alertId))
        } catch (e: ApiException) {
            if (e.isAuthError || e.code in RETRYABLE_HTTP) {
                scheduleRetry(alert)
            } else {
                // Validation-grade 4xx will never succeed — park it where
                // the Alerts screen shows the failure instead of looping.
                val failure = AlertDelivery.Failed(alert, e.message ?: "HTTP ${e.code}")
                pending.remove(alert)
                persist()
                failed.add(failure)
                onChanged(failure)
            }
        } catch (e: Exception) {
            // Network failure: the mission-critical case. Retry forever.
            scheduleRetry(alert)
        }
    }

    private fun scheduleRetry(alert: PendingAlert) {
        val delay = backoff.nextDelayMs()
        val bumped = alert.copy(attemptCount = alert.attemptCount + 1)
        pending.remove(alert)
        pending.add(bumped)
        onChanged(AlertDelivery.Retrying(bumped, delay))
        retryExecutor.execute {
            try {
                Thread.sleep(delay)
            } catch (_: InterruptedException) {
                return@execute
            }
            attempt(bumped)
        }
    }

    private companion object {
        /** 401/403 retry after re-auth; 429 and 5xx retry on the ladder. */
        val RETRYABLE_HTTP = setOf(401, 403, 408, 429, 500, 502, 503, 504)
    }
}
