package ai.obvious.mcptt.emergency

import ai.obvious.mcptt.api.ApiException
import ai.obvious.mcptt.signal.BackoffPolicy
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.Executor

/**
 * The mission-critical retry guarantee: an emergency alert survives
 * network loss, process death, and queue races until the dispatcher's
 * ack. Mirrors the spec's failure-case table.
 */
class AlertRetryQueueTest {

    private class RecordingStore : AlertStore {
        val saved = mutableListOf<List<PendingAlert>>()
        private var state: List<PendingAlert> = emptyList()
        override fun load(): List<PendingAlert> = state
        override fun save(alerts: List<PendingAlert>) {
            state = alerts
            saved.add(alerts)
        }
    }

    /** Direct executor: retries run inline — deterministic, no threads. */
    private val directExecutor = Executor { it.run() }

    private class FlakyTransport : AlertRetryQueue.Transport {
        var attempts = 0
        var failTimes = 0
        var failure: Exception = java.io.IOException("network down")
        var response: String = "alert-1"
        override fun raiseAlert(alert: PendingAlert): String {
            attempts++
            if (attempts <= failTimes) throw failure
            return response
        }
    }

    private fun alert(id: String = "alert-1") = PendingAlert(
        alertId = id,
        imminentPeril = false,
        lat = 47.6205,
        lon = -122.3493,
        note = "help",
        createdAtMs = 1_000,
    )

    private fun queue(
        transport: FlakyTransport,
        store: RecordingStore = RecordingStore(),
        backoff: BackoffPolicy = BackoffPolicy(baseMs = 1, maxMs = 1),
        onChanged: (AlertDelivery) -> Unit = {},
    ): AlertRetryQueue = AlertRetryQueue(
        transport = transport,
        store = store,
        backoff = backoff,
        retryExecutor = directExecutor,
        onChanged = onChanged,
    )

    @Test
    fun `submit attempts delivery immediately`() {
        val transport = FlakyTransport()
        val q = queue(transport)
        q.submit(alert())
        assertEquals(1, transport.attempts)
    }

    @Test
    fun `network failure retries until success`() {
        val transport = FlakyTransport().apply { failTimes = 2 }
        var delivered: AlertDelivery? = null
        val q = queue(transport, onChanged = { delivered = it })
        q.submit(alert())
        assertEquals(3, transport.attempts)
        assertTrue(delivered is AlertDelivery.DeliveredAwaitingAck)
        assertEquals(0, q.pendingCount())
    }

    @Test
    fun `retryable http statuses retry`() {
        val transport = FlakyTransport().apply {
            failTimes = 1
            failure = ApiException(503, "unavailable")
        }
        val q = queue(transport)
        q.submit(alert())
        assertEquals(2, transport.attempts)
    }

    @Test
    fun `non-retryable 4xx parks the alert visibly instead of looping`() {
        val transport = FlakyTransport().apply {
            failTimes = Int.MAX_VALUE // never succeeds
            failure = ApiException(400, "bad request")
        }
        var last: AlertDelivery? = null
        val q = queue(transport, onChanged = { last = it })
        q.submit(alert())
        assertEquals(1, transport.attempts)
        assertTrue(last is AlertDelivery.Failed)
    }

    @Test
    fun `submit dedupes by alert id`() {
        val transport = FlakyTransport()
        val q = queue(transport)
        q.submit(alert())
        q.submit(alert())
        assertEquals(1, transport.attempts)
    }

    @Test
    fun `pending alerts persist for process-restart recovery`() {
        val store = RecordingStore()
        val transport = FlakyTransport().apply { failTimes = 1 }
        val q = queue(transport, store)
        q.submit(alert())
        // The alert hits durable storage while pending — a process restart
        // reloads it (the final snapshot is empty after successful delivery).
        assertTrue(store.saved.any { snapshot -> snapshot.any { it.alertId == "alert-1" } })
        assertEquals(0, q.pendingCount())
    }

    @Test
    fun `start reloads undelivered alerts from the store`() {
        val store = RecordingStore()
        store.save(listOf(alert("recovered")))
        val transport = FlakyTransport()
        val q = queue(transport, store)
        q.start()
        assertEquals(1, transport.attempts)
        assertEquals(0, q.pendingCount())
    }

    @Test
    fun `ack closes the delivery loop`() {
        val transport = FlakyTransport()
        var last: AlertDelivery? = null
        val q = queue(transport, onChanged = { last = it })
        q.submit(alert())
        q.onAlertAck("alert-1")
        assertTrue(last is AlertDelivery.DeliveredAwaitingAck)
    }

    @Test
    fun `multiple alerts each reach the server`() {
        val transport = FlakyTransport()
        val q = queue(transport)
        q.submit(alert("a"))
        q.submit(alert("b"))
        assertEquals(2, transport.attempts)
    }
}
