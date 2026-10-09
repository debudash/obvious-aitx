package ai.obvious.mcptt.store

import ai.obvious.mcptt.MissedCall
import org.json.JSONArray
import org.json.JSONObject

/** Durable missed-call log, newest first, capped. Pure-JSON for JVM tests. */
interface MissedCallStore {
    fun load(): List<MissedCall>
    fun add(call: MissedCall)
    fun clear()
}

class JsonMissedCallStore(
    private val backing: Backing,
    private val cap: Int = 50,
) : MissedCallStore {

    interface Backing {
        fun read(): String?
        fun write(json: String?)
    }

    override fun load(): List<MissedCall> {
        val json = backing.read() ?: return emptyList()
        return runCatching {
            val arr = JSONArray(json)
            (0 until arr.length()).map { i ->
                val obj = arr.getJSONObject(i)
                MissedCall(
                    callId = obj.getString("callId"),
                    fromUserId = obj.getString("fromUserId"),
                    atMs = obj.getLong("atMs"),
                )
            }
        }.getOrDefault(emptyList())
    }

    override fun add(call: MissedCall) {
        val current = load().toMutableList()
        current.removeAll { it.callId == call.callId }
        current.add(0, call)
        val arr = JSONArray()
        current.take(cap).forEach { entry ->
            arr.put(
                JSONObject()
                    .put("callId", entry.callId)
                    .put("fromUserId", entry.fromUserId)
                    .put("atMs", entry.atMs),
            )
        }
        backing.write(arr.toString())
    }

    override fun clear() {
        backing.write(null)
    }
}
