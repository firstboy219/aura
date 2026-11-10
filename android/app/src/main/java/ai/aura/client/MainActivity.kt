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

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        store = SecureStore(this)
        voice = Voice(this, policy)
        val root = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        // The sensor banner: always visible while the microphone is on, with one button that stops everything.
        banner = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setBackgroundColor(Color.rgb(183, 28, 28))
            setPadding(dp(12), dp(8), dp(12), dp(8))
            visibility = View.GONE
            addView(TextView(this@MainActivity).apply {
                text = "● Mikrofon aktif — AURA mendengarkan"
                setTextColor(Color.WHITE)
                layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
            })
            addView(Button(this@MainActivity).apply { text = "Hentikan"; setOnClickListener { voice.stop() } })
        }
        policy.onChange { s -> runOnUiThread { banner.visibility = if (s == SensorPolicy.State.LISTENING) View.VISIBLE else View.GONE } }
        root.addView(banner)
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
        if (store.token.isEmpty()) { content.addView(label("Isi server dan token di Pengaturan dulu.")); return }
        content.addView(label("Tugas baru", bold = true))
        val objective = EditText(this).apply { hint = "Apa yang harus AURA kerjakan?"; minLines = 3; gravity = Gravity.TOP }
        content.addView(objective)
        val domains = EditText(this).apply { hint = "Domain yang boleh diakses (pisahkan dengan koma, opsional)"; isSingleLine = true }
        content.addView(domains)
        val public = CheckBox(this).apply { text = "Publik (boleh memakai model gratis; jangan untuk data pribadi)" }
        content.addView(public)
        val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        row.addView(button("🎤 Bicara") { startMic { objective.append(if (objective.text.isEmpty()) it else " $it") } })
        row.addView(button("Kirim") {
            val text = objective.text.toString().trim()
            if (text.isEmpty()) return@button
            val ds = domains.text.toString().split(",").map { it.trim() }.filter { it.isNotEmpty() }
            bg({ api().createTask(text, listOf("web.search", "web.fetch", "workspace.write"), ds, public.isChecked) }) {
                toast("Tugas dibuat: ${it.id}")
                render()
            }
        })
        content.addView(row)
        content.addView(label("Tugas terakhir", bold = true))
        bg({ api().tasks() }) { ts ->
            if (ts.isEmpty()) content.addView(label("Belum ada tugas."))
            ts.forEach { t ->
                content.addView(label("${t.state}  ${t.objective.take(90)}").apply {
                    setOnClickListener { showTask(t.id) }
                    if (t.state == "WAITING_INPUT" || t.state == "WAITING_APPROVAL") setTextColor(Color.rgb(230, 81, 0))
                })
            }
        }
    }

    private fun showTask(id: String) {
        bg({ api().task(id) to api().events(id) }) { (t, evs) ->
            val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(dp(16), dp(8), dp(16), dp(8)) }
            box.addView(label(t.objective, bold = true))
            box.addView(label("Status: ${t.state}${t.reasonCode?.let { " ($it)" } ?: ""} · biaya \$${"%.4f".format(t.costUsd)}", size = 13f))
            val question = evs.lastOrNull { it.type == "INPUT_REQUESTED" }?.data
            var answerBox: EditText? = null
            if (t.state == "WAITING_INPUT" && question != null) {
                box.addView(label("AURA bertanya: " + (Regex("\"question\":\"(.*?)\"").find(question)?.groupValues?.get(1) ?: question), bold = true))
                answerBox = EditText(this).apply { hint = "Jawaban" }
                box.addView(answerBox)
                box.addView(button("🎤 Jawab dengan suara") { startMic { answerBox.setText(it) } })
            }
            evs.takeLast(25).forEach { box.addView(label("${it.seq}. ${it.type}", size = 12f)) }
            if (store.speakResults && t.state == "COMPLETED") voice.speak("Tugas selesai: ${t.objective}")
            val d = AlertDialog.Builder(this).setView(ScrollView(this).apply { addView(box) }).setNegativeButton("Tutup", null)
            if (answerBox != null) d.setPositiveButton("Kirim jawaban") { _, _ ->
                val a = answerBox.text.toString().trim()
                if (a.isNotEmpty()) bg({ api().answer(id, a) }) { toast("Jawaban terkirim"); render() }
            }
            d.show()
        }
    }

    // ---------------------------------------------------------------- approvals
    private fun renderApprovals() {
        if (store.token.isEmpty()) { content.addView(label("Isi server dan token di Pengaturan dulu.")); return }
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
        if (store.token.isEmpty()) { content.addView(label("Isi server dan token di Pengaturan dulu.")); return }
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
        content.addView(label("Token anggota (dibuat dengan: aura admin member add ponsel approver)", size = 13f))
        val token = EditText(this).apply {
            hint = if (store.token.isEmpty()) "aura_m_…" else "tersimpan (isi untuk mengganti)"
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
        }
        content.addView(token)
        content.addView(button("Simpan dan uji") {
            try {
                store.server = Api.checkBase(server.text.toString())
            } catch (e: IllegalArgumentException) { toast(e.message ?: "URL tidak valid"); return@button }
            if (token.text.isNotBlank()) store.token = token.text.toString().trim()
            bg({ api().tasks(1) }) { toast("Terhubung ke AURA") }
        })
        content.addView(label("Sensor dan privasi", bold = true))
        content.addView(CheckBox(this).apply {
            text = "Izinkan mikrofon untuk dikte (hanya saat Tuan menekan 🎤)"
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
                "rekaman suara tidak disimpan. Kamera dan layar tidak dipakai di versi ini. Token disimpan terenkripsi " +
                "dengan kunci Android Keystore. Hapus token kapan saja di bawah.", size = 12f))
        content.addView(button("Hapus token dari ponsel ini") { store.token = ""; NotifyJob.schedule(this, false); toast("Token dihapus"); render() })
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

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQ_MIC && grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) pendingMic?.invoke()
        pendingMic = null
    }

    private companion object {
        const val REQ_MIC = 1
        const val REQ_NOTIFY = 2
    }
}
