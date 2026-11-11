package ai.aura.client

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.ImageBitmap
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** What the app needs from the Android activity. */
interface Host {
    fun micGranted(): Boolean
    fun requestMic(then: () -> Unit)
    fun foreground(): Boolean
    fun keepScreenOn(on: Boolean)
    fun requestNotify()
    fun backgroundChecks(on: Boolean)
    fun device(): String
    fun version(): String
    // The phone's own music library and player (ADR-0061).
    fun audioGranted(): Boolean
    fun requestAudio(then: () -> Unit)
    fun songs(): List<Tunes.Song>
    fun playSong(uri: String, done: () -> Unit, failed: (String) -> Unit)
    fun pauseSong()
    fun resumeSong()
    fun stopSong()
    fun decode(bytes: ByteArray): ImageBitmap?
    /** Opens a document AURA wrote in an app of the phone; false when no app can open it (ADR-0064). */
    fun openFile(name: String, bytes: ByteArray): Boolean
    /** Sets the phone's reminder alarms to [list] (ADR-0063). */
    fun setReminders(list: List<Api.ReminderItem>)
}

/**
 * Everything the screens show and everything the owner can do, in one place. The screens only read this state and
 * call these functions; every action goes through the public API with this device's token (AGENTS rule 17).
 */
class AppState(val store: Settings, private val voice: Voice?, private val policy: SensorPolicy, private val host: Host?) {
    private val scope by lazy { CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate) }
    private var token = store.token

    // ---- where the owner is
    var signedIn by mutableStateOf(token.isNotEmpty())
    var tab by mutableStateOf("home")
    /** A page opened from "Lainnya": board, memory, skills, schedules, self, limits, look, settings. */
    var page by mutableStateOf<String?>(null)
    var talkOpen by mutableStateOf(false)
    var message by mutableStateOf<String?>(null)
    var offline by mutableStateOf(false)
    var busy by mutableStateOf(false)

    // ---- what the Core says
    var home by mutableStateOf<Api.Home?>(null)
    var tasks by mutableStateOf<List<Api.Task>>(emptyList())
    var approvals by mutableStateOf<List<Api.Approval>>(emptyList())
    var notices by mutableStateOf<List<Api.Notice>>(emptyList())
    var memories by mutableStateOf<List<Api.Memory>>(emptyList())
    var skills by mutableStateOf<List<Api.Skill>>(emptyList())
    var triggers by mutableStateOf<List<Api.Trigger>>(emptyList())
    var limitations by mutableStateOf<List<Api.Limitation>>(emptyList())
    var self by mutableStateOf<Api.Self?>(null)
    var versions by mutableStateOf<List<Api.ScreenVersion>>(emptyList())
    /** What each running task is doing right now, from the event stream. */
    val activity = mutableStateMapOf<String, String>()
    /** The questions of tasks that wait for an answer. */
    val questions = mutableStateMapOf<String, String>()

    val version: String get() = host?.version() ?: ""
    val username: String get() = store.username

    // ---- the composer
    var draft by mutableStateOf("")
    var public by mutableStateOf(false)

    // ---- one task, opened
    var openTask by mutableStateOf<Api.Task?>(null)
    var openEvents by mutableStateOf<List<Api.Event>>(emptyList())

    // ---- talking
    var micOn by mutableStateOf(false)
    var talkState by mutableStateOf(LiveTalk.State.OFF)
    var presenting by mutableStateOf(false)
    var heard by mutableStateOf("")
    var heardAs by mutableStateOf("")
    var caption by mutableStateOf("")
    var stage by mutableStateOf<List<Api.Block>>(emptyList())
    var stageScene by mutableStateOf(Api.Scene())
    var stageReveal by mutableStateOf(0)
    var stageFocus by mutableStateOf(-1)

    // ---- the workspace (ADR-0061): home is an empty canvas where AURA puts what it is talking about
    /** The typed request the workspace is waiting on. */
    var stageTask by mutableStateOf<String?>(null)
    /** The text box of the workspace is open. */
    var typing by mutableStateOf(false)
    var notes by mutableStateOf<List<Api.NoteItem>>(emptyList())
    var reminders by mutableStateOf<List<Api.ReminderItem>>(emptyList())
    /** Pictures of a reply's figures, by id, once loaded. */
    val pictures = mutableStateMapOf<String, ImageBitmap>()
    private val picturesAsked = mutableSetOf<String>()
    var queue by mutableStateOf<List<Tunes.Song>>(emptyList())
    var playingAt by mutableStateOf(-1)
    var paused by mutableStateOf(false)
    /** Why the player has nothing to play: "" | "permission" | "empty" | "nomatch". */
    var musicState by mutableStateOf("")
    private var cuePending = false
    private var narrating = false
    private var playGen = 0

    private val live = LiveTalk()
    private var beats: List<Narration.Beat>? = null
    private var interrupted = false
    @Volatile private var running = false
    @Volatile private var streaming = false
    private var loop: Job? = null
    private var soon: Job? = null

    init {
        policy.onChange { s ->
            micOn = s == SensorPolicy.State.LISTENING
            if (s == SensorPolicy.State.IDLE) endTalk() // one stop ends everything, the conversation included
        }
    }

    /** The look of this moment: what is on screen asks, what is waiting for the owner decides. */
    val scene: Api.Scene
        get() {
            val moment = home?.context?.scene ?: Api.Scene()
            return when {
                talkOpen || (tab == "home" && page == null) -> HomeLogic.look(stageScene, moment)
                page == "board" -> HomeLogic.look(home?.screen?.scene ?: Api.Scene(), moment)
                else -> HomeLogic.look(Api.Scene(), moment)
            }
        }

    private fun api() = Api(store.server, token)

    private fun <T> bg(quiet: Boolean = false, work: () -> T, done: (T) -> Unit) {
        scope.launch {
            val r = runCatching { withContext(Dispatchers.IO) { work() } }
            r.onSuccess { offline = false; done(it) }.onFailure { e -> report(e, quiet) }
        }
    }

    private fun report(e: Throwable, quiet: Boolean) {
        val api = e as? Api.ApiError
        if (api != null && api.status == 401 && signedIn) {
            signOutLocally()
            message = "Sesi berakhir. Silakan masuk lagi."
            return
        }
        if (quiet) { if (api == null) offline = true; return }
        message = api?.let { "${it.code}: ${it.message}" } ?: (e.message ?: e.toString())
    }

    // ---------------------------------------------------------------- session
    fun login(server: String, user: String, pass: String) {
        val base = try { Api.checkBase(server) } catch (e: IllegalArgumentException) { message = e.message ?: "URL tidak valid"; return }
        if (user.isBlank() || pass.isEmpty()) { message = "Isi nama pengguna dan kata sandi"; return }
        store.server = base
        busy = true
        scope.launch {
            val r = runCatching { withContext(Dispatchers.IO) { Api.login(base, user.trim(), pass, host?.device() ?: "Android") } }
            busy = false
            r.onSuccess { tok ->
                store.token = tok // the password is not kept; only this device's token, encrypted
                store.username = user.trim()
                token = tok
                signedIn = true
                tab = "home"
                resume()
            }.onFailure { report(it, false) }
        }
    }

    fun logout() {
        val a = api()
        scope.launch { runCatching { withContext(Dispatchers.IO) { a.logout() } } } // revoke on the server; sign out locally either way
        signOutLocally()
        message = "Sudah keluar"
    }

    private fun signOutLocally() {
        voice?.stop()
        pause()
        store.token = ""
        token = ""
        signedIn = false
        host?.backgroundChecks(false)
        home = null; tasks = emptyList(); approvals = emptyList(); notices = emptyList(); activity.clear(); questions.clear()
        page = null; talkOpen = false; openTask = null
        stopMusic(); clearStage(); notes = emptyList(); pictures.clear(); picturesAsked.clear(); queue = emptyList(); typing = false
    }

    // ---------------------------------------------------------------- staying current
    /** The app came to the front: load, follow the event stream, keep refreshing while it stays in front. */
    fun resume() {
        if (!signedIn || running) return
        running = true
        refresh()
        loop = scope.launch {
            while (isActive) {
                delay(if (tasks.any { HomeLogic.working(it.state) } || (home?.context?.running ?: 0) > 0) 4000 else 20000)
                refresh()
            }
        }
        if (!streaming) {
            streaming = true
            Thread {
                var wait = 2000L
                while (running) {
                    try {
                        api().stream({ running }) { ev -> scope.launch { onLive(ev) } }
                        wait = 2000L
                    } catch (_: Exception) {
                        wait = minOf(wait * 2, 30000L) // polling keeps the screens current meanwhile
                    }
                    if (running) try { Thread.sleep(wait) } catch (_: InterruptedException) { }
                }
                streaming = false
            }.apply { isDaemon = true }.start()
        }
    }

    fun pause() {
        running = false
        loop?.cancel(); loop = null
        soon?.cancel(); soon = null
    }

    private fun onLive(ev: Api.Live) {
        HomeLogic.activity(ev.type, ev.data)?.let { activity[ev.taskId] = it }
        if (ev.type in REFRESH_ON) refreshSoon()
    }

    private fun refreshSoon() {
        soon?.cancel()
        soon = scope.launch { delay(500); refresh() }
    }

    private class Snapshot(val home: Api.Home?, val tasks: List<Api.Task>, val approvals: List<Api.Approval>, val notices: List<Api.Notice>,
                           val questions: Map<String, String>, val reminders: List<Api.ReminderItem>?)

    fun refresh() {
        if (!signedIn) return
        bg(quiet = true, work = {
            val a = api()
            val ts = a.tasks(40)
            val qs = ts.filter { it.state == "WAITING_INPUT" }.take(5).associate { t ->
                t.id to (runCatching { a.events(t.id).lastOrNull { it.type == "INPUT_REQUESTED" }?.data }.getOrNull()?.let { HomeLogic.question(it) } ?: "")
            }
            Snapshot(runCatching { a.home() }.getOrNull(), ts, a.approvals(), a.notifications(false), qs, runCatching { a.reminders() }.getOrNull())
        }) { s ->
            s.reminders?.let { reminders = it; host?.setReminders(it) }
            s.home?.let { home = it }
            tasks = s.tasks
            approvals = s.approvals
            notices = s.notices
            questions.clear(); questions.putAll(s.questions.filterValues { it.isNotEmpty() })
            activity.keys.filter { id -> s.tasks.none { it.id == id && HomeLogic.working(it.state) } }.forEach { activity.remove(it) }
            openTask?.let { o -> s.tasks.firstOrNull { it.id == o.id }?.let { if (it.state != o.state || it.reply != o.reply) openTask = it } }
        }
    }

    // ---------------------------------------------------------------- navigation
    fun go(t: String) {
        tab = t; page = null
        if (t == "more") bg(quiet = true, work = { api().self() }) { self = it }
        refresh()
    }

    /** One step back; false when there is nowhere to go back to. */
    fun back(): Boolean {
        when {
            talkOpen -> closeTalk()
            openTask != null -> closeTask()
            page != null -> page = null
            tab != "home" -> tab = "home"
            typing -> typing = false
            stage.isNotEmpty() || heard.isNotEmpty() || caption.isNotEmpty() -> clearStage()
            else -> return false
        }
        return true
    }

    /**
     * A request a reply or a designed screen proposes (an action or a filled form). It is model-written text, so it
     * is put in the composer for the owner to read and send, never sent on its own.
     */
    fun propose(text: String) {
        if (talkOpen) closeTalk()
        draft = text
        if (onWorkspace) typing = true else { tab = "chat"; page = null }
    }

    val onWorkspace: Boolean get() = tab == "home" && page == null

    fun openPage(p: String) {
        page = p
        when (p) {
            "memory" -> bg(work = { api().memories() }) { memories = it }
            "skills" -> bg(work = { api().skills() }) { skills = it }
            "schedules" -> {
                bg(work = { api().triggers() }) { triggers = it }
                bg(work = { api().reminders() }) { reminders = it; host?.setReminders(it) }
            }
            "limits" -> bg(work = { api().limitations() }) { limitations = it }
            "self" -> bg(work = { api().self() }) { self = it }
            "look" -> loadVersions()
        }
    }

    /** Loads what a widget of a designed screen shows, once per source. */
    fun need(source: String) {
        if (host == null) return // a preview draws what it was given
        when (source) {
            "skills" -> if (skills.isEmpty()) bg(quiet = true, work = { api().skills() }) { skills = it }
            "memories" -> if (memories.isEmpty()) bg(quiet = true, work = { api().memories() }) { memories = it }
            "schedules" -> if (triggers.isEmpty()) bg(quiet = true, work = { api().triggers() }) { triggers = it }
            "limitations" -> if (limitations.isEmpty()) bg(quiet = true, work = { api().limitations() }) { limitations = it }
            "self" -> if (self == null) bg(quiet = true, work = { api().self() }) { self = it }
        }
    }

    // ---------------------------------------------------------------- requests
    /** Sends a request and stays with it until AURA has answered. */
    fun ask(text: String, design: String? = null) {
        val objective = text.trim()
        if (objective.isEmpty()) return
        val pub = public
        // Asked on the workspace, answered on the workspace; asked in the conversation, answered there.
        val here = onWorkspace && design == null
        draft = ""
        if (here) {
            if (talkState != LiveTalk.State.OFF) stopTalk()
            voice?.hush()
            clearStage(); heard = objective; typing = false
        }
        bg(work = { api().createTask(objective, TOOLS, emptyList(), pub, design = design) }) { t ->
            tasks = listOf(t) + tasks.filter { it.id != t.id }
            activity[t.id] = "Menerima permintaan…"
            if (here) stageTask = t.id else { tab = "chat"; page = null }
            follow(t.id)
        }
    }

    /** A one-tap request: sent as it is, or put in the composer when the owner still has to finish it. */
    fun suggest(s: HomeLogic.Suggestion) {
        if (HomeLogic.needsMore(s)) { draft = s.request; tab = "chat"; page = null; return }
        val was = public
        public = s.public || was // a public suggestion has no personal content; a private one keeps the owner's choice
        ask(s.request)
        public = was
    }

    private fun follow(id: String) {
        scope.launch {
            repeat(300) {
                delay(1200)
                val t = runCatching { withContext(Dispatchers.IO) { api().task(id) } }.getOrNull() ?: return@repeat
                tasks = tasks.map { if (it.id == id) t else it }
                if (!HomeLogic.working(t.state)) {
                    activity.remove(id)
                    refresh()
                    if (stageTask == id) show(t)
                    else if (store.speakResults && !talkOpen) t.reply?.text?.let { voice?.speak(Narration.spoken(it)) }
                    return@launch
                }
            }
        }
    }

    fun answer(taskId: String, text: String) {
        if (text.isBlank()) return
        bg(work = { api().answer(taskId, text.trim()) }) { questions.remove(taskId); message = "Jawaban terkirim"; refresh(); follow(taskId) }
    }

    fun cancel(taskId: String) = bg(work = { api().cancel(taskId) }) { message = "Tugas dibatalkan"; refresh() }

    fun open(taskId: String) {
        tasks.firstOrNull { it.id == taskId }?.let { openTask = it; openEvents = emptyList() }
        bg(work = { val a = api(); a.task(taskId) to a.events(taskId) }) { (t, evs) -> openTask = t; openEvents = evs }
    }

    fun closeTask() { openTask = null; openEvents = emptyList() }

    /** Approves exactly the action the owner saw on screen: the Core checks the hash. Never reachable by voice. */
    fun approve(a: Api.Approval) = bg(work = { api().approve(a) }) { message = "Disetujui"; refresh() }
    fun reject(a: Api.Approval) = bg(work = { api().reject(a) }) { message = "Ditolak"; refresh() }

    fun read(n: Api.Notice) {
        bg(quiet = true, work = { api().markRead(n.id) }) { refresh() }
        n.taskId?.let { open(it) }
    }

    // ---------------------------------------------------------------- AURA designs the app (ADR-0060)
    fun redesign(request: String) {
        if (request.isBlank()) return
        ask(request, design = "home")
    }

    fun loadVersions() = bg(work = { api().screenVersions("home") }) { versions = it }

    fun screenAction(action: String, version: Int = 0) =
        bg(work = { api().screenAction("home", action, version); api().screenVersions("home") }) {
            versions = it
            message = when (action) { "reset" -> "Tampilan bawaan dipasang"; "rollback" -> "Kembali ke tampilan sebelumnya"; else -> "Tampilan versi $version dipasang" }
            refresh()
        }

    // ---------------------------------------------------------------- settings
    fun setMic(on: Boolean) { store.micAllowed = on; if (!on) voice?.stop() }
    fun setBackground(on: Boolean) {
        store.backgroundChecks = on
        if (on) host?.requestNotify()
        host?.backgroundChecks(on)
    }
    fun stopSensors() { voice?.stop(); message = "Semua sensor dihentikan" }
    fun saveServer(server: String) {
        try { store.server = Api.checkBase(server) } catch (e: IllegalArgumentException) { message = e.message ?: "URL tidak valid"; return }
        bg(work = { api().tasks(1) }) { message = "Terhubung ke AURA" }
    }

    // ---------------------------------------------------------------- microphone
    private fun micReady(retry: () -> Unit): Boolean {
        val h = host ?: return false
        val v = voice ?: return false
        if (store.micAllowed && !h.micGranted()) { h.requestMic(retry); return false }
        policy.whyNot(store.micAllowed, h.micGranted(), h.foreground())?.let { message = it; return false }
        if (!v.available()) { message = "Pengenal suara tidak tersedia di perangkat ini"; return false }
        return true
    }

    /** One dictation into the composer. */
    fun dictate() {
        if (!micReady { dictate() }) return
        voice?.listen({ draft = if (draft.isBlank()) it else draft.trimEnd() + " " + it }) { message = it }
    }

    // ---------------------------------------------------------------- live conversation (ADR-0057, ADR-0058, ADR-0060)
    /** Starts a spoken conversation: AURA listens, shows the words as they are spoken, answers aloud and shows what it explains. */
    fun startTalk() {
        if (!micReady { startTalk() }) return
        stopPresenting()
        if (playingAt >= 0 && !paused) togglePlay() // the microphone should hear the owner, not the song
        clearStage()
        tab = "home"; page = null; openTask = null; typing = false
        talkOpen = true
        host?.keepScreenOn(true)
        act(live.start())
    }

    /** Ends the conversation and turns the microphone off. */
    fun stopTalk() { voice?.stop(); endTalk() }

    fun closeTalk() { stopTalk(); stopPresenting(); talkOpen = false }

    private fun endTalk() {
        live.stop()
        talkState = LiveTalk.State.OFF
        host?.keepScreenOn(false)
    }

    /** The owner wants to speak while AURA is still talking. */
    fun interrupt() {
        if (presenting) { stopPresenting(); return }
        if (live.state == LiveTalk.State.SPEAKING) { interrupted = true; voice?.hush() }
    }

    private fun act(d: LiveTalk.Do) {
        talkState = live.state
        when (d) {
            is LiveTalk.Do.Nothing -> {}
            is LiveTalk.Do.Listen -> {
                // Every turn re-checks the sensor rules: consent, permission and foreground may have changed.
                val h = host
                if (h == null || !store.micAllowed || !h.micGranted() || !h.foreground()) { voice?.stop(); return }
                stageFocus = -1
                voice?.hear(
                    { heard = it; heardAs = "" },
                    { alts -> heard = alts.first().text; act(live.heard(alts)) },
                    { act(live.silence()) },
                    { message = it; voice?.stop() })
            }
            is LiveTalk.Do.Submit -> {
                caption = ""; stage = emptyList(); stageReveal = 0; stageScene = Api.Scene(); cuePending = false
                // Every spoken request carries what the recognizer heard, so AURA can repair misheard words.
                val spoken = d.alternatives.ifEmpty { listOf(LiveTalk.Alt(d.text, null)) }
                val pub = public
                turn { api().createTask(d.text, TOOLS, emptyList(), pub, spoken).id }
            }
            is LiveTalk.Do.Answer -> turn { api().answer(d.taskId, d.text); d.taskId }
            is LiveTalk.Do.Speak -> {
                val plan = beats ?: listOf(Narration.Beat(Narration.spoken(d.text), stage.size, -1))
                beats = null
                interrupted = false
                play(plan) { act(live.spoken()) }
            }
            is LiveTalk.Do.End -> {
                caption = d.say
                voice?.stop()
                voice?.speak(d.say)
                refresh()
            }
        }
    }

    /**
     * Goes through the beats in order; each one brings its blocks on screen before it is spoken. Without [aloud] the
     * words are shown as captions for about as long as reading them takes.
     */
    private fun play(plan: List<Narration.Beat>, aloud: Boolean = true, done: () -> Unit) {
        playGen++
        narrating = true
        step(plan, 0, aloud, playGen, done)
    }

    private fun step(plan: List<Narration.Beat>, i: Int, aloud: Boolean, gen: Int, done: () -> Unit) {
        if (gen != playGen) return // something newer is on the workspace
        val v = voice
        if ((aloud && v == null) || interrupted || i >= plan.size) {
            narrating = false
            stageReveal = stage.size; stageFocus = -1
            done()
            // A song the reply queued starts when AURA has finished speaking, and the microphone is turned off for it.
            if (cuePending && !interrupted && queue.isNotEmpty()) { voice?.stop(); playAt(0) }
            cuePending = false
            return
        }
        val b = plan[i]
        stageReveal = maxOf(stageReveal, b.reveal); stageFocus = b.focus; caption = b.say
        when {
            b.say.isEmpty() -> step(plan, i + 1, aloud, gen, done)
            aloud -> v?.speak(b.say) { step(plan, i + 1, aloud, gen, done) }
            else -> scope.launch { delay(Narration.readMillis(b.say)); step(plan, i + 1, aloud, gen, done) }
        }
    }

    private fun turn(send: () -> String) {
        scope.launch {
            val r = runCatching { withContext(Dispatchers.IO) { send() } }
            r.onSuccess { id -> activity[id] = "Menerima permintaan…"; followTurn(id) }.onFailure { e -> act(live.failed(e.message ?: e.toString())) }
        }
    }

    /** Polls the turn's task quickly so the spoken answer follows the question closely. */
    private fun followTurn(id: String) {
        scope.launch {
            repeat(220) {
                delay(800)
                if (live.state != LiveTalk.State.THINKING) return@launch
                val r = runCatching {
                    withContext(Dispatchers.IO) {
                        val a = api()
                        val t = a.task(id)
                        t to if (t.state == "WAITING_INPUT") a.events(id).lastOrNull { it.type == "INPUT_REQUESTED" }?.data else null
                    }
                }
                val (t, q) = r.getOrElse { e -> act(live.failed(e.message ?: e.toString())); return@launch }
                t.heardAs?.let { understood(t.objective, it) }
                if (!HomeLogic.working(t.state)) {
                    activity.remove(id)
                    val d = live.replied(id, t.state, t.reply?.text, q?.let { HomeLogic.question(it) })
                    val reply = t.reply
                    if (d is LiveTalk.Do.Speak && reply != null && d.text == reply.text) {
                        place(reply)
                        beats = Narration.beats(reply)
                    }
                    act(d)
                    refresh()
                    return@launch
                }
            }
            act(live.replied(id, "RUNNING", null, null))
        }
    }

    /** Shows what AURA understood when it differs from what the recognizer heard, and learns the corrected words. */
    private fun understood(transcript: String, meant: String) {
        if (meant.trim().equals(transcript.trim(), ignoreCase = true)) return
        heardAs = meant
        val learned = LiveTalk.learnedWords(transcript, meant)
        if (learned.isNotEmpty()) {
            store.vocabulary = (learned + store.vocabulary).distinctBy { it.lowercase() }.take(100)
            voice?.vocabulary = store.vocabulary
        }
    }

    /** AURA presents a finished reply: it speaks while the workspace shows, block by block, what it is talking about. */
    fun present(t: Api.Task) {
        val reply = t.reply ?: return
        stopTalk()
        clearStage()
        heard = t.objective
        place(reply)
        tab = "home"; page = null; openTask = null
        talkOpen = true
        presenting = true
        interrupted = false
        host?.keepScreenOn(true)
        play(Narration.beats(reply)) { presenting = false; host?.keepScreenOn(false) }
    }

    /** The answer to a request typed on the workspace: shown there, spoken when the owner chose spoken results. */
    private fun show(t: Api.Task) {
        stageTask = null
        val reply = t.reply
        if (reply == null) {
            caption = when (t.state) {
                "WAITING_INPUT" -> "Saya perlu jawaban Tuan dulu — ada di Kotak masuk."
                "WAITING_APPROVAL" -> "Langkah ini menunggu persetujuan Tuan — ada di Kotak masuk."
                else -> HomeLogic.explain(t.reasonCode) ?: HomeLogic.stateLabel(t.state)
            }
            return
        }
        place(reply)
        interrupted = false
        val aloud = store.speakResults && voice != null && onWorkspace && host?.foreground() == true
        presenting = aloud
        play(Narration.beats(reply), aloud) { presenting = false }
    }

    /** Puts a reply on the workspace and prepares what its panels need: the notes, the songs. */
    private fun place(reply: Api.Reply) {
        stage = reply.view; stageScene = reply.scene; stageReveal = 0; stageFocus = -1
        if (reply.view.any { it.type == "note" }) loadNotes()
        if (reply.view.any { it.type == "schedule" }) bg(quiet = true, work = { api().reminders() }) { reminders = it; host?.setReminders(it) }
        reply.view.firstOrNull { it.type == "player" }?.let { cue(it, auto = true) }
    }

    /** Empties the workspace. Music keeps playing; the stop button ends that. */
    fun clearStage() {
        playGen++
        narrating = false; cuePending = false; presenting = false
        heard = ""; heardAs = ""; caption = ""; stage = emptyList(); stageScene = Api.Scene(); stageReveal = 0; stageFocus = -1
        stageTask = null
    }

    /** Opens one of the workspace's own panels without asking AURA: "note" or "player". */
    fun openTool(type: String) {
        if (talkOpen) closeTalk()
        clearStage()
        tab = "home"; page = null; openTask = null
        val b = Api.Block(type, "", "", "", "", emptyList(), emptyList(), emptyList())
        stage = listOf(b); stageReveal = 1
        if (type == "note") loadNotes()
        if (type == "player" && queue.isEmpty()) cue(b, auto = false)
    }

    /** One button that silences everything: the microphone, AURA's voice and the music. */
    fun stopAll() {
        closeTalk()
        voice?.hush()
        playGen++; narrating = false; cuePending = false; presenting = false
        stageReveal = stage.size; stageFocus = -1
        stopMusic()
    }

    val active: Boolean get() = talkState != LiveTalk.State.OFF || presenting || narrating || (playingAt >= 0 && !paused)

    // ---------------------------------------------------------------- files (ADR-0064)
    fun openFile(taskId: String, path: String) {
        message = "Membuka ${path.substringAfterLast('/')}…"
        bg(work = { api().file(taskId, path) }) { b ->
            if (host?.openFile(path.substringAfterLast('/'), b) != true) message = "Tidak ada aplikasi di ponsel ini yang bisa membuka file itu"
        }
    }

    // ---------------------------------------------------------------- reminders (ADR-0063)
    fun deleteReminder(r: Api.ReminderItem) = bg(work = { api().deleteReminder(r.id); api().reminders() }) {
        reminders = it; host?.setReminders(it); message = "Pengingat dihapus"
    }

    // ---------------------------------------------------------------- notes (ADR-0061)
    fun loadNotes() {
        if (host == null) return // a preview draws what it was given
        bg(quiet = true, work = { api().notes() }) { notes = it }
    }

    fun addNote(text: String) {
        if (text.isBlank()) return
        bg(work = { val a = api(); a.addNote(text.trim()); a.notes() }) { notes = it }
    }

    fun tickNote(n: Api.NoteItem) {
        notes = notes.map { if (it.id == n.id) it.copy(done = !n.done) else it }
        bg(work = { val a = api(); a.tickNote(n.id, !n.done); a.notes() }) { notes = it }
    }

    fun deleteNote(n: Api.NoteItem) {
        notes = notes.filter { it.id != n.id }
        bg(work = { val a = api(); a.deleteNote(n.id); a.notes() }) { notes = it }
    }

    // ---------------------------------------------------------------- pictures of a reply's figures
    /** Loads a picture the Core stored, once. The screen reads [pictures]. */
    fun needPicture(id: String) {
        val h = host ?: return
        if (!Api.MEDIA_ID.matches(id) || !picturesAsked.add(id)) return
        bg(quiet = true, work = { h.decode(api().media(id)) }) { it?.let { pic -> pictures[id] = pic } }
    }

    // ---------------------------------------------------------------- music from the phone's own library (ADR-0061)
    /** Builds the queue a player block asks for. With [auto] the first song starts once AURA has finished speaking. */
    fun cue(b: Api.Block, auto: Boolean) {
        val h = host ?: return
        if (!h.audioGranted()) {
            musicState = "permission"
            if (auto) h.requestAudio { cue(b, auto) }
            return
        }
        bg(work = { h.songs() }) { library ->
            val pick = Tunes.pick(library, b.text, b.items.map { it.label to it.value })
            stopMusic()
            queue = pick.songs
            musicState = when { library.isEmpty() -> "empty"; !pick.matched -> "nomatch"; else -> "" }
            if (auto && pick.matched && queue.isNotEmpty()) { if (narrating) cuePending = true else playAt(0) }
        }
    }

    fun allowMusic() {
        val b = stage.firstOrNull { it.type == "player" } ?: return
        host?.requestAudio { cue(b, auto = false) }
    }

    fun musicDenied() { musicState = "permission"; message = "Tanpa izin itu AURA tidak bisa melihat lagu di ponsel" }

    fun playAt(i: Int) {
        val h = host ?: return
        val song = queue.getOrNull(i) ?: run { stopMusic(); return }
        if (talkState != LiveTalk.State.OFF) stopTalk()
        playingAt = i; paused = false
        h.playSong(song.uri, { if (playingAt == i) playAt(i + 1) }) { why -> message = why; if (playingAt == i) { playingAt = -1; paused = false } }
    }

    fun togglePlay() {
        val h = host ?: return
        when {
            playingAt < 0 -> playAt(0)
            paused -> { paused = false; h.resumeSong() }
            else -> { paused = true; h.pauseSong() }
        }
    }

    fun nextSong() { if (queue.isNotEmpty()) playAt(if (playingAt + 1 < queue.size) playingAt + 1 else 0) }
    fun prevSong() { if (queue.isNotEmpty()) playAt(if (playingAt > 0) playingAt - 1 else 0) }

    fun stopMusic() {
        host?.stopSong()
        playingAt = -1; paused = false
    }

    private fun stopPresenting() {
        if (!presenting) return
        interrupted = true
        voice?.hush()
    }

    companion object {
        val TOOLS = listOf("web.search", "web.fetch", "workspace.write")
        private val REFRESH_ON = setOf("TASK_CREATED", "STATE_CHANGED", "OWNER_REPLY", "INPUT_REQUESTED", "SCREEN_DESIGNED", "VERIFIED")
    }
}
