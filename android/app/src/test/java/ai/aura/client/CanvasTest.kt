package ai.aura.client

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

/** The living canvas (ADR-0060): narration in step with the screen, the look of the moment, forms, and the new API shapes. */
class CanvasTest {
    private fun block(type: String, say: String = "", span: String = "") =
        Api.Block(type, "", "", "", "", emptyList(), emptyList(), emptyList(), say = say, span = span)

    @Test
    fun narrationRevealsEachBlockWhenItIsSpokenAbout() {
        val blocks = listOf(block("hero", "Ini intinya."), block("metrics"), block("chart", "Grafiknya naik."), block("links"))
        val beats = Narration.beats("Berikut hasilnya.", blocks)
        assertEquals(listOf(
            Narration.Beat("Berikut hasilnya.", 0, -1), // the screen never runs ahead of the voice
            Narration.Beat("Ini intinya.", 1, 0),
            Narration.Beat("Grafiknya naik.", 3, 2), // the silent block before it arrives with it
        ).map { it.say to it.focus }, beats.map { it.say to it.focus })
        assertEquals(listOf(0, 1, 4), beats.map { it.reveal }) // and the trailing blocks arrive with the last spoken one
    }

    @Test
    fun narrationWithoutSpokenBlocksShowsEverythingWithTheAnswer() {
        assertEquals(listOf(Narration.Beat("Selesai.", 2, -1)), Narration.beats("Selesai.", listOf(block("text"), block("list"))))
        assertEquals(emptyList<Narration.Beat>(), Narration.beats("", emptyList()))
        assertEquals(listOf(Narration.Beat("", 1, -1)), Narration.beats("", listOf(block("text"))))
    }

    @Test
    fun spokenTextHasNoMarkupAndIsBounded() {
        assertEquals("Judul tebal lihat tautan", Narration.spoken("# Judul **tebal** lihat https://contoh.id/a?b=1"))
        val long = "Kalimat pertama yang cukup panjang untuk diuji. ".repeat(40)
        val s = Narration.spoken(long)
        assertTrue(s.length <= Narration.MAX_SPOKEN)
        assertTrue(s.endsWith("."))
    }

    @Test
    fun formsFillOnlyWhenCompleteAndValuesCannotOpenFields() {
        val t = "Bandingkan {{produk}} dengan {{pesaing}} untuk {{produk}}"
        assertEquals(listOf("produk", "pesaing"), Narration.formFields(t))
        assertNull(Narration.fillForm(t, mapOf("produk" to "cat tembok")))
        assertEquals("Bandingkan cat tembok dengan merek B untuk cat tembok", Narration.fillForm(t, mapOf("produk" to "cat tembok", "pesaing" to " merek B ")))
        assertEquals("Cari x", Narration.fillForm("Cari {{q}}", mapOf("q" to "{{x}}")))
        assertEquals(listOf("a", "b"), Narration.choices("a| b |"))
        assertEquals(emptyList<String>(), Narration.choices("bebas"))
    }

    @Test
    fun lookFollowsTheDesignButAnAlertIsNeverHidden() {
        val calmDesign = Api.Scene("calm", "emerald", "grid")
        assertEquals(Api.Scene("calm", "emerald", "grid"), HomeLogic.look(calmDesign, Api.Scene("focus", "amber", "")))
        assertEquals(Api.Scene("alert", "amber", "grid"), HomeLogic.look(calmDesign, Api.Scene("alert", "amber", "")))
        assertEquals(Api.Scene("calm", "aurora", "stack"), HomeLogic.look(Api.Scene(), Api.Scene()))
        assertEquals(Api.Scene("focus", "rose", "stack"), HomeLogic.look(Api.Scene(), Api.Scene("focus", "rose", "")))
    }

    @Test
    fun homeWordsFollowTheMoment() {
        assertEquals("Selamat pagi, Tuan", HomeLogic.greeting("morning"))
        assertEquals("Halo, Tuan", HomeLogic.greeting(""))
        assertEquals("2 tindakan menunggu persetujuan Tuan.", HomeLogic.headline(Api.HomeContext(approvals = 2, headline = "needs_approval")))
        assertEquals("3 tugas selesai hari ini. Ada yang bisa saya bantu?", HomeLogic.headline(Api.HomeContext(doneToday = 3)))
        for (part in listOf("dawn", "morning", "midday", "afternoon", "evening", "night", "")) {
            val s = HomeLogic.suggestions(part)
            assertEquals(6, s.size)
            // Personal requests are never marked public (free models may log prompts).
            assertTrue(s.filter { it.public }.none { it.label in setOf("Brief pagi", "Rekap hari ini", "Karier", "Ubah tampilan", "Riset web", "Analisa", "Kerjakan kode") })
        }
        assertTrue(HomeLogic.needsMore(HomeLogic.Suggestion("", "Riset", "Riset tentang: ")))
        assertFalse(HomeLogic.needsMore(HomeLogic.Suggestion("", "Brief", "Buatkan brief.")))
    }

    @Test
    fun liveActivityAndLayoutRows() {
        assertEquals("Mencari di web…", HomeLogic.activity("TOOL_STARTED", JSONObject().put("tool_id", "web.search")))
        assertEquals("Merancang tampilan…", HomeLogic.activity("MODEL_CALLED", JSONObject().put("purpose", "design")))
        assertNull(HomeLogic.activity("SOMETHING_ELSE", JSONObject()))
        assertTrue(HomeLogic.working("RUNNING"))
        assertFalse(HomeLogic.working("WAITING_INPUT"))
        assertEquals("Apa warnanya?", HomeLogic.question("""{"question":"Apa warnanya?"}"""))
        assertEquals("teks biasa", HomeLogic.question("teks biasa"))
        val now = Instant.parse("2026-11-11T10:00:00Z")
        assertEquals("5 mnt lalu", HomeLogic.ago("2026-11-11T09:55:00Z", now))
        assertEquals("2 hari lalu", HomeLogic.ago("2026-11-09T09:00:00Z", now))
        assertEquals("", HomeLogic.ago("kemarin", now))
        val rows = HomeLogic.rows(listOf(block("a"), block("b", span = "half"), block("c", span = "half"), block("d", span = "half"), block("e")))
        assertEquals(listOf(listOf(0), listOf(1, 2), listOf(3), listOf(4)), rows)
    }

    @Test
    fun parsesHomeScreensAndLiveEvents() {
        val home = Api.parseHome(JSONObject("""{"screen":{"version":3,"screen":{"scene":{"mood":"focus","accent":"emerald","layout":"grid"},
            "blocks":[{"type":"widget","source":"greeting","limit":0},{"type":"hero","title":"Fokus","text":"Dua keputusan","icon":"🎯","emphasis":"strong"},
            {"type":"widget","source":"recent","limit":5,"span":"half"}]}},
            "context":{"daypart":"morning","approvals":1,"questions":0,"running":2,"unread":4,"failed_today":1,"done_today":7,"cost_today_usd":0.12,
            "provider_issues":["openrouter"],"scene":{"mood":"alert","accent":"amber"},"headline":"needs_approval"}}"""))
        assertEquals(3, home.version)
        assertEquals(Api.Scene("focus", "emerald", "grid"), home.screen.scene)
        assertEquals(listOf("greeting", "", "recent"), home.screen.blocks.map { it.source })
        assertEquals(5, home.screen.blocks[2].limit)
        assertEquals("half", home.screen.blocks[2].span)
        assertEquals("🎯", home.screen.blocks[1].icon)
        assertEquals("morning", home.context.dayPart)
        assertEquals(listOf(1, 2, 4, 1, 7), listOf(home.context.approvals, home.context.running, home.context.unread, home.context.failedToday, home.context.doneToday))
        assertEquals(listOf("openrouter"), home.context.providerIssues)
        assertEquals("needs_approval", home.context.headline)
        assertEquals("alert", home.context.scene.mood)

        val empty = Api.parseHome(JSONObject("{}"))
        assertEquals("idle", empty.context.headline)
        assertTrue(empty.screen.blocks.isEmpty())

        val versions = Api.parseVersions(JSONObject("""{"versions":[{"version":2,"active":true,"request":"lebih ringkas","summary":"Tiga bagian",
            "screen":{"scene":{},"blocks":[]}},{"version":1,"active":false,"screen":{"scene":{},"blocks":[]}}]}"""))
        assertEquals(listOf(2 to true, 1 to false), versions.map { it.version to it.active })
        assertEquals("lebih ringkas", versions[0].request)

        val live = Api.parseLive("""{"task_id":"t1","type":"TOOL_STARTED","data":{"tool_id":"web.fetch"}}""")!!
        assertEquals("Membaca halaman…", HomeLogic.activity(live.type, live.data))
        assertNull(Api.parseLive("bukan json"))
        assertNull(Api.parseLive("""{"type":"X"}"""))
    }

    @Test
    fun requestsCarryDesignAndSceneOnlyAsData() {
        val body = Api.taskJson("Ubah beranda jadi ringkas", listOf("web.search"), emptyList(), false, emptyList(), "home")
        assertEquals("home", body.getString("design"))
        assertFalse(body.has("data_class")) // a design request is personal unless the owner says otherwise
        assertFalse(Api.taskJson("x", emptyList(), emptyList(), true, emptyList(), null).has("design"))
        val reply = Api.parseReply(JSONObject("""{"text":"Ini","view":[{"type":"progress","say":"Sudah separuh","items":[{"label":"Proyek","number":50}]}],
            "scene":{"mood":"celebrate","accent":"violet","layout":"story"},"notes":[{"code":"screen_designed","detail":"home"}]}"""))
        assertEquals(Api.Scene("celebrate", "violet", "story"), reply.scene)
        assertEquals("Sudah separuh", reply.view[0].say)
        assertEquals(50.0, reply.view[0].items[0].number!!, 0.0)
        assertTrue(ReplyView.noteText(reply.notes[0]).isNotBlank())
    }
}
