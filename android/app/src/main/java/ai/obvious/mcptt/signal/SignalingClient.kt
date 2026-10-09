package ai.obvious.mcptt.signal

import ai.obvious.mcptt.protocol.McpttMessage
import ai.obvious.mcptt.protocol.McpttProtocol
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

/** Signaling connection lifecycle — what the UI banner renders. */
sealed interface SignalState {
    data object Disconnected : SignalState
    data object Connecting : SignalState
    data object Connected : SignalState

    /** Down; sleeping before the next dial. */
    data class Retrying(val delayMs: Long) : SignalState
}

interface SignalListener {
    fun onStateChange(state: SignalState) {}
    fun onMessage(message: McpttMessage) {}

    /** A fresh connection is live — listeners refresh rosters after a gap. */
    fun onReconnected() {}
}

/** Derives the server's /ws dial URL from the REST base URL. */
fun signalingUrl(baseUrl: String, token: String): String {
    val wsBase = when {
        baseUrl.startsWith("https://") -> "wss://" + baseUrl.removePrefix("https://")
        baseUrl.startsWith("http://") -> "ws://" + baseUrl.removePrefix("http://")
        else -> baseUrl
    }.trimEnd('/')
    return "$wsBase/ws?token=$token"
}

/**
 * OkHttp WebSocket signaling client with auto-reconnect (exponential
 * backoff, full jitter) and typed protocol frames. The server verifies
 * the JWT pre-upgrade via the ?token= query parameter (browsers cannot
 * set headers on WS dials; the mobile client keeps the same contract).
 *
 * OkHttp answers the server's pings automatically; pingInterval keeps
 * half-open NATs honest from our side too.
 */
class SignalingClient(
    private val baseUrl: String,
    private val httpClient: OkHttpClient,
    private val tokenProvider: () -> String?,
    private val backoff: BackoffPolicy = BackoffPolicy(),
    private val scheduler: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor { r ->
        Thread(r, "mcptt-signal").apply { isDaemon = true }
    },
    private val listener: SignalListener,
) {
    private val socket = AtomicReference<WebSocket?>(null)
    private val desired = AtomicBoolean(false)

    @Volatile
    var signalState: SignalState = SignalState.Disconnected
        private set

    fun connect() {
        desired.set(true)
        dial()
    }

    /** Clean shutdown: no further reconnect attempts. */
    fun close() {
        desired.set(false)
        scheduler.shutdownNow()
        socket.getAndSet(null)?.close(NORMAL_CLOSE, "client shutdown")
    }

    /**
     * Send a frame now. Returns false when the socket is not open — the
     * caller decides whether to queue (alerts do, via AlertRetryQueue) or
     * surface the loss (a PTT press re-requests).
     */
    fun send(message: McpttMessage): Boolean {
        val ws = socket.get() ?: return false
        return ws.send(McpttProtocol.encode(message))
    }

    @Synchronized
    private fun setState(next: SignalState) {
        signalState = next
        listener.onStateChange(next)
    }

    private fun dial() {
        val token = tokenProvider() ?: run {
            setState(SignalState.Disconnected)
            return
        }
        setState(SignalState.Connecting)
        val request = Request.Builder()
            .url(signalingUrl(baseUrl, token))
            .build()
        socket.set(httpClient.newWebSocket(request, WsListener()))
    }

    private inner class WsListener : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            backoff.reset()
            setState(SignalState.Connected)
            // Presence re-broadcast is automatic server-side on connect.
            listener.onReconnected()
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            val message = McpttProtocol.decode(text) ?: return // unknown type: forward-compatible skip
            listener.onMessage(message)
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            socket.compareAndSet(webSocket, null)
            if (!desired.get()) {
                setState(SignalState.Disconnected)
                return
            }
            scheduleReconnect()
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            socket.compareAndSet(webSocket, null)
            if (!desired.get()) {
                setState(SignalState.Disconnected)
                return
            }
            scheduleReconnect()
        }
    }

    private fun scheduleReconnect() {
        val delay = backoff.nextDelayMs()
        setState(SignalState.Retrying(delay))
        scheduler.schedule({
            if (desired.get()) dial()
        }, delay, TimeUnit.MILLISECONDS)
    }

    companion object {
        const val NORMAL_CLOSE = 1000
    }
}
