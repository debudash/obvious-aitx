package ai.obvious.mcptt.smoke

import ai.obvious.mcptt.api.McpttApi
import ai.obvious.mcptt.model.User
import ai.obvious.mcptt.signal.SignalingClient
import ai.obvious.mcptt.signal.SignalListener
import ai.obvious.mcptt.signal.SignalState
import okhttp3.OkHttpClient
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Runtime smoke against a live MCPTT server: the real REST client and the
 * real signaling client exercise the actual wire contract — login, roster,
 * groups, affiliations, WebSocket connect and first server frames.
 *
 * Gated on the `mcptt.liveServer` system property: absent (CI, plain
 * `gradlew test`) the suite skips itself; with a server running locally:
 *
 * ```
 * ./gradlew :app:testDebugUnitTest --tests "*LiveServerSmoke*" \
 *   -Dmcptt.liveServer=http://localhost:18080
 * ```
 *
 * This is the runtime-smoke evidence for the spec's Android acceptance row
 * (no emulator exists in a Linux sandbox; the protocol/state layers under
 * test here are the same JVM code the app runs).
 */
class LiveServerSmokeTest {

    private val server: String? = System.getProperty("mcptt.liveServer")?.takeIf { it.isNotBlank() }

    private fun seededUser() = ("bravo_2" to "mcptt-demo-2026")

    @Test
    fun `login, roster, and signaling work against the live server`() {
        val base = server ?: return // skipped: no live server this run
        val http = OkHttpClient.Builder()
            .connectTimeout(5, TimeUnit.SECONDS)
            .readTimeout(10, TimeUnit.SECONDS)
            .build()
        val api = McpttApi(http, base)

        val login = api.login(seededUser().first, seededUser().second)
        assertTrue("token must be a JWT (three segments)", login.token.count { it == '.' } == 2)
        assertEquals("bravo_2", login.user.username)
        assertEquals(5, login.user.priority)

        api.authToken = login.token
        val me: User = api.me()
        assertEquals(login.user.id, me.id)

        val users = api.listUsers()
        assertTrue("seeded roster has 9 users", users.size >= 9)

        val groups = api.listGroups()
        assertTrue("seeded groups exist", groups.isNotEmpty())

        val affiliations = api.listAffiliations()
        assertTrue("bravo_2 is affiliated by seed", affiliations.isNotEmpty())
        val first = api.affiliate(groups.first().id)
        assertEquals(groups.first().id, first.groupId)

        // Real signaling socket: connect, reach Connected, observe frames.
        val connected = CountDownLatch(1)
        val anyFrame = CountDownLatch(1)
        val states = mutableListOf<SignalState>()
        val signaling = SignalingClient(
            baseUrl = base,
            httpClient = http,
            tokenProvider = { login.token },
            listener = object : SignalListener {
                override fun onStateChange(state: SignalState) {
                    synchronized(states) { states.add(state) }
                    if (state == SignalState.Connected) connected.countDown()
                }

                override fun onMessage(message: ai.obvious.mcptt.protocol.McpttMessage) {
                    anyFrame.countDown()
                }
            },
        )
        try {
            signaling.connect()
            assertTrue("signaling reached Connected in 10 s", connected.await(10, TimeUnit.SECONDS))
            // Server announces presence/affiliation state to new sockets.
            assertTrue(
                "received at least one server frame in 10 s",
                anyFrame.await(10, TimeUnit.SECONDS),
            )
        } finally {
            signaling.close()
        }
    }
}
