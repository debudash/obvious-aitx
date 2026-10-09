package ai.obvious.mcptt.protocol

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ProtocolCodecTest {

    @Test
    fun `floor request round-trips`() {
        val msg = FloorRequest(callId = "call_9f3", userId = "radio_bravo", priority = 7, emergency = false)
        assertEquals(msg, McpttProtocol.decode(McpttProtocol.encode(msg)))
    }

    @Test
    fun `floor granted keeps the queue`() {
        val msg = FloorGranted(callId = "call_9f3", userId = "radio_bravo", queue = listOf("radio_charlie"))
        val decoded = McpttProtocol.decode(McpttProtocol.encode(msg)) as FloorGranted
        assertEquals(listOf("radio_charlie"), decoded.queue)
    }

    @Test
    fun `floor denied carries reason and position`() {
        val msg = FloorDenied(callId = "c", userId = "u", reason = "busy", queuePosition = 2)
        assertEquals(msg, McpttProtocol.decode(McpttProtocol.encode(msg)))
    }

    @Test
    fun `pre-emption carries the emergency flag`() {
        val msg = FloorPreempted(callId = "c", by = "radio_alpha", emergency = true)
        val decoded = McpttProtocol.decode(McpttProtocol.encode(msg)) as FloorPreempted
        assertTrue(decoded.emergency)
    }

    @Test
    fun `call kinds survive the wire`() {
        CallKind.entries.forEach { kind ->
            val msg = CallStart(callId = "c", groupId = "g", kind = kind.wire, initiatorId = "u")
            val decoded = McpttProtocol.decode(McpttProtocol.encode(msg)) as CallStart
            assertEquals(kind.wire, decoded.kind)
        }
    }

    @Test
    fun `emergency alert with location round-trips`() {
        val msg = EmergencyAlert(alertId = "a1", userId = "radio_bravo", lat = 47.6205, lon = -122.3493, emergency = true)
        val decoded = McpttProtocol.decode(McpttProtocol.encode(msg)) as EmergencyAlert
        assertEquals(47.6205, decoded.lat!!, 1e-9)
        assertEquals(-122.3493, decoded.lon!!, 1e-9)
    }

    @Test
    fun `emergency alert ack round-trips`() {
        val msg = EmergencyAlertAck(alertId = "a1", acknowledgedBy = "dispatcher_1")
        assertEquals(msg, McpttProtocol.decode(McpttProtocol.encode(msg)))
    }

    @Test
    fun `presence and affiliation round-trip`() {
        assertEquals(
            PresenceUpdate(userId = "u", state = PresenceUpdate.STATE_ONLINE, at = 1L),
            McpttProtocol.decode(McpttProtocol.encode(PresenceUpdate(userId = "u", state = PresenceUpdate.STATE_ONLINE, at = 1L))),
        )
        assertEquals(
            AffiliationChanged(userId = "u", groupId = "g", state = AffiliationChanged.STATE_AFFILIATED, at = 1L),
            McpttProtocol.decode(McpttProtocol.encode(AffiliationChanged(userId = "u", groupId = "g", state = AffiliationChanged.STATE_AFFILIATED, at = 1L))),
        )
    }

    @Test
    fun `unknown types decode to null for forward compatibility`() {
        assertNull(McpttProtocol.decode("""{"type":"SomeFutureFrame","x":1}"""))
        assertNull(McpttProtocol.decode("not json at all"))
    }

    @Test
    fun `optional fields may be absent`() {
        val decoded = McpttProtocol.decode("""{"type":"MediaAnswer","callId":"c"}""") as MediaAnswer
        assertEquals("c", decoded.callId)
        assertNull(decoded.sdp)
    }
}
