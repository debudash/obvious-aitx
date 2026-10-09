package ai.obvious.mcptt.signal

import kotlin.math.min
import kotlin.math.pow
import kotlin.random.Random

/**
 * Exponential backoff with full jitter for reconnects and emergency-alert
 * retries. Mission-critical clients retry forever; the cap bounds each
 * wait, not the attempt count. Deterministic under an injected Random.
 */
class BackoffPolicy(
    private val baseMs: Long = DEFAULT_BASE_MS,
    private val multiplier: Double = DEFAULT_MULTIPLIER,
    private val maxMs: Long = DEFAULT_MAX_MS,
    private val random: Random = Random.Default,
) {
    private var attempt = 0

    /** Delay before the next attempt; grows exponentially with jitter. */
    fun nextDelayMs(): Long {
        val exponential = baseMs * multiplier.pow(attempt)
        val capped = min(exponential.toLong(), maxMs)
        attempt++
        // Full jitter: uniform in [capped/2, capped) — spreads retries.
        return (capped / 2 + (random.nextDouble() * (capped - capped / 2))).toLong().coerceAtLeast(baseMs / 2)
    }

    /** A successful exchange resets the ladder. */
    fun reset() {
        attempt = 0
    }

    companion object {
        const val DEFAULT_BASE_MS: Long = 1_000L
        const val DEFAULT_MULTIPLIER: Double = 2.0
        const val DEFAULT_MAX_MS: Long = 30_000L
    }
}
