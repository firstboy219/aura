package ai.aura.client

/**
 * Turn-taking of a live voice conversation (ADR-0057): listen → the owner's words appear as they are spoken → at the
 * end of the turn they are sent to AURA → the reply is spoken → listen again. Pure logic, unit-tested; the microphone
 * and the network live in [Voice] and [MainActivity].
 *
 * Voice is an input channel, never an authorization channel: a turn is only ever sent as a new request or as the
 * answer to a clarification question (which grants nothing). An action that needs approval ends the conversation
 * and is approved on the Approvals tab, by hand, against its action hash.
 */
class LiveTalk(private val maxSilences: Int = 4, private val minConfidence: Float = 0.35f) {
    enum class State { OFF, LISTENING, THINKING, SPEAKING }

    sealed class Do {
        object Listen : Do()
        object Nothing : Do()
        data class Submit(val text: String) : Do()
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
    fun heard(text: String, confidence: Float?): Do {
        if (state != State.LISTENING) return Do.Nothing
        val said = text.trim()
        if (said.isEmpty()) return silence()
        silences = 0
        if (confidence != null && confidence >= 0f && confidence < minConfidence) {
            state = State.SPEAKING
            return Do.Speak(UNCLEAR) // a doubtful transcript is never sent as the owner's words
        }
        if (STOP_WORDS.any { said.equals(it, ignoreCase = true) }) return end("Baik, percakapan saya akhiri.")
        state = State.THINKING
        val task = asking
        asking = null
        return if (task != null) Do.Answer(task, said) else Do.Submit(said)
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
            "silakan buka tab Persetujuan."
        val STOP_WORDS = listOf("berhenti", "stop", "selesai", "cukup", "sudah cukup")
        val WORKING = setOf("CREATED", "PLANNING", "RUNNING", "SCHEDULED", "WAITING_EXTERNAL")
    }
}
