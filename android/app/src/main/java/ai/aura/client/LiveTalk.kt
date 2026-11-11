package ai.aura.client

/**
 * Turn-taking of a live voice conversation (ADR-0057): listen → the owner's words appear as they are spoken → at the
 * end of the turn they are sent to AURA → the reply is spoken → listen again. Pure logic, unit-tested; the microphone
 * and the network live in [Voice] and [MainActivity].
 *
 * Voice is an input channel, never an authorization channel: a turn is only ever sent as a new request or as the
 * answer to a clarification question (which grants nothing). An action that needs approval ends the conversation
 * and is approved on the Approvals tab, by hand, against its action hash.
 *
 * Understanding (ADR-0058): a turn is sent with the recognizer's alternatives, so AURA can repair misheard words
 * and asks "Maksud Tuan …?" when two readings would lead to different actions. A doubtful turn (confidence below
 * [minConfidence]) is only sent when there are alternatives to reconcile; below [repeatBelow] it is never sent.
 */
class LiveTalk(private val maxSilences: Int = 4, private val minConfidence: Float = 0.35f, private val repeatBelow: Float = 0.15f) {
    /** One recognition hypothesis. */
    data class Alt(val text: String, val confidence: Float?)

    enum class State { OFF, LISTENING, THINKING, SPEAKING }

    sealed class Do {
        object Listen : Do()
        object Nothing : Do()
        /** A new request; [alternatives] are the recognizer's hypotheses (empty when there was only one). */
        data class Submit(val text: String, val alternatives: List<Alt> = emptyList()) : Do()
        data class Answer(val taskId: String, val text: String) : Do()
        data class Speak(val text: String) : Do()
        /** Say this, then end the conversation. */
        data class End(val say: String) : Do()
    }

    var state = State.OFF
        private set
    private var silences = 0
    private var asking: String? = null // the task whose clarification question was just spoken

    fun start(): Do {
        if (state != State.OFF) return Do.Nothing
        state = State.LISTENING; silences = 0; asking = null
        return Do.Listen
    }

    /** The recognizer's final text for one turn. confidence is null when the device does not report it. */
    fun heard(text: String, confidence: Float?, alternatives: List<Alt> = emptyList()): Do {
        if (state != State.LISTENING) return Do.Nothing
        val said = text.trim()
        if (said.isEmpty()) return silence()
        silences = 0
        val others = alternatives.filter { !it.text.trim().equals(said, ignoreCase = true) }
        val doubtful = confidence != null && confidence >= 0f && confidence < minConfidence
        // A doubtful transcript is never sent as the owner's words. It may go as hypotheses for AURA to reconcile
        // (it then confirms with the owner), but never as the answer to a question and never when hardly anything was understood.
        if (doubtful && (asking != null || others.isEmpty() || confidence!! < repeatBelow)) {
            state = State.SPEAKING
            return Do.Speak(UNCLEAR)
        }
        if (isStop(said)) return end("Baik, percakapan saya akhiri.")
        state = State.THINKING
        val task = asking
        asking = null
        return when {
            task != null -> Do.Answer(task, said)
            others.isEmpty() -> Do.Submit(said)
            else -> Do.Submit(said, listOf(Alt(said, confidence)) + others)
        }
    }

    /** The finished turn as the recognizer's hypotheses, best first. */
    fun heard(alts: List<Alt>): Do {
        val best = alts.firstOrNull() ?: return silence()
        return heard(best.text, best.confidence, alts)
    }

    /** Nothing was said in this listening window. The microphone does not stay on for an empty room. */
    fun silence(): Do {
        if (state != State.LISTENING) return Do.Nothing
        silences++
        return if (silences >= maxSilences) end("Tidak ada suara, percakapan saya akhiri.") else Do.Listen
    }

    /** The task this turn started has reached a state the owner should hear about. */
    fun replied(taskId: String, taskState: String, text: String?, question: String?): Do {
        if (state != State.THINKING) return Do.Nothing
        return when {
            taskState == "WAITING_APPROVAL" -> end(NEEDS_APPROVAL)
            taskState == "WAITING_INPUT" && !question.isNullOrBlank() -> { asking = taskId; speak(question) }
            !text.isNullOrBlank() -> speak(text)
            taskState in WORKING -> speak("Masih saya kerjakan. Hasilnya akan muncul di daftar tugas.")
            else -> speak("Tugas berakhir tanpa jawaban. Rinciannya ada di daftar tugas.")
        }
    }

    /** Sending the turn failed (no network, signed out). */
    fun failed(why: String): Do = if (state == State.THINKING) end("Maaf, tidak terkirim: $why") else Do.Nothing

    /** Speech output finished, or the owner cut it short to speak. */
    fun spoken(): Do {
        if (state != State.SPEAKING) return Do.Nothing
        state = State.LISTENING
        return Do.Listen
    }

    fun stop() { state = State.OFF; asking = null }

    private fun speak(text: String): Do { state = State.SPEAKING; return Do.Speak(text) }
    private fun end(say: String): Do { stop(); return Do.End(say) }

    companion object {
        const val UNCLEAR = "Maaf, kurang jelas. Mohon ulangi."
        const val NEEDS_APPROVAL = "Tindakan ini perlu persetujuan Tuan. Persetujuan tidak bisa diberikan lewat suara; " +
            "silakan buka Kotak masuk."
        val STOP_WORDS = listOf("berhenti", "stop", "selesai", "cukup", "sudah cukup", "udah cukup", "udahan", "berhenti dulu",
            "cukup dulu", "stop dulu", "akhiri percakapan")

        /** "Berhenti.", "stop aura", "AURA, cukup" — a stop word alone, optionally with AURA's name. */
        fun isStop(said: String): Boolean {
            val words = said.lowercase().split(Regex("[^\\p{L}\\p{N}]+")).filter { it.isNotEmpty() && it != "aura" }
            return words.joinToString(" ") in STOP_WORDS
        }

        /** The recognizer's results as hypotheses, best first: blanks and repeats dropped, at most five. */
        fun alternatives(texts: List<String>, confidences: FloatArray?): List<Alt> {
            val seen = HashSet<String>()
            return texts.mapIndexedNotNull { i, t ->
                val s = t.trim()
                if (s.isEmpty() || !seen.add(s.lowercase())) null
                else Alt(s, confidences?.getOrNull(i)?.takeIf { it in 0f..1f })
            }.take(5)
        }

        /**
         * Words AURA's understanding added to what the recognizer heard (a corrected name, place or term): the
         * recognizer is biased towards them next time. Short and common words are ignored.
         */
        fun learnedWords(transcript: String, heardAs: String): List<String> {
            fun words(s: String) = s.split(Regex("[^\\p{L}\\p{N}]+")).filter { it.isNotEmpty() }
            val said = words(transcript).map { it.lowercase() }.toSet()
            return words(heardAs).filter { it.length >= 4 && it.lowercase() !in said && it.lowercase() !in COMMON }
                .distinctBy { it.lowercase() }.take(5)
        }

        private val COMMON = setOf("tolong", "saya", "untuk", "dengan", "yang", "akan", "sekarang", "berapa", "maksud", "tuan",
            "bisa", "tidak", "sudah", "belum", "apakah", "jam", "hari", "ini", "itu", "dari", "kepada", "please")
        val WORKING = setOf("CREATED", "PLANNING", "RUNNING", "SCHEDULED", "WAITING_EXTERNAL")
    }
}
