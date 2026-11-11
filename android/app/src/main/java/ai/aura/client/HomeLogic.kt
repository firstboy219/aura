package ai.aura.client

import org.json.JSONObject
import java.time.Duration
import java.time.Instant

/**
 * The words and choices of the home screen that depend on the moment: greeting, headline, one-tap requests, what a
 * running task is doing, and which look to use. Pure logic, unit-tested; the facts come from the Core (/ui/home).
 */
object HomeLogic {
    /** A request the owner can send with one tap. [public] marks requests with no personal content (free models allowed). */
    data class Suggestion(val icon: String, val label: String, val request: String, val public: Boolean = false)

    fun greeting(dayPart: String): String = when (dayPart) {
        "dawn" -> "Selamat subuh, Tuan"
        "morning" -> "Selamat pagi, Tuan"
        "midday" -> "Selamat siang, Tuan"
        "afternoon" -> "Selamat sore, Tuan"
        "evening" -> "Selamat malam, Tuan"
        "night" -> "Masih terjaga, Tuan?"
        else -> "Halo, Tuan"
    }

    fun headline(c: Api.HomeContext): String = when (c.headline) {
        "needs_approval" -> "${c.approvals} tindakan menunggu persetujuan Tuan."
        "needs_answer" -> "${c.questions} pertanyaan saya menunggu jawaban Tuan."
        "provider_problem" -> "Penyedia model bermasalah: ${c.providerIssues.joinToString(", ").ifEmpty { "periksa kunci API" }}."
        "working" -> "Saya sedang mengerjakan ${c.running} tugas."
        "failed" -> "${c.failedToday} tugas gagal hari ini — rinciannya di Percakapan."
        "unread" -> "${c.unread} kabar baru menunggu dibaca."
        else -> if (c.doneToday > 0) "${c.doneToday} tugas selesai hari ini. Ada yang bisa saya bantu?" else "Semua tenang. Ada yang bisa saya bantu?"
    }

    /**
     * One-tap requests for this part of the day. They are plain requests the owner could have typed; nothing runs
     * until the owner taps one. Personal ones (brief, recap, career) are never marked public.
     */
    fun suggestions(dayPart: String): List<Suggestion> {
        val brief = Suggestion("🌅", "Brief pagi", "Buatkan brief pagi saya: agenda dan tugas terbuka, hal yang perlu keputusan saya, dan tiga prioritas hari ini.")
        val recap = Suggestion("🌙", "Rekap hari ini", "Rekap hari ini: apa saja yang sudah AURA kerjakan, apa yang gagal, dan apa yang perlu saya lanjutkan besok.")
        val news = Suggestion("📰", "Berita penting", "Cari berita bisnis dan teknologi paling penting hari ini di Indonesia, ringkas dengan sumbernya.", public = true)
        val research = Suggestion("🔎", "Riset web", "Riset di web tentang topik berikut dan bandingkan sumbernya: ")
        val analyse = Suggestion("📊", "Analisa", "Analisa hal berikut, tampilkan angka kunci, perbandingan, dan rekomendasi: ")
        val career = Suggestion("💼", "Karier", "Bantu saya soal karier: susun langkah konkret minggu ini untuk tujuan berikut: ")
        val story = Suggestion("📖", "Dongeng", "Ceritakan dongeng pengantar tidur yang hangat untuk anak, sekitar tiga menit, dengan pesan baik di akhirnya.", public = true)
        val code = Suggestion("🧩", "Kerjakan kode", "Kerjakan tugas kode berikut di latar belakang dan laporkan hasilnya: ")
        val explain = Suggestion("💡", "Jelaskan", "Jelaskan dengan sederhana, pakai langkah dan contoh: ", public = true)
        val design = Suggestion("🎨", "Ubah tampilan", "Ubah tampilan beranda saya: ")
        return when (dayPart) {
            "dawn", "morning" -> listOf(brief, news, research, analyse, career, design)
            "midday", "afternoon" -> listOf(research, analyse, code, news, explain, design)
            "evening" -> listOf(recap, story, career, research, explain, design)
            else -> listOf(story, recap, explain, research, analyse, design)
        }
    }

    /** A request that ends with ": " waits for the owner to finish it; the others are sent as they are. */
    fun needsMore(s: Suggestion): Boolean = s.request.endsWith(": ")

    /** What a running task is doing, in the owner's words, from one event of the stream (metadata only). */
    fun activity(type: String, data: JSONObject): String? = when (type) {
        "TASK_CREATED" -> "Menerima permintaan…"
        "VOICE_UNDERSTOOD" -> "Memahami ucapan Tuan…"
        "PLAN_CREATED" -> "Rencana siap, mulai bekerja…"
        "REPLANNED" -> "Mengubah rencana…"
        "TOOL_STARTED" -> toolText(data.optString("tool_id"))
        "TOOL_SUCCEEDED" -> "Langkah selesai, lanjut…"
        "TOOL_FAILED" -> "Satu langkah gagal, mencari jalan lain…"
        "MODEL_CALLED" -> when (data.optString("purpose")) {
            "plan" -> "Menyusun rencana…"
            "reply" -> "Menulis jawaban…"
            "verify" -> "Memeriksa hasil…"
            "design" -> "Merancang tampilan…"
            else -> "Berpikir…"
        }
        "COMPLETION_CHECK" -> "Memeriksa apakah sudah benar-benar selesai…"
        "VERIFIED" -> "Hasil terverifikasi."
        "SCREEN_DESIGNED" -> "Tampilan baru terpasang."
        "INPUT_REQUESTED" -> "Menunggu jawaban Tuan."
        "OWNER_REPLY" -> "Jawaban siap."
        else -> null
    }

    private fun toolText(tool: String): String = when {
        tool == "web.search" -> "Mencari di web…"
        tool == "web.fetch" -> "Membaca halaman…"
        tool.startsWith("browser") -> "Membuka peramban…"
        tool == "workspace.write" -> "Menulis berkas…"
        tool == "workspace.read" -> "Membaca berkas…"
        tool == "calc" -> "Menghitung…"
        tool.isEmpty() -> "Menjalankan alat…"
        else -> "Menjalankan $tool…"
    }

    fun working(state: String): Boolean = state in ACTIVE
    val ACTIVE = setOf("CREATED", "QUEUED", "PLANNING", "RUNNING", "VERIFYING", "SCHEDULED", "WAITING_EXTERNAL")

    fun stateLabel(state: String): String = when (state) {
        "COMPLETED" -> "Selesai"
        "FAILED" -> "Gagal"
        "CREATED", "QUEUED", "PLANNING", "RUNNING", "VERIFYING" -> "Dikerjakan"
        "WAITING_INPUT" -> "Menunggu jawaban"
        "WAITING_APPROVAL" -> "Menunggu persetujuan"
        "BUDGET_EXCEEDED" -> "Budget habis"
        "CANCELLED" -> "Dibatalkan"
        "PAUSED" -> "Dijeda"
        else -> state
    }

    /** Plain words for the reason codes an owner meets most often. */
    fun explain(code: String?): String? = when (code) {
        null, "" -> null
        "PROVIDER_UNAVAILABLE" -> "Model AI sedang tidak bisa dipakai: kredit atau kunci penyedia bermasalah, atau kuota model gratis harian habis (reset 07.00 WIB)."
        "PROVIDER_REFUSED" -> "Penyedia model menolak kunci API. Kunci perlu diganti di server AURA."
        "BUDGET_EXCEEDED", "MAX_ITERATIONS", "MAX_WALL_TIME" -> "Tugas berhenti karena batas biaya, langkah, atau waktu tercapai."
        "POLICY_DENIED" -> "Tugas ditolak oleh aturan keamanan AURA (izin alat atau domain)."
        "VERIFICATION_FAILED" -> "AURA tidak bisa membuktikan hasilnya benar, jadi tugas tidak ditandai selesai."
        "SITE_BLOCKED" -> "Situs menolak akses otomatis."
        else -> "Alasan: $code"
    }

    /** "baru saja", "5 mnt lalu", "3 jam lalu", "2 hari lalu" from an RFC 3339 time; empty when it cannot be read. */
    fun ago(iso: String, now: Instant = Instant.now()): String {
        val t = runCatching { Instant.parse(iso) }.getOrNull() ?: return ""
        val s = Duration.between(t, now).seconds
        return when {
            s < 60 -> "baru saja"
            s < 3600 -> "${s / 60} mnt lalu"
            s < 86400 -> "${s / 3600} jam lalu"
            else -> "${s / 86400} hari lalu"
        }
    }

    /**
     * The look to use: what a reply or designed screen asked for wins, then what the Core computed for this moment.
     * Something waiting for the owner (alert) is never overridden by a design's calmer mood.
     */
    fun look(asked: Api.Scene, moment: Api.Scene): Api.Scene = Api.Scene(
        mood = if (moment.mood == "alert") "alert" else asked.mood.ifEmpty { moment.mood.ifEmpty { "calm" } },
        accent = if (moment.mood == "alert") moment.accent.ifEmpty { "amber" } else asked.accent.ifEmpty { moment.accent.ifEmpty { "aurora" } },
        layout = asked.layout.ifEmpty { moment.layout.ifEmpty { "stack" } },
    )

    /** The question of a WAITING_INPUT task from its INPUT_REQUESTED event payload. */
    fun question(data: String): String = runCatching { JSONObject(data).optString("question") }.getOrNull()?.takeIf { it.isNotBlank() } ?: data

    /** Rows of a layout: two neighbouring "half" blocks share a row; everything else gets its own. */
    fun rows(blocks: List<Api.Block>): List<List<Int>> {
        val out = mutableListOf<List<Int>>()
        var i = 0
        while (i < blocks.size) {
            if (blocks[i].span == "half" && i + 1 < blocks.size && blocks[i + 1].span == "half") { out += listOf(i, i + 1); i += 2 }
            else { out += listOf(i); i++ }
        }
        return out
    }
}
