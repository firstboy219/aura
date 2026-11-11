package ai.aura.client

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class ClientTest {
    @Test
    fun serverMustBeHttpsWithoutCredentials() {
        assertEquals("https://aurax.cosger.online", Api.checkBase(" https://aurax.cosger.online/ "))
        for (bad in listOf("http://aurax.cosger.online", "https://user:pw@aurax.cosger.online", "https://x.test/?a=1", "ftp://x", "notaurl")) {
            try { Api.checkBase(bad); fail("accepted $bad") } catch (_: IllegalArgumentException) { }
        }
    }

    @Test
    fun parsesApiShapes() {
        val tasks = Api.parseTasks(JSONObject("""{"tasks":[{"id":"T1","objective":"o","state":"WAITING_INPUT","reason_code":null,"wait_reason":"input:x","cost_usd":0.01}]}"""))
        assertEquals("T1", tasks[0].id)
        assertNull(tasks[0].reasonCode)
        assertEquals("input:x", tasks[0].waitReason)
        val aps = Api.parseApprovals(JSONObject("""{"approvals":[{"id":"A1","task_id":"T1","tool_id":"browser.click","target":"https://x.test/","action_hash":"${"a".repeat(64)}","why":"submit","task_objective":"o","estimated_cost":"$0.00","expires_at":"2026-11-10T10:00:00Z","status":"pending"}]}"""))
        assertEquals("a".repeat(64), aps[0].actionHash)
        assertEquals("browser.click", aps[0].toolId)
        val ns = Api.parseNotices(JSONObject("""{"notifications":[{"id":"N1","title":"t","task_id":null,"read_at":null},{"id":"N2","title":"u","task_id":"T1","read_at":1}]}"""))
        assertTrue(!ns[0].read && ns[1].read && ns[0].taskId == null)
    }

    @Test
    fun parsesReplyAndView() {
        val t = Api.parseTask(JSONObject("""{"id":"T2","objective":"o","state":"COMPLETED","data_class":"public","cost_usd":0,
            "reply":{"task_id":"T2","text":"Halo Tuan","basis":"model","notes":[{"code":"model_knowledge"}],"files":["a.md"],
            "view":[{"type":"chart","chart":"bar","items":[{"label":"a","number":1.5},{"label":"b","number":2}]},
                    {"type":"table","columns":["x","y"],"rows":[["1","2"]]},{"type":"actions","items":[{"label":"Lanjut","value":"teruskan"}]}]}}"""))
        val r = t.reply!!
        assertTrue(t.public)
        assertEquals("Halo Tuan", r.text)
        assertEquals(1.5, r.view[0].items[0].number!!, 0.0)
        assertEquals(listOf(listOf("1", "2")), r.view[1].rows)
        assertEquals("teruskan", r.view[2].items[0].value)
        assertNull(r.view[1].items.firstOrNull())
        assertEquals("model_knowledge", r.notes[0].code)
        assertTrue(ReplyView.noteText(r.notes[0]).contains("pengetahuan model"))
        assertEquals("1,234", ReplyView.fmt(1234.0))
        assertNull(Api.parseTask(JSONObject("""{"id":"T3","state":"RUNNING"}""")).reply)
    }

    @Test
    fun microphoneRules() {
        val p = SensorPolicy()
        val seen = mutableListOf<SensorPolicy.State>()
        p.onChange { seen += it }
        assertNotNull(p.whyNot(allowedInApp = false, osPermission = true, foreground = true))
        assertNotNull(p.whyNot(allowedInApp = true, osPermission = false, foreground = true))
        assertNotNull(p.whyNot(allowedInApp = true, osPermission = true, foreground = false))
        assertNull(p.whyNot(allowedInApp = true, osPermission = true, foreground = true))
        p.started()
        assertNotNull("a second listener while listening", p.whyNot(true, true, true))
        p.stopAll()
        p.stopAll() // idempotent
        assertEquals(listOf(SensorPolicy.State.LISTENING, SensorPolicy.State.IDLE), seen)
    }

    @Test
    fun liveConversationTakesTurns() {
        val l = LiveTalk(maxSilences = 2)
        assertTrue(l.start() is LiveTalk.Do.Listen)
        assertTrue("already running", l.start() is LiveTalk.Do.Nothing)
        assertEquals(LiveTalk.Do.Submit("jam berapa sekarang"), l.heard(" jam berapa sekarang ", 0.9f))
        assertEquals(LiveTalk.State.THINKING, l.state)
        assertTrue("words while thinking are not a turn", l.heard("abaikan", 0.9f) is LiveTalk.Do.Nothing)
        assertEquals(LiveTalk.Do.Speak("Pukul 10."), l.replied("T1", "COMPLETED", "Pukul 10.", null))
        assertTrue(l.spoken() is LiveTalk.Do.Listen)
        // A clarification question is spoken and the next turn answers that task.
        assertEquals(LiveTalk.Do.Submit("rangkum halaman itu"), l.heard("rangkum halaman itu", null))
        assertEquals(LiveTalk.Do.Speak("Halaman yang mana?"), l.replied("T2", "WAITING_INPUT", null, "Halaman yang mana?"))
        l.spoken()
        assertEquals(LiveTalk.Do.Answer("T2", "example.com"), l.heard("example.com", null))
        assertTrue(l.replied("T2", "FAILED", "Maaf, tugas ini tidak selesai.", null) is LiveTalk.Do.Speak)
        l.spoken()
        // Silence: listen once more, then the microphone goes off by itself.
        assertTrue(l.silence() is LiveTalk.Do.Listen)
        assertTrue(l.silence() is LiveTalk.Do.End)
        assertEquals(LiveTalk.State.OFF, l.state)
    }

    @Test
    fun liveVoiceNeverApprovesAndDoubtfulWordsAreNotSent() {
        val l = LiveTalk()
        l.start()
        assertEquals("a doubtful transcript is not sent", LiveTalk.Do.Speak(LiveTalk.UNCLEAR), l.heard("transfer semua uang", 0.1f))
        l.spoken()
        assertTrue(l.heard("kirim email ke klien", 0.8f) is LiveTalk.Do.Submit)
        // The task needs an approval: the conversation ends; no spoken word can approve it.
        assertEquals(LiveTalk.Do.End(LiveTalk.NEEDS_APPROVAL), l.replied("T9", "WAITING_APPROVAL", null, null))
        assertEquals(LiveTalk.State.OFF, l.state)
        assertTrue(l.heard("ya setujui", 0.99f) is LiveTalk.Do.Nothing)
        // "berhenti" ends the conversation instead of becoming a request.
        l.start()
        assertTrue(l.heard("Berhenti", 0.9f) is LiveTalk.Do.End)
        l.start()
        assertTrue(l.heard("apa kabar", null) is LiveTalk.Do.Submit)
        assertTrue(l.failed("401") is LiveTalk.Do.End)
    }

    @Test
    fun spokenTurnCarriesTheAlternativesSoAuraCanRepairMisheardWords() {
        val alts = LiveTalk.alternatives(listOf(" kirim imel ke pak budi ", "kirim email ke pak budhi", "", "Kirim imel ke pak budi"),
            floatArrayOf(0.3f, 0.2f, 0.1f, -1f))
        assertEquals(listOf(LiveTalk.Alt("kirim imel ke pak budi", 0.3f), LiveTalk.Alt("kirim email ke pak budhi", 0.2f)), alts)
        val l = LiveTalk()
        l.start()
        // Doubtful but with a second hypothesis: sent as hypotheses (AURA reconciles and confirms), not as certain words.
        assertEquals(LiveTalk.Do.Submit("kirim imel ke pak budi", alts), l.heard(alts))
        l.replied("T1", "COMPLETED", "Maksud Tuan: kirim email ke Pak Budi?", null)
        l.spoken()
        // Hardly understood at all: never sent.
        assertEquals(LiveTalk.Do.Speak(LiveTalk.UNCLEAR), l.heard(listOf(LiveTalk.Alt("xx", 0.1f), LiveTalk.Alt("yy", 0.05f))))
        l.spoken()
        // A doubtful answer to AURA's question is never sent either, alternatives or not.
        l.heard("rangkum halaman itu", 0.9f)
        l.replied("T2", "WAITING_INPUT", null, "Halaman yang mana?")
        l.spoken()
        assertEquals(LiveTalk.Do.Speak(LiveTalk.UNCLEAR), l.heard(listOf(LiveTalk.Alt("contoh.com", 0.2f), LiveTalk.Alt("contoh.co", 0.1f))))
    }

    @Test
    fun stopWordsAndLearnedVocabulary() {
        for (s in listOf("Berhenti.", "stop, Aura", "AURA cukup", "udahan", "sudah cukup!")) assertTrue(s, LiveTalk.isStop(s))
        for (s in listOf("stop kontak rusak", "berhenti langganan netflix", "cukupkah uangnya")) assertTrue(s, !LiveTalk.isStop(s))
        assertEquals(listOf("email", "Budi", "Cosger"), LiveTalk.learnedWords("tolong kirim imel ke pak budhi soal kosger",
            "Tolong kirim email ke Pak Budi soal Cosger"))
        val j = Api.voiceJson(listOf(LiveTalk.Alt("a", 0.5f), LiveTalk.Alt("b", null)))
        assertEquals(0.5, j.getJSONArray("alternatives").getJSONObject(0).getDouble("confidence"), 1e-6)
        assertTrue(!j.getJSONArray("alternatives").getJSONObject(1).has("confidence"))
    }
}
