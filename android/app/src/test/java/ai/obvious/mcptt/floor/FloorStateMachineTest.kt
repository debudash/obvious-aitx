package ai.obvious.mcptt.floor

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Client mirror of the server's floor machine (mcptt/server/internal/floor
 * tests): same states, same transitions, same effects. The PTT button
 * renders exactly these states.
 */
class FloorStateMachineTest {

    private val callId = "call_1"
    private val me = "radio_bravo"
    private val other = "radio_charlie"

    private fun machine(): FloorStateMachine = FloorStateMachine(me)

    private fun granted(m: FloorStateMachine, callId: String = this.callId, t: Long = 1_000): Transition {
        return m.onEvent(FloorEvent.GrantReceived(callId, me, t, 60_000))
    }

    @Test
    fun `press from idle requests the floor`() {
        val m = machine()
        val t = m.onEvent(FloorEvent.PttPressed(callId, 1_000, priority = 5))
        assertEquals(FloorState.Requesting, m.state)
        assertTrue(t.effects.any { it is FloorEffect.SendFloorRequest })
    }

    @Test
    fun `grant moves to granted and starts transmit`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        val t = granted(m)
        assertTrue(m.state is FloorState.Granted)
        assertEquals(1_000, (m.state as FloorState.Granted).burstStartedAtMs)
        assertTrue(t.effects.any { it is FloorEffect.StartTransmit })
    }

    @Test
    fun `release returns to idle and notifies server`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        granted(m)
        val t = m.onEvent(FloorEvent.PttReleased(callId, 2_000))
        assertEquals(FloorState.Idle, m.state)
        assertTrue(t.effects.any { it is FloorEffect.SendFloorRelease })
        assertTrue(t.effects.any { it is FloorEffect.StopTransmit })
    }

    @Test
    fun `press while already granted is a no-op`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        granted(m)
        val t = m.onEvent(FloorEvent.PttPressed(callId, 1_100, 5))
        assertTrue(m.state is FloorState.Granted)
        assertTrue(t.effects.isEmpty())
    }

    @Test
    fun `denied surfaces reason and queue position`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        m.onEvent(FloorEvent.DeniedReceived(callId, "busy", 2, 1_100))
        val state = m.state as FloorState.Denied
        assertEquals("busy", state.reason)
        assertEquals(2, state.queuePosition)
    }

    @Test
    fun `queued position is tracked and re-request is offered`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        m.onEvent(FloorEvent.DeniedReceived(callId, "busy", 3, 1_100))
        // Re-request (user still holds PTT): machine may re-enter requesting.
        val t = m.onEvent(FloorEvent.PttPressed(callId, 1_200, 5))
        assertTrue(
            m.state == FloorState.Requesting || m.state == FloorState.Queued(3),
        )
        assertTrue(t.effects.none { it is FloorEffect.StartTransmit })
    }

    @Test
    fun `pre-emption strips the floor and names the taker`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        granted(m)
        val t = m.onEvent(FloorEvent.PreemptedReceived(callId, other, emergency = true, 1_500))
        val state = m.state as FloorState.Preempted
        assertEquals(other, state.by)
        assertTrue(t.effects.any { it is FloorEffect.StopTransmit })
    }

    @Test
    fun `self-echo pre-emption is ignored`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        granted(m)
        m.onEvent(FloorEvent.PreemptedReceived(callId, me, emergency = true, 1_500))
        assertTrue(m.state is FloorState.Granted)
    }

    @Test
    fun `burst expiry resets the button and releases the floor`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        granted(m, t = 1_000)
        val t = m.onEvent(FloorEvent.Tick(callId, 1_000 + 60_000 + 1))
        assertEquals(FloorState.Idle, m.state)
        assertTrue(t.effects.any { it is FloorEffect.StopTransmit })
        assertTrue(t.effects.any { it is FloorEffect.SendFloorRelease })
    }

    @Test
    fun `re-press after pre-emption re-enters the queue path`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        granted(m)
        m.onEvent(FloorEvent.PreemptedReceived(callId, other, true, 1_500))
        val t = m.onEvent(FloorEvent.PttPressed(callId, 1_600, 5))
        assertEquals(FloorState.Requesting, m.state)
        assertTrue(t.effects.any { it is FloorEffect.SendFloorRequest })
    }

    @Test
    fun `release echo for another user is ignored`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        m.onEvent(FloorEvent.ReleaseEchoReceived(callId, other, 1_100))
        assertEquals(FloorState.Requesting, m.state)
    }

    @Test
    fun `grant for another user does not grant my floor`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        m.onEvent(FloorEvent.GrantReceived(callId, other, 1_100, 60_000))
        assertEquals(FloorState.Requesting, m.state)
    }

    @Test
    fun `call end clears any state`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        granted(m)
        val t = m.onEvent(FloorEvent.CallEndedReceived(callId, 2_000))
        assertEquals(FloorState.Idle, m.state)
        assertTrue(t.effects.any { it is FloorEffect.StopTransmit })
    }

    @Test
    fun `reset returns to idle without effects`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        granted(m)
        m.reset()
        assertEquals(FloorState.Idle, m.state)
    }

    @Test
    fun `tick inside the burst window keeps transmitting`() {
        val m = machine()
        m.onEvent(FloorEvent.PttPressed(callId, 1_000, 5))
        granted(m, t = 1_000)
        m.onEvent(FloorEvent.Tick(callId, 30_000))
        assertTrue(m.state is FloorState.Granted)
    }
}
