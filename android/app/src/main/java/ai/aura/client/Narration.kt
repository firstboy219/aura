package ai.aura.client

/**
 * What AURA says and what the screen shows, in step (ADR-0060). A reply is spoken as a sequence of beats: first its
 * text, then each block's "say" line while that block appears. Blocks without a line appear together with the next
 * spoken one, so the screen never runs ahead of the voice and never stays behind it. Pure logic, unit-tested.
 */
object Narration {
    /** Speak [say] while the first [reveal] blocks are on screen; [focus] is the block being explained (-1: none). */
    data class Beat(val say: String, val reveal: Int, val focus: Int)

    const val MAX_SPOKEN = 700
    /** A form placeholder: {{field}} (or {field}). */
    private val FIELD = Regex("\\{+\\s*([^{}]+?)\\s*\\}+")

    fun beats(text: String, blocks: List<Api.Block>): List<Beat> {
        val out = mutableListOf<Beat>()
        val spokenBlocks = blocks.indices.filter { blocks[it].say.isNotBlank() }
        val intro = spoken(text)
        // Without any narrated block the whole view appears with the answer.
        if (spokenBlocks.isEmpty()) return listOf(Beat(intro, blocks.size, -1)).filter { it.say.isNotEmpty() || blocks.isNotEmpty() }
        if (intro.isNotEmpty()) out += Beat(intro, 0, -1)
        for (i in spokenBlocks) out += Beat(spoken(blocks[i].say), i + 1, i)
        // Blocks after the last narrated one arrive with it.
        out[out.lastIndex] = out.last().copy(reveal = blocks.size)
        return out
    }

    fun beats(r: Api.Reply): List<Beat> = beats(r.text, r.view)

    /** Text fit for a speech engine: no markup signs, bounded, cut at the end of a sentence where possible. */
    fun spoken(s: String): String {
        val clean = s.replace(Regex("[*_#`>|]+"), " ").replace(Regex("https?://\\S+"), "tautan").replace(Regex("\\s+"), " ").trim()
        if (clean.length <= MAX_SPOKEN) return clean
        val cut = clean.take(MAX_SPOKEN)
        val end = cut.lastIndexOfAny(charArrayOf('.', '!', '?'))
        return if (end > MAX_SPOKEN / 2) cut.take(end + 1) else cut
    }

    /** How long a caption stays when it is shown instead of spoken: about the time it takes to read it. */
    fun readMillis(say: String): Long = (900L + 45L * say.length).coerceAtMost(6500L)

    /** The fields a form block asks for: the {{names}} in its template, in order, without repeats. */
    fun formFields(template: String): List<String> =
        FIELD.findAll(template).map { it.groupValues[1] }.distinct().toList()

    /** The request a filled form sends; null while a field is still empty. */
    fun fillForm(template: String, values: Map<String, String>): String? {
        var missing = false
        val out = FIELD.replace(template) { m ->
            val v = values[m.groupValues[1]]?.trim().orEmpty()
            if (v.isEmpty()) missing = true
            // A value is the owner's words inside AURA's sentence; braces are dropped so it cannot open another field.
            v.replace("{", "").replace("}", "")
        }
        return if (missing || out.isBlank()) null else out
    }

    /** The choices of a form field ("a|b|c"), empty for free text. */
    fun choices(value: String): List<String> = value.split("|").map { it.trim() }.filter { it.isNotEmpty() }.let { if (it.size < 2) emptyList() else it }
}
