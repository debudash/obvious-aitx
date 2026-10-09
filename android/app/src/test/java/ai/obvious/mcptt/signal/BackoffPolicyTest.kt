package ai.obvious.mcptt.signal

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.random.Random

class BackoffPolicyTest {

    @Test
    fun `delays grow exponentially until the cap`() {
        val policy = BackoffPolicy(baseMs = 1_000, multiplier = 2.0, maxMs = 30_000, random = Random(42))
        val delays = List(8) { policy.nextDelayMs() }
        // With full jitter each delay is in [cap/2, cap); the cap sequence
        // itself is 1k,2k,4k,8k,16k,30k,30k,30k — monotone non-decreasing.
        assertEquals(8, delays.size)
        assertTrue(delays.zipWithNext().all { (a, b) -> b >= a / 2 })
        assertTrue(delays.all { it <= 30_000 })
        assertTrue(delays.all { it >= 500 })
    }

    @Test
    fun `reset returns to the base delay`() {
        val policy = BackoffPolicy(baseMs = 1_000, random = Random(7))
        repeat(5) { policy.nextDelayMs() }
        policy.reset()
        val first = policy.nextDelayMs()
        assertTrue(first in 500..1_000)
    }

    @Test
    fun `deterministic under a fixed seed`() {
        val a = BackoffPolicy(random = Random(1))
        val b = BackoffPolicy(random = Random(1))
        assertEquals(List(5) { a.nextDelayMs() }, List(5) { b.nextDelayMs() })
    }

    @Test
    fun `never exceeds the cap regardless of attempt count`() {
        val policy = BackoffPolicy(baseMs = 100, maxMs = 1_000, random = Random(3))
        repeat(50) {
            assertTrue(policy.nextDelayMs() <= 1_000)
        }
    }
}
