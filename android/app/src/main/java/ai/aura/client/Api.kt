package ai.aura.client

import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URI
import java.net.URL

/** Client of AURA's public API (/api/v1). Every request carries the member token; nothing else is trusted. */
class Api(private val base: String, private val token: String) {

    class ApiError(val status: Int, val code: String, message: String) : IOException(message)

    data class Task(
        val id: String, val objective: String, val state: String, val reasonCode: String?, val waitReason: String?, val costUsd: Double,
        val public: Boolean = false, val reply: Reply? = null,
    )

    /** AURA's answer to the owner (ADR-0055). The view is declarative data already sanitised by the Core; the app draws it natively. */
    data class Reply(val text: String, val view: List<Block>, val basis: String, val notes: List<Note>, val files: List<String>)
    data class Note(val code: String, val detail: String)
    data class Item(val label: String, val value: String, val note: String, val url: String, val number: Double?)
    data class Block(
        val type: String, val title: String, val text: String, val tone: String, val chart: String,
        val columns: List<String>, val rows: List<List<String>>, val items: List<Item>,
    )
    data class Approval(
        val id: String, val taskId: String, val toolId: String, val target: String, val actionHash: String,
        val why: String, val objective: String, val estimatedCost: String, val expiresAt: String,
    )
    data class Notice(val id: String, val title: String, val taskId: String?, val read: Boolean)
    data class Event(val seq: Int, val type: String, val data: String)

    fun tasks(limit: Int = 30): List<Task> = parseTasks(call("GET", "/tasks?limit=$limit"))
    fun task(id: String): Task = parseTask(call("GET", "/tasks/${enc(id)}"))
    fun events(id: String): List<Event> = parseEvents(call("GET", "/tasks/${enc(id)}/events?limit=200"))
    fun approvals(): List<Approval> = parseApprovals(call("GET", "/approvals"))
    fun notifications(unreadOnly: Boolean): List<Notice> = parseNotices(call("GET", "/notifications" + if (unreadOnly) "?unread=true" else ""))
    fun markRead(id: String) { call("POST", "/notifications/${enc(id)}/read") }

    fun createTask(objective: String, tools: List<String>, domains: List<String>, public: Boolean): Task {
        val body = JSONObject().put("objective", objective).put("tools", JSONArray(tools)).put("allow_domains", JSONArray(domains))
        if (public) body.put("data_class", "public")
        return parseTask(call("POST", "/tasks", body))
    }

    /** Revokes this device's token on the server. */
    fun logout() { call("POST", "/app/logout") }

    fun answer(id: String, text: String) { call("POST", "/tasks/${enc(id)}/input", JSONObject().put("answer", text)) }

    /** Approves exactly the action the owner saw: the Core checks the hash. */
    fun approve(a: Approval) { call("POST", "/approvals/${enc(a.id)}/approve", JSONObject().put("action_hash", a.actionHash)) }
    fun reject(a: Approval) { call("POST", "/approvals/${enc(a.id)}/reject", JSONObject()) }

    private fun call(method: String, path: String, body: JSONObject? = null): JSONObject {
        val url = URL(checkBase(base) + "/api/v1" + path)
        val c = url.openConnection() as HttpURLConnection
        try {
            c.requestMethod = method
            c.connectTimeout = 15000
            c.readTimeout = 30000
            c.instanceFollowRedirects = false // never send the token to another place
            if (token.isNotEmpty()) c.setRequestProperty("Authorization", "Bearer $token")
            c.setRequestProperty("Accept", "application/json")
            if (body != null) {
                c.doOutput = true
                c.setRequestProperty("Content-Type", "application/json")
                c.outputStream.use { it.write(body.toString().toByteArray()) }
            }
            val status = c.responseCode
            val text = (if (status < 400) c.inputStream else c.errorStream)?.bufferedReader()?.use { it.readText() } ?: ""
            val json = if (text.isBlank()) JSONObject() else JSONObject(text)
            if (status >= 400) {
                val err = json.optJSONObject("error")
                throw ApiError(status, err?.optString("code") ?: "http_$status", err?.optString("message") ?: "HTTP $status")
            }
            return json
        } finally {
            c.disconnect()
        }
    }

    companion object {
        /** Signs in with an app account; the server issues this device's own token (ADR-0053). */
        fun login(base: String, username: String, password: String, device: String): String {
            val body = JSONObject().put("username", username).put("password", password).put("device", device)
            val o = Api(base, "").call("POST", "/app/login", body)
            return o.getString("token")
        }

        /** Only https base URLs without credentials, query or fragment. */
        fun checkBase(raw: String): String {
            val s = raw.trim().trimEnd('/')
            val u = try { URI(s) } catch (e: Exception) { throw IllegalArgumentException("invalid URL") }
            require(u.scheme == "https") { "the server URL must start with https://" }
            require(!u.host.isNullOrBlank() && u.userInfo == null && u.query == null && u.fragment == null) { "invalid server URL" }
            return s
        }

        fun enc(s: String): String = java.net.URLEncoder.encode(s, "UTF-8")

        private fun str(o: JSONObject, k: String): String? = if (o.isNull(k)) null else o.optString(k)

        fun parseTask(o: JSONObject) = Task(o.getString("id"), o.optString("objective"), o.optString("state"),
            str(o, "reason_code"), str(o, "wait_reason"), o.optDouble("cost_usd", 0.0),
            o.optString("data_class") == "public", o.optJSONObject("reply")?.let { parseReply(it) })

        private fun strings(a: JSONArray?): List<String> = if (a == null) emptyList() else (0 until a.length()).map { a.optString(it) }

        private fun <T> objects(a: JSONArray?, f: (JSONObject) -> T): List<T> =
            if (a == null) emptyList() else (0 until a.length()).mapNotNull { a.optJSONObject(it)?.let(f) }

        fun parseReply(o: JSONObject) = Reply(
            o.optString("text"),
            objects(o.optJSONArray("view")) { b ->
                val rows = b.optJSONArray("rows")
                Block(b.optString("type"), b.optString("title"), b.optString("text"), b.optString("tone"), b.optString("chart"),
                    strings(b.optJSONArray("columns")),
                    if (rows == null) emptyList() else (0 until rows.length()).map { strings(rows.optJSONArray(it)) },
                    objects(b.optJSONArray("items")) { i ->
                        Item(i.optString("label"), i.optString("value"), i.optString("note"), i.optString("url"),
                            if (i.has("number") && !i.isNull("number")) i.optDouble("number") else null)
                    })
            },
            o.optString("basis"),
            objects(o.optJSONArray("notes")) { Note(it.optString("code"), it.optString("detail")) },
            strings(o.optJSONArray("files")),
        )

        fun parseTasks(o: JSONObject): List<Task> {
            val a = o.optJSONArray("tasks") ?: return emptyList()
            return (0 until a.length()).map { parseTask(a.getJSONObject(it)) }
        }

        fun parseApprovals(o: JSONObject): List<Approval> {
            val a = o.optJSONArray("approvals") ?: return emptyList()
            return (0 until a.length()).map {
                val x = a.getJSONObject(it)
                Approval(x.getString("id"), x.optString("task_id"), x.optString("tool_id"), x.optString("target"),
                    x.getString("action_hash"), x.optString("why"), x.optString("task_objective"), x.optString("estimated_cost"),
                    x.optString("expires_at"))
            }
        }

        fun parseNotices(o: JSONObject): List<Notice> {
            val a = o.optJSONArray("notifications") ?: return emptyList()
            return (0 until a.length()).map {
                val x = a.getJSONObject(it)
                Notice(x.getString("id"), x.optString("title"), str(x, "task_id"), !x.isNull("read_at"))
            }
        }

        fun parseEvents(o: JSONObject): List<Event> {
            val a = o.optJSONArray("events") ?: return emptyList()
            return (0 until a.length()).map {
                val x = a.getJSONObject(it)
                Event(x.optInt("seq"), x.optString("type"), x.opt("data")?.toString() ?: "")
            }
        }
    }
}
