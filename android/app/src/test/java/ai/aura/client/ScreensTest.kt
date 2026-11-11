package ai.aura.client

import ai.aura.client.ui.AuraScreen
import app.cash.paparazzi.DeviceConfig
import app.cash.paparazzi.Paparazzi
import org.junit.Rule
import org.junit.Test

/**
 * Draws the real screens on the build machine (no device) from sample data, so a change that breaks a screen fails
 * the build and the pictures can be looked at. The pictures land in app/build/reports/paparazzi.
 */
class ScreensTest {
    @get:Rule
    val paparazzi = Paparazzi(deviceConfig = DeviceConfig.PIXEL_6, theme = "android:Theme.Material.NoActionBar")

    private class Mem : Settings {
        override var server = "https://aura.example"
        override var username = "tuan"
        override var micAllowed = true
        override var speakResults = false
        override var backgroundChecks = false
        override var vocabulary = emptyList<String>()
        override var token = ""
    }

    private fun item(label: String, value: String = "", note: String = "", number: Double? = null) = Api.Item(label, value, note, "", number)

    private fun block(
        type: String, title: String = "", text: String = "", tone: String = "", chart: String = "", columns: List<String> = emptyList(),
        rows: List<List<String>> = emptyList(), items: List<Api.Item> = emptyList(), say: String = "", icon: String = "", span: String = "",
        emphasis: String = "", source: String = "", limit: Int = 0,
    ) = Api.Block(type, title, text, tone, chart, columns, rows, items, say, icon, span, emphasis, source, limit)

    private fun widget(source: String, limit: Int = 0, span: String = "") = block("widget", source = source, limit = limit, span = span)

    private val view = listOf(
        block("hero", "Penjualan naik 18%", "Kuartal ini melampaui target di tiga wilayah.", icon = "📈", say = "Intinya, penjualan naik delapan belas persen."),
        block("metrics", "Angka kunci", items = listOf(item("Omzet", "Rp 4,2 M", "+18%"), item("Margin", "31%", "+2 poin"), item("Pesanan", "1.284"))),
        block("chart", "Omzet per bulan", chart = "bar", items = listOf(item("Agu", number = 1.1), item("Sep", number = 1.3), item("Okt", number = 1.8)),
            say = "Oktober adalah bulan terkuat."),
        block("compare", "Pilihan strategi", text = "Fokus Jawa", columns = listOf("Fokus Jawa", "Ekspansi"),
            rows = listOf(listOf("Biaya", "Rendah", "Tinggi"), listOf("Risiko", "Kecil", "Sedang"))),
        block("steps", "Langkah berikutnya", items = listOf(item("Kunci stok November"), item("Tambah dua tenaga penjual"), item("Tinjau harga"))),
        block("progress", "Target tahunan", items = listOf(item("Omzet", "72%", number = 72.0)), span = "half"),
        block("quote", "Catatan", "Pertumbuhan yang sehat itu yang bisa diulang.", span = "half"),
        block("actions", items = listOf(item("Rincikan per wilayah", "Rincikan penjualan per wilayah"), item("Buat rencana Q1", "Buat rencana Q1"))),
    )

    private fun state(setup: AppState.() -> Unit): AppState = AppState(Mem(), null, SensorPolicy(), null).apply {
        signedIn = true
        home = Api.Home(2, Api.Screen(Api.Scene("", "", "grid"), listOf(
            widget("greeting"), widget("attention"), widget("ask"), widget("suggestions", 6), widget("activity", 4),
            block("hero", "Fokus hari ini", "Dua keputusan menunggu, lalu rapat pukul 14.00.", icon = "🎯"),
            widget("usage"), widget("recent", 4), widget("schedules", 3, "half"), widget("skills", 4, "half"),
        )), Api.HomeContext("morning", approvals = 1, running = 1, unread = 2, doneToday = 7, costTodayUsd = 0.12,
            scene = Api.Scene("focus", "amber", ""), headline = "needs_approval"))
        tasks = listOf(
            Api.Task("t3", "Riset harga cat tembok premium di Indonesia", "RUNNING", null, null, 0.0, public = true, updatedAt = "2026-11-11T02:00:00Z"),
            Api.Task("t2", "Analisa penjualan kuartal ini", "COMPLETED", null, null, 0.031,
                reply = Api.Reply("Penjualan kuartal ini naik 18% dan melampaui target.", view, "evidence",
                    listOf(Api.Note("core_summary", "")), emptyList(), Api.Scene("celebrate", "emerald", "grid")), updatedAt = "2026-11-11T01:00:00Z"),
            Api.Task("t1", "Kirim rekap ke tim", "FAILED", "PROVIDER_UNAVAILABLE", null, 0.0, updatedAt = "2026-11-10T09:00:00Z"),
        )
        activity["t3"] = "Membaca halaman…"
        approvals = listOf(Api.Approval("a1", "t9", "workspace.write", "laporan/q4.md", "3f9a1c0be27d55aa90", "Menyimpan laporan akhir",
            "Tulis laporan Q4", "$0.00", "2026-11-11T12:00:00Z"))
        notices = listOf(Api.Notice("n1", "Tugas selesai: Analisa penjualan", "t2", false), Api.Notice("n2", "Brief pagi siap", null, true))
        triggers = listOf(Api.Trigger("g1", "Brief pagi", "daily", "Buat brief pagi", true, "06:30"))
        skills = listOf(Api.Skill("s1", "riset-web", "active", listOf("riset", "cari")), Api.Skill("s2", "analisa-angka", "active", listOf("analisa")))
        self = Api.Self("AURA 0.14 menyelesaikan 41 dari 46 tugas; 30 terverifikasi.", "0.14.0", 46, 41, 30, 9, 22)
        versions = listOf(Api.ScreenVersion(2, home!!.screen, "fokus ke keputusan saya", "Sapaan, perhatian, lalu ringkasan", true),
            Api.ScreenVersion(1, Api.Screen(Api.Scene(), emptyList()), "", "Bawaan", false))
        setup()
    }

    /** Draws a screen with its entrance animations finished, as the owner sees it a moment after it appears. */
    @androidx.compose.runtime.Composable
    private fun Still(s: AppState) =
        androidx.compose.runtime.CompositionLocalProvider(androidx.compose.ui.platform.LocalInspectionMode provides true) { AuraScreen(s) }

    @Test fun login() = paparazzi.snapshot { AuraScreen(AppState(Mem(), null, SensorPolicy(), null)) }

    @Test fun home() = paparazzi.snapshot { AuraScreen(state { }) }

    @Test fun conversation() = paparazzi.snapshot { AuraScreen(state { tab = "chat" }) }

    @Test fun inbox() = paparazzi.snapshot { AuraScreen(state { tab = "inbox"; questions["t7"] = "Laporan untuk periode yang mana?" }) }

    @Test fun more() = paparazzi.snapshot { AuraScreen(state { tab = "more" }) }

    @Test fun look() = paparazzi.snapshot { AuraScreen(state { tab = "more"; page = "look" }) }

    @Test fun settings() = paparazzi.snapshot { AuraScreen(state { tab = "more"; page = "settings" }) }

    @Test fun board() = paparazzi.snapshot { AuraScreen(state { page = "board" }) }

    @Test fun explaining() = paparazzi.snapshot {
        AuraScreen(state {
            presenting = true; heard = "Analisa penjualan kuartal ini"; caption = "Oktober adalah bulan terkuat."
            stage = view; stageScene = Api.Scene("celebrate", "emerald", "story"); stageReveal = 3; stageFocus = 2
        })
    }

    @Test fun listening() = paparazzi.snapshot {
        AuraScreen(state { talkOpen = true; talkState = LiveTalk.State.LISTENING; heard = "berapa omzet bulan" })
    }

    @Test fun workspaceNews() = paparazzi.snapshot {
        Still(state {
            heard = "Cek berita hari ini"; caption = "Ada tiga kabar utama pagi ini."
            stage = listOf(block("links", "Berita hari ini", items = listOf(
                Api.Item("Rupiah menguat setelah keputusan suku bunga", "", "Bank sentral menahan suku bunga acuan.", "https://www.contoh-berita.id/ekonomi/rupiah", null),
                Api.Item("Harga minyak turun dua persen", "", "", "https://kabar.example/energi/minyak", null),
                Api.Item("Bursa Asia dibuka bervariasi", "", "Investor menunggu data inflasi.", "https://pasar.example/asia", null))))
            stageReveal = 1; stageFocus = 0
        })
    }

    @Test fun workspaceFigures() = paparazzi.snapshot {
        Still(state {
            heard = "Analisa apa yang terjadi"; caption = "Tiga hal ini saling berkaitan."
            stage = listOf(block("figures", "Yang berperan", items = listOf(
                Api.Item("Donald Trump", "Mengumumkan tarif impor baru", "Indeks saham turun", "https://en.wikipedia.org/wiki/Donald_Trump", null),
                Api.Item("Nvidia", "Diterpa kabar soal pimpinannya", "Saham chip tertekan", "", null),
                Api.Item("Xi Jinping", "Mengajak beberapa negara bekerja sama", "Blok dagang baru menguat", "", null))))
            stageReveal = 1; stageFocus = 0
        })
    }

    @Test fun workspaceNotes() = paparazzi.snapshot {
        Still(state {
            heard = "Catat: beli cat dasar 20 liter"; caption = "Sudah saya catat."
            stage = listOf(block("note", items = listOf(item("Beli cat dasar 20 liter"))))
            notes = listOf(Api.NoteItem("n3", "Beli cat dasar 20 liter", false, "t5"), Api.NoteItem("n2", "Telepon pemasok hari Kamis", false, ""),
                Api.NoteItem("n1", "Kirim penawaran ke toko Jaya", true, ""))
            stageReveal = 1
        })
    }

    @Test fun workspacePlayer() = paparazzi.snapshot {
        Still(state {
            heard = "Putarkan lagu"; caption = "Saya putar dari koleksi di ponsel."
            stage = listOf(block("player", text = "lagu santai"))
            queue = listOf(Tunes.Song(1, "Bengawan Solo", "Gesang", "content://media/1", 215000), Tunes.Song(2, "Sore Tugu Pancoran", "Iwan Fals", "content://media/2", 262000),
                Tunes.Song(3, "Kala Cinta Menggoda", "Chrisye", "content://media/3", 248000))
            playingAt = 0; stageReveal = 1
        })
    }

    @Test fun workspaceReminder() = paparazzi.snapshot {
        Still(state {
            heard = "Ingatkan saya minum obat jam 3 sore dan tiap pagi kirim ringkasan berita"; caption = "Baik, Tuan. Sudah saya atur."
            stage = listOf(block("schedule", items = listOf(item("Minum obat", "2026-11-11T15:00", "remind"),
                item("Cek berita hari ini dan ringkas untuk saya", "daily 05:45", "task"))))
            stageReveal = 1
        })
    }

    @Test fun workspacePlayerNeedsPermission() = paparazzi.snapshot {
        Still(state { stage = listOf(block("player")); musicState = "permission"; stageReveal = 1 })
    }

    @Test fun taskDetail() = paparazzi.snapshot {
        AuraScreen(state { openTask = tasks[1]; openEvents = listOf(Api.Event(1, "TASK_CREATED", "{}"), Api.Event(2, "TOOL_STARTED", """{"tool_id":"web.search"}""")) })
    }
}
