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
}
