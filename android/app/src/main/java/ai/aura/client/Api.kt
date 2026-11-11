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
        /** What AURA understood a spoken request to mean (ADR-0058); null when typed or not yet planned. */
        val heardAs: String? = null,
        val updatedAt: String = "",
    )

    /** AURA's answer to the owner (ADR-0055). The view is declarative data already sanitised by the Core; the app draws it natively. */
    data class Reply(
        val text: String, val view: List<Block>, val basis: String, val notes: List<Note>, val files: List<String>,
        val scene: Scene = Scene(),
    )

    /** The look a reply or screen asks for (ADR-0060); an empty field leaves the choice to the app. */
    data class Scene(val mood: String = "", val accent: String = "", val layout: String = "")
    data class Note(val code: String, val detail: String)
    /** [image] is the id of a picture the Core found and stored for a figure (ADR-0061); the app loads it from /media. */
    data class Item(val label: String, val value: String, val note: String, val url: String, val number: Double?, val image: String = "")
    data class Block(
        val type: String, val title: String, val text: String, val tone: String, val chart: String,
        val columns: List<String>, val rows: List<List<String>>, val items: List<Item>,
        /** Presentation (ADR-0060): what AURA says while the block is shown, one emoji, "half", "strong" | "quiet". */
        val say: String = "", val icon: String = "", val span: String = "", val emphasis: String = "",
        /** A screen's widget block: which live data the app loads, and how many entries. */
        val source: String = "", val limit: Int = 0,
    )

    /** A page AURA designed: data the app draws, never code. */
    data class Screen(val scene: Scene, val blocks: List<Block>)
    data class ScreenVersion(val version: Int, val screen: Screen, val request: String, val summary: String, val active: Boolean)

    /** What is going on right now, computed by the Core from its records. */
    data class HomeContext(
        val dayPart: String = "", val approvals: Int = 0, val questions: Int = 0, val running: Int = 0, val unread: Int = 0,
        val failedToday: Int = 0, val doneToday: Int = 0, val costTodayUsd: Double = 0.0, val providerIssues: List<String> = emptyList(),
        val scene: Scene = Scene(), val headline: String = "idle",
    )
    data class Home(val version: Int, val screen: Screen, val context: HomeContext)
    data class Memory(val id: String, val kind: String, val content: String)
    data class Skill(val id: String, val name: String, val status: String, val triggers: List<String>)
    data class Trigger(val id: String, val name: String, val kind: String, val objective: String, val enabled: Boolean, val dailyAt: String)
    data class Limitation(val id: String, val title: String, val status: String, val category: String)
    data class Self(val summary: String, val version: String, val finished: Int, val succeeded: Int, val verified: Int, val skills: Int, val memories: Int)
    /** One live step of a task, from the event stream. */
    data class Live(val taskId: String, val type: String, val data: JSONObject)
    data class Approval(
        val id: String, val taskId: String, val toolId: String, val target: String, val actionHash: String,
        val why: String, val objective: String, val estimatedCost: String, val expiresAt: String,
    )
    data class Notice(val id: String, val title: String, val taskId: String?, val read: Boolean)
    data class Event(val seq: Int, val type: String, val data: String)
    /** One note of the owner, kept by the Core (ADR-0061). */
    data class NoteItem(val id: String, val text: String, val done: Boolean, val taskId: String)

    fun tasks(limit: Int = 30): List<Task> = parseTasks(call("GET", "/tasks?limit=$limit"))
    fun task(id: String): Task = parseTask(call("GET", "/tasks/${enc(id)}"))
    fun events(id: String): List<Event> = parseEvents(call("GET", "/tasks/${enc(id)}/events?limit=200"))
    fun approvals(): List<Approval> = parseApprovals(call("GET", "/approvals"))
    fun notifications(unreadOnly: Boolean): List<Notice> = parseNotices(call("GET", "/notifications" + if (unreadOnly) "?unread=true" else ""))
    fun markRead(id: String) { call("POST", "/notifications/${enc(id)}/read") }

    fun createTask(
        objective: String, tools: List<String>, domains: List<String>, public: Boolean, spoken: List<LiveTalk.Alt> = emptyList(),
        design: String? = null,
    ): Task = parseTask(call("POST", "/tasks", taskJson(objective, tools, domains, public, spoken, design)))

    fun cancel(id: String) { call("POST", "/tasks/${enc(id)}/cancel", JSONObject()) }

    // Designed screens (ADR-0060).
    fun home(): Home = parseHome(call("GET", "/ui/home"))
    fun screenVersions(name: String): List<ScreenVersion> = parseVersions(call("GET", "/ui/screens/${enc(name)}"))
    /** action: rollback | reset | use (with a version). */
    fun screenAction(name: String, action: String, version: Int = 0) {
        call("POST", "/ui/screens/${enc(name)}/${enc(action)}", if (action == "use") JSONObject().put("version", version) else null)
    }

    fun memories(): List<Memory> = objects(call("GET", "/memories").optJSONArray("memories")) {
        Memory(it.optString("id"), it.optString("kind"), it.optString("content"))
    }
    fun skills(): List<Skill> = objects(call("GET", "/skills").optJSONArray("skills")) {
        Skill(it.optString("id"), it.optString("name"), it.optString("status"), strings(it.optJSONArray("triggers")))
    }
    fun triggers(): List<Trigger> = objects(call("GET", "/triggers").optJSONArray("triggers")) {
        Trigger(it.optString("id"), it.optString("name"), it.optString("kind"), it.optString("objective"), it.optBoolean("enabled"), it.optString("daily_at"))
    }
    fun limitations(): List<Limitation> = objects(call("GET", "/limitations").optJSONArray("limitations")) {
        Limitation(it.optString("id"), it.optString("title"), it.optString("status"), it.optString("category"))
    }
    fun self(): Self = parseSelf(call("GET", "/self"))

    // The workspace (ADR-0061): the owner's notes and the pictures of a reply's figures.
    fun notes(): List<NoteItem> = parseNotes(call("GET", "/notes?limit=100"))
    fun addNote(text: String) { call("POST", "/notes", JSONObject().put("text", text)) }
    fun tickNote(id: String, done: Boolean) { call("PATCH", "/notes/${enc(id)}", JSONObject().put("done", done)) }
    fun deleteNote(id: String) { call("DELETE", "/notes/${enc(id)}") }

    /** The bytes of a stored picture. The id is a content hash, so nothing else can be asked for through it. */
    fun media(id: String): ByteArray {
        require(MEDIA_ID.matches(id)) { "invalid picture id" }
        val c = URL(checkBase(base) + "/api/v1/media/" + id).openConnection() as HttpURLConnection
        try {
            c.connectTimeout = 15000
            c.readTimeout = 30000
            c.instanceFollowRedirects = false
            c.setRequestProperty("Authorization", "Bearer $token")
            if (c.responseCode != 200) throw ApiError(c.responseCode, "http_${c.responseCode}", "HTTP ${c.responseCode}")
            val out = java.io.ByteArrayOutputStream()
            c.inputStream.use { input ->
                val buf = ByteArray(16384)
                while (true) {
                    val n = input.read(buf)
                    if (n < 0) break
                    out.write(buf, 0, n)
                    if (out.size() > MAX_MEDIA) throw IOException("picture too large")
                }
            }
            return out.toByteArray()
        } finally {
            c.disconnect()
        }
    }

    /**
     * Follows the Core's event stream until [alive] is false or the connection ends. Events carry metadata only
     * (which step, which tool), never content.
     */
    fun stream(alive: () -> Boolean, onEvent: (Live) -> Unit) {
        val c = URL(checkBase(base) + "/api/v1/stream").openConnection() as HttpURLConnection
        try {
            c.connectTimeout = 15000
            c.readTimeout = 40000 // the Core sends a keep-alive every 15 s
            c.instanceFollowRedirects = false
            c.setRequestProperty("Authorization", "Bearer $token")
            c.setRequestProperty("Accept", "text/event-stream")
            if (c.responseCode != 200) throw ApiError(c.responseCode, "http_${c.responseCode}", "HTTP ${c.responseCode}")
            c.inputStream.bufferedReader().use { r ->
                while (alive()) {
                    val line = r.readLine() ?: break
                    if (line.startsWith("data: ")) parseLive(line.substring(6))?.let(onEvent)
                }
            }
        } finally {
            c.disconnect()
        }
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
        val MEDIA_ID = Regex("^[0-9a-f]{64}$")
        const val MAX_MEDIA = 1 shl 20

        fun parseNotes(o: JSONObject): List<NoteItem> = objects(o.optJSONArray("notes")) {
            NoteItem(it.optString("id"), it.optString("text"), it.optBoolean("done"), it.optString("task_id"))
        }

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

        fun taskJson(objective: String, tools: List<String>, domains: List<String>, public: Boolean, spoken: List<LiveTalk.Alt>, design: String?): JSONObject {
            val body = JSONObject().put("objective", objective).put("tools", JSONArray(tools)).put("allow_domains", JSONArray(domains))
            if (public) body.put("data_class", "public")
            if (spoken.isNotEmpty()) body.put("voice", voiceJson(spoken))
            if (!design.isNullOrBlank()) body.put("design", design)
            return body
        }

        fun parseScene(o: JSONObject?) = if (o == null) Scene() else Scene(o.optString("mood"), o.optString("accent"), o.optString("layout"))

        fun parseBlocks(a: JSONArray?): List<Block> = objects(a) { b ->
            val rows = b.optJSONArray("rows")
            Block(b.optString("type"), b.optString("title"), b.optString("text"), b.optString("tone"), b.optString("chart"),
                strings(b.optJSONArray("columns")),
                if (rows == null) emptyList() else (0 until rows.length()).map { strings(rows.optJSONArray(it)) },
                objects(b.optJSONArray("items")) { i ->
                    Item(i.optString("label"), i.optString("value"), i.optString("note"), i.optString("url"),
                        if (i.has("number") && !i.isNull("number")) i.optDouble("number") else null, i.optString("image"))
                },
                b.optString("say"), b.optString("icon"), b.optString("span"), b.optString("emphasis"),
                b.optString("source"), b.optInt("limit", 0))
        }

        fun parseScreen(o: JSONObject?) = Screen(parseScene(o?.optJSONObject("scene")), parseBlocks(o?.optJSONArray("blocks")))

        private fun parseVersion(v: JSONObject) = ScreenVersion(v.optInt("version"), parseScreen(v.optJSONObject("screen")),
            v.optString("request"), v.optString("summary"), v.optBoolean("active"))

        fun parseHome(o: JSONObject): Home {
            val v = o.optJSONObject("screen") ?: JSONObject()
            val c = o.optJSONObject("context") ?: JSONObject()
            return Home(v.optInt("version"), parseScreen(v.optJSONObject("screen")), HomeContext(
                c.optString("daypart"), c.optInt("approvals"), c.optInt("questions"), c.optInt("running"), c.optInt("unread"),
                c.optInt("failed_today"), c.optInt("done_today"), c.optDouble("cost_today_usd", 0.0), strings(c.optJSONArray("provider_issues")),
                parseScene(c.optJSONObject("scene")), c.optString("headline", "idle")))
        }

        fun parseVersions(o: JSONObject): List<ScreenVersion> = objects(o.optJSONArray("versions")) { parseVersion(it) }

        fun parseSelf(o: JSONObject): Self {
            val t = o.optJSONObject("self_model")?.optJSONObject("tasks") ?: JSONObject()
            return Self(o.optString("summary"), o.optString("version"), t.optInt("finished"), t.optInt("succeeded"), t.optInt("verified"),
                o.optInt("skills_active"), o.optInt("memories_active"))
        }

        fun parseLive(data: String): Live? = runCatching {
            val o = JSONObject(data)
            Live(o.optString("task_id"), o.optString("type"), o.optJSONObject("data") ?: JSONObject())
        }.getOrNull()?.takeIf { it.taskId.isNotEmpty() && it.type.isNotEmpty() }

        fun parseTask(o: JSONObject) = Task(o.getString("id"), o.optString("objective"), o.optString("state"),
            str(o, "reason_code"), str(o, "wait_reason"), o.optDouble("cost_usd", 0.0),
            o.optString("data_class") == "public", o.optJSONObject("reply")?.let { parseReply(it) }, str(o, "heard_as")?.takeIf { it.isNotBlank() },
            o.optString("updated_at"))

        /** The recognizer's hypotheses as the task's voice input (ADR-0058). */
        fun voiceJson(alts: List<LiveTalk.Alt>): JSONObject = JSONObject().put("alternatives", JSONArray(alts.take(5).map { a ->
            JSONObject().put("text", a.text.take(2000)).also { o -> a.confidence?.let { o.put("confidence", it.toDouble()) } }
        }))

        private fun strings(a: JSONArray?): List<String> = if (a == null) emptyList() else (0 until a.length()).map { a.optString(it) }

        private fun <T> objects(a: JSONArray?, f: (JSONObject) -> T): List<T> =
            if (a == null) emptyList() else (0 until a.length()).mapNotNull { a.optJSONObject(it)?.let(f) }

        fun parseReply(o: JSONObject) = Reply(
            o.optString("text"),
            parseBlocks(o.optJSONArray("view")),
            o.optString("basis"),
            objects(o.optJSONArray("notes")) { Note(it.optString("code"), it.optString("detail")) },
            strings(o.optJSONArray("files")),
            parseScene(o.optJSONObject("scene")),
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
