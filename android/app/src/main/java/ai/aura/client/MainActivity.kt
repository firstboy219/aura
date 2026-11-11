package ai.aura.client

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.pm.PackageManager
import android.graphics.Color
import android.graphics.Typeface
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import java.util.concurrent.Executors

/** AURA's Android client: tasks, approvals, notifications and settings, all through the public API. */
class MainActivity : Activity() {
    private lateinit var store: SecureStore
    private val policy = SensorPolicy()
    private lateinit var voice: Voice
    private val io = Executors.newSingleThreadExecutor()
    private lateinit var banner: LinearLayout
    private lateinit var content: LinearLayout
    private var foreground = false
    private var tab = "tasks"
    private var pendingMic: (() -> Unit)? = null
    private var lastPublic = false
    private val live = LiveTalk()
    private lateinit var liveBox: LinearLayout
    private lateinit var liveStatus: TextView
    private lateinit var liveHeard: TextView
    private lateinit var liveSaid: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        store = SecureStore(this)
        voice = Voice(this, policy)
        voice.vocabulary = store.vocabulary
        val root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        // The sensor banner: always visible while the microphone is on, with one button that stops everything.
        banner = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setBackgroundColor(Color.rgb(183, 28, 28))
            setPadding(dp(12), dp(8), dp(12), dp(8))
            visibility = View.GONE
            addView(TextView(this@MainActivity).apply {
                text = "● Mikrofon dipakai AURA (dikte atau percakapan langsung)"
                setTextColor(Color.WHITE)
                layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
            })
            addView(Button(this@MainActivity).apply { text = "Hentikan"; setOnClickListener { voice.stop() } })
        }
        policy.onChange { s -> runOnUiThread {
            banner.visibility = if (s == SensorPolicy.State.LISTENING) View.VISIBLE else View.GONE
            if (s == SensorPolicy.State.IDLE) endLive() // one stop ends everything, the conversation included
        } }
        root.addView(banner)
        // The live conversation panel: what AURA hears, as it is spoken, and what it answers.
        liveStatus = label("", bold = true)
        liveHeard = label("", size = 16f)
        liveSaid = label("", size = 14f).apply { setTextColor(Color.DKGRAY) }
        liveBox = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(Color.rgb(232, 240, 254))
            setPadding(dp(12), dp(8), dp(12), dp(8))
            visibility = View.GONE
            addView(liveStatus); addView(liveHeard); addView(liveSaid)
            addView(LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.HORIZONTAL
                addView(button("Sela / bicara sekarang") { if (live.state == LiveTalk.State.SPEAKING) voice.hush() })
                addView(button("Akhiri percakapan") { voice.stop() })
            })
        }
        root.addView(liveBox)
        val tabs = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        for ((id, label) in listOf("tasks" to "Tugas", "approvals" to "Persetujuan", "notices" to "Notifikasi", "settings" to "Pengaturan")) {
            tabs.addView(Button(this).apply {
                text = label
                textSize = 12f
                layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
                setOnClickListener { tab = id; render() }
            })
        }
        root.addView(tabs)
        content = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(dp(12), dp(8), dp(12), dp(24)) }
        root.addView(ScrollView(this).apply { addView(content) }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        setContentView(root)
        if (store.token.isEmpty()) tab = "settings"
        render()
    }

    override fun onResume() { super.onResume(); foreground = true }

    override fun onPause() {
        super.onPause()
        foreground = false
        voice.stop() // the microphone never stays on when the app is not in front
    }

    override fun onDestroy() { voice.shutdown(); io.shutdown(); super.onDestroy() }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()

    private fun api() = Api(store.server, store.token)

    private fun <T> bg(work: () -> T, done: (T) -> Unit) {
        io.execute {
            val r = runCatching(work)
            runOnUiThread {
                r.onSuccess(done).onFailure { e ->
                    val msg = (e as? Api.ApiError)?.let { "${it.code}: ${it.message}" } ?: (e.message ?: e.toString())
                    toast(msg)
                }
            }
        }
    }

    private fun toast(s: String) = Toast.makeText(this, s, Toast.LENGTH_LONG).show()

    private fun label(s: String, bold: Boolean = false, size: Float = 15f) = TextView(this).apply {
        text = s
        textSize = size
        if (bold) setTypeface(typeface, Typeface.BOLD)
        setPadding(0, dp(6), 0, dp(6))
    }

    private fun button(s: String, f: () -> Unit) = Button(this).apply { text = s; setOnClickListener { f() } }

    private fun render() {
        content.removeAllViews()
        when (tab) {
            "tasks" -> renderTasks()
            "approvals" -> renderApprovals()
            "notices" -> renderNotices()
            else -> renderSettings()
        }
    }

    // ---------------------------------------------------------------- tasks
    private fun renderTasks() {
        if (store.token.isEmpty()) { content.addView(label("Masuk dulu di Pengaturan.")); return }
        content.addView(label("Tanya AURA", bold = true))
        val objective = EditText(this).apply { hint = "Tulis permintaan Tuan…"; minLines = 3; gravity = Gravity.TOP }
        content.addView(objective)
        val domains = EditText(this).apply { hint = "Domain yang boleh diakses (pisahkan dengan koma, opsional)"; isSingleLine = true }
        content.addView(domains)
        val public = CheckBox(this).apply {
            text = "Publik (boleh memakai model gratis; jangan untuk data pribadi)"
            isChecked = lastPublic
            setOnCheckedChangeListener { _, on -> lastPublic = on }
        }
        content.addView(public)
        val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        row.addView(button("🎤 Bicara") { startMic { objective.append(if (objective.text.isEmpty()) it else " $it") } })
        row.addView(button("🎧 Percakapan langsung") { startLive() })
        row.addView(button("Kirim") {
            val text = objective.text.toString().trim()
            if (text.isEmpty()) return@button
            val ds = domains.text.toString().split(",").map { it.trim() }.filter { it.isNotEmpty() }
            ask(text, ds, public.isChecked)
        })
        content.addView(row)
        content.addView(label("Tugas terakhir", bold = true))
        bg({ api().tasks() }) { ts ->
            if (ts.isEmpty()) content.addView(label("Belum ada tugas."))
            ts.forEach { t ->
                content.addView(label("${stateText(t)}  ${t.objective.take(90)}").apply {
                    setOnClickListener { showTask(t.id) }
                    if (t.state == "WAITING_INPUT" || t.state == "WAITING_APPROVAL") setTextColor(Color.rgb(230, 81, 0))
                })
                t.reply?.let { r ->
                    content.addView(label(r.text.take(220) + if (r.text.length > 220) "…" else "", size = 13f).apply {
                        setTextColor(Color.GRAY)
                        setOnClickListener { showTask(t.id) }
                    })
                }
            }
        }
    }

    /** Sends a request and stays with it until AURA has answered. */
    private fun ask(text: String, domains: List<String>, public: Boolean) {
        bg({ api().createTask(text, listOf("web.search", "web.fetch", "workspace.write"), domains, public) }) {
            toast("AURA sedang mengerjakan…")
            render()
            follow(it.id, 0)
        }
    }

    private fun follow(id: String, n: Int) {
        content.postDelayed({
            if (isFinishing || isDestroyed) return@postDelayed
            bg({ api().task(id) }) { t ->
                when {
                    t.state in ACTIVE && n < 90 -> follow(id, n + 1)
                    foreground -> { if (tab == "tasks") render(); showTask(id) }
                }
            }
        }, 2000)
    }

    private fun showTask(id: String) {
        bg({ api().task(id) to api().events(id) }) { (t, evs) ->
            val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(dp(16), dp(8), dp(16), dp(8)) }
            box.addView(label(t.objective, bold = true))
            box.addView(label("Status: ${stateText(t)} · biaya \$${"%.4f".format(t.costUsd)}", size = 13f))
            explain(t.reasonCode)?.let { box.addView(label(it, size = 13f).apply { setTextColor(Color.rgb(183, 28, 28)) }) }
            var dialog: AlertDialog? = null
            t.reply?.let { r -> box.addView(ReplyView.build(this, r) { next -> dialog?.dismiss(); ask(next, emptyList(), t.public) }) }
            val question = evs.lastOrNull { it.type == "INPUT_REQUESTED" }?.data
            var answerBox: EditText? = null
            if (t.state == "WAITING_INPUT" && question != null) {
                box.addView(label("AURA bertanya: " + (Regex("\"question\":\"(.*?)\"").find(question)?.groupValues?.get(1) ?: question), bold = true))
                answerBox = EditText(this).apply { hint = "Jawaban" }
                box.addView(answerBox)
                box.addView(button("🎤 Jawab dengan suara") { startMic { answerBox.setText(it) } })
            }
            val trail = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; visibility = if (t.reply == null) View.VISIBLE else View.GONE }
            evs.takeLast(25).forEach { trail.addView(label("${it.seq}. ${it.type}", size = 12f)) }
            if (t.reply != null) box.addView(label("▸ Rincian teknis", size = 12f).apply {
                setOnClickListener { trail.visibility = if (trail.visibility == View.VISIBLE) View.GONE else View.VISIBLE }
            })
            box.addView(trail)
            if (store.speakResults && t.state == "COMPLETED") voice.speak(t.reply?.text?.take(600) ?: "Tugas selesai: ${t.objective}")
            val d = AlertDialog.Builder(this).setView(ScrollView(this).apply { addView(box) }).setNegativeButton("Tutup", null)
            if (answerBox != null) d.setPositiveButton("Kirim jawaban") { _, _ ->
                val a = answerBox.text.toString().trim()
                if (a.isNotEmpty()) bg({ api().answer(id, a) }) { toast("Jawaban terkirim"); render() }
            }
            dialog = d.show()
        }
    }

    private fun stateText(t: Api.Task): String = when (t.state) {
        "COMPLETED" -> "✓ Selesai"
        "FAILED" -> "✗ Gagal"
        "RUNNING", "PLANNING", "CREATED" -> "… Dikerjakan"
        "WAITING_INPUT" -> "? Menunggu jawaban"
        "WAITING_APPROVAL" -> "! Menunggu persetujuan"
        "BUDGET_EXCEEDED" -> "✗ Budget habis"
        "CANCELLED" -> "Dibatalkan"
        else -> t.state
    }

    /** Plain words for the reason codes an owner meets most often. */
    private fun explain(code: String?): String? = when (code) {
        null, "" -> null
        "PROVIDER_UNAVAILABLE" -> "Model AI sedang tidak bisa dipakai: kredit OpenRouter habis atau kuota model gratis harian sudah terpakai " +
            "(reset pukul 07.00 WIB). Tambahkan kredit OpenRouter agar AURA bisa menjawab kapan saja."
        "BUDGET_EXCEEDED", "MAX_ITERATIONS", "MAX_WALL_TIME" -> "Tugas berhenti karena batas biaya, langkah, atau waktu tercapai."
        "POLICY_DENIED" -> "Tugas ditolak oleh aturan keamanan AURA (izin tool atau domain)."
        "VERIFICATION_FAILED" -> "AURA tidak bisa membuktikan hasilnya benar, jadi tugas tidak ditandai selesai."
        "SITE_BLOCKED" -> "Situs menolak akses otomatis."
        else -> "Alasan: $code"
    }

    // ---------------------------------------------------------------- approvals
    private fun renderApprovals() {
        if (store.token.isEmpty()) { content.addView(label("Masuk dulu di Pengaturan.")); return }
        bg({ api().approvals() }) { aps ->
            if (aps.isEmpty()) content.addView(label("Tidak ada yang menunggu persetujuan."))
            aps.forEach { a ->
                content.addView(label("${a.toolId} → ${a.target}", bold = true))
                content.addView(label("Tugas: ${a.objective.take(120)}\nAlasan: ${a.why}\nPerkiraan biaya: ${a.estimatedCost}\nKedaluwarsa: ${a.expiresAt}", size = 13f))
                val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
                row.addView(button("Setujui") {
                    AlertDialog.Builder(this).setTitle("Setujui tindakan ini persis?")
                        .setMessage("${a.toolId}\n${a.target}\n\nHash tindakan: ${a.actionHash.take(16)}…")
                        .setPositiveButton("Setujui") { _, _ -> bg({ api().approve(a) }) { toast("Disetujui"); render() } }
                        .setNegativeButton("Batal", null).show()
                })
                row.addView(button("Tolak") { bg({ api().reject(a) }) { toast("Ditolak"); render() } })
                content.addView(row)
            }
        }
    }

    // ---------------------------------------------------------------- notifications
    private fun renderNotices() {
        if (store.token.isEmpty()) { content.addView(label("Masuk dulu di Pengaturan.")); return }
        bg({ api().notifications(false) }) { ns ->
            if (ns.isEmpty()) content.addView(label("Belum ada notifikasi."))
            ns.forEach { n ->
                content.addView(label((if (n.read) "" else "● ") + n.title).apply {
                    setOnClickListener {
                        bg({ api().markRead(n.id) }) { render() }
                        n.taskId?.let { showTask(it) }
                    }
                })
            }
        }
    }

    // ---------------------------------------------------------------- settings
    private fun renderSettings() {
        content.addView(label("Server AURA (https)", bold = true))
        val server = EditText(this).apply { setText(store.server); isSingleLine = true }
        content.addView(server)
        val signedIn = store.token.isNotEmpty()
        content.addView(label(if (signedIn) "Status: sudah masuk" else "Masuk dengan akun aplikasi AURA", size = 13f))
        val user = EditText(this).apply { hint = "Nama pengguna"; isSingleLine = true; setText(store.username) }
        val pass = EditText(this).apply { hint = "Kata sandi"; inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD }
        if (!signedIn) { content.addView(user); content.addView(pass) }
        content.addView(button(if (signedIn) "Simpan server dan uji koneksi" else "Masuk") {
            try {
                store.server = Api.checkBase(server.text.toString())
            } catch (e: IllegalArgumentException) { toast(e.message ?: "URL tidak valid"); return@button }
            if (signedIn) { bg({ api().tasks(1) }) { toast("Terhubung ke AURA") }; return@button }
            val u = user.text.toString().trim()
            val p = pass.text.toString()
            if (u.isEmpty() || p.isEmpty()) { toast("Isi nama pengguna dan kata sandi"); return@button }
            val srv = store.server
            bg({ Api.login(srv, u, p, Build.MANUFACTURER + " " + Build.MODEL) }) { tok ->
                store.token = tok // the password is not kept; only this device's token, encrypted
                store.username = u
                toast("Berhasil masuk")
                tab = "tasks"
                render()
            }
        })
        content.addView(label("Sensor dan privasi", bold = true))
        content.addView(CheckBox(this).apply {
            text = "Izinkan mikrofon untuk dikte dan percakapan langsung (hanya setelah Tuan menekan 🎤 atau 🎧)"
            isChecked = store.micAllowed
            setOnCheckedChangeListener { _, on -> store.micAllowed = on; if (!on) voice.stop() }
        })
        content.addView(CheckBox(this).apply {
            text = "Bacakan hasil dengan suara (text-to-speech di perangkat)"
            isChecked = store.speakResults
            setOnCheckedChangeListener { _, on -> store.speakResults = on }
        })
        content.addView(CheckBox(this).apply {
            text = "Cek notifikasi dan persetujuan di latar belakang (diatur Android, minimal tiap 15 menit)"
            isChecked = store.backgroundChecks
            setOnCheckedChangeListener { _, on ->
                store.backgroundChecks = on
                if (on && Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIFY)
                NotifyJob.schedule(this@MainActivity, on)
            }
        })
        content.addView(button("Hentikan semua sensor sekarang") { voice.stop(); toast("Semua sensor dihentikan") })
        content.addView(label(
            "Suara dikenali oleh pengenal suara perangkat (offline bila tersedia) dan hanya teksnya yang dikirim ke AURA; " +
                "percakapan langsung berhenti saat aplikasi ditutup, saat hening, atau saat Tuan berkata \"berhenti\"; " +
                "persetujuan tindakan tidak pernah diterima lewat suara; " +
                "rekaman suara tidak disimpan. Kamera dan layar tidak dipakai di versi ini. Kata sandi tidak disimpan; " +
                "ponsel hanya menyimpan token perangkat terenkripsi (Android Keystore), yang dicabut saat keluar.", size = 12f))
        if (store.token.isNotEmpty()) content.addView(button("Keluar dari perangkat ini") {
            val a = api()
            io.execute { runCatching { a.logout() } } // revoke on the server; sign out locally either way
            store.token = ""
            NotifyJob.schedule(this, false)
            toast("Sudah keluar")
            render()
        })
        content.addView(label("Versi ${packageManager.getPackageInfo(packageName, 0).versionName}", size = 12f))
    }

    // ---------------------------------------------------------------- microphone
    private fun startMic(onText: (String) -> Unit) {
        val granted = checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED
        if (store.micAllowed && !granted) {
            pendingMic = { startMic(onText) }
            requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_MIC)
            return
        }
        policy.whyNot(store.micAllowed, granted, foreground)?.let { toast(it); return }
        if (!voice.available()) { toast("Pengenal suara tidak tersedia di perangkat ini"); return }
        voice.listen(onText) { toast(it) }
    }

    // ---------------------------------------------------------------- live conversation
    /** Starts a spoken conversation: AURA listens, shows the words as they are spoken, answers aloud, listens again. */
    private fun startLive() {
        val granted = checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED
        if (store.micAllowed && !granted) {
            pendingMic = { startLive() }
            requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_MIC)
            return
        }
        policy.whyNot(store.micAllowed, granted, foreground)?.let { toast(it); return }
        if (!voice.available()) { toast("Pengenal suara tidak tersedia di perangkat ini"); return }
        liveHeard.text = ""
        liveSaid.text = ""
        liveBox.visibility = View.VISIBLE
        window.addFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        act(live.start())
    }

    private fun endLive() {
        live.stop()
        liveBox.visibility = View.GONE
        window.clearFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
    }

    private fun act(d: LiveTalk.Do) {
        when (d) {
            is LiveTalk.Do.Nothing -> {}
            is LiveTalk.Do.Listen -> {
                // Every turn re-checks the sensor rules: consent, permission and foreground may have changed.
                val granted = checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED
                if (!store.micAllowed || !granted || !foreground) { voice.stop(); return }
                liveStatus.text = "● Mendengarkan… silakan bicara"
                voice.hear(
                    { liveHeard.text = "Tuan: $it…" },
                    { alts -> liveHeard.text = "Tuan: ${alts.first().text}"; act(live.heard(alts)) },
                    { act(live.silence()) },
                    { toast(it); voice.stop() })
            }
            is LiveTalk.Do.Submit -> {
                liveStatus.text = "AURA sedang memproses…"
                // Every spoken request carries what the recognizer heard, so AURA can repair misheard words.
                val spoken = d.alternatives.ifEmpty { listOf(LiveTalk.Alt(d.text, null)) }
                liveTurn({ api().createTask(d.text, listOf("web.search", "web.fetch", "workspace.write"), emptyList(), lastPublic, spoken).id })
            }
            is LiveTalk.Do.Answer -> {
                liveStatus.text = "AURA sedang memproses…"
                liveTurn({ api().answer(d.taskId, d.text); d.taskId })
            }
            is LiveTalk.Do.Speak -> {
                liveStatus.text = "AURA berbicara…"
                liveSaid.text = "AURA: " + d.text.take(600)
                voice.speak(d.text.take(600)) { act(live.spoken()) }
            }
            is LiveTalk.Do.End -> {
                liveSaid.text = "AURA: " + d.say
                voice.stop()
                voice.speak(d.say)
                toast(d.say)
                if (tab == "tasks") render()
            }
        }
    }

    private fun liveTurn(send: () -> String) {
        io.execute {
            val r = runCatching(send)
            runOnUiThread {
                r.onSuccess { liveFollow(it, 0) }.onFailure { e -> act(live.failed(e.message ?: e.toString())) }
            }
        }
    }

    /** Polls the turn's task quickly so the spoken answer follows the question closely. */
    private fun liveFollow(id: String, n: Int) {
        content.postDelayed({
            if (isFinishing || isDestroyed || live.state != LiveTalk.State.THINKING) return@postDelayed
            io.execute {
                val r = runCatching { api().task(id).let { t -> t to if (t.state == "WAITING_INPUT") api().events(id).lastOrNull { it.type == "INPUT_REQUESTED" }?.data else null } }
                runOnUiThread {
                    r.onSuccess { (t, q) ->
                        t.heardAs?.let { h -> showHeardAs(t.objective, h) }
                        if (t.state in ACTIVE && n < 150) liveFollow(id, n + 1)
                        else act(live.replied(id, t.state, t.reply?.text, q?.let { questionText(it) }))
                    }.onFailure { e -> act(live.failed(e.message ?: e.toString())) }
                }
            }
        }, 800)
    }

    /** Shows what AURA understood when it differs from what the recognizer heard, and learns the corrected words. */
    private fun showHeardAs(transcript: String, heardAs: String) {
        if (heardAs.trim().equals(transcript.trim(), ignoreCase = true)) return
        liveHeard.text = "Tuan: $transcript\n🎧 Saya tangkap: $heardAs"
        val learned = LiveTalk.learnedWords(transcript, heardAs)
        if (learned.isNotEmpty()) {
            store.vocabulary = (learned + store.vocabulary).distinctBy { it.lowercase() }.take(100)
            voice.vocabulary = store.vocabulary
        }
    }

    private fun questionText(data: String) = runCatching { org.json.JSONObject(data).optString("question") }.getOrNull()?.takeIf { it.isNotBlank() } ?: data

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQ_MIC && grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) pendingMic?.invoke()
        pendingMic = null
    }

    private companion object {
        const val REQ_MIC = 1
        const val REQ_NOTIFY = 2
        val ACTIVE = setOf("CREATED", "PLANNING", "RUNNING", "SCHEDULED", "WAITING_EXTERNAL")
    }
}
