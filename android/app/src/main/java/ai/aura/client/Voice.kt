package ai.aura.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import java.util.Locale

/**
 * Voice input and output on the device: Android's speech recognizer (offline preferred) returns text only, and no
 * audio is recorded or stored by AURA; speech output uses the device's text-to-speech. No external voice provider.
 */
class Voice(private val ctx: Context, private val policy: SensorPolicy) {
    private var rec: SpeechRecognizer? = null
    private var tts: TextToSpeech? = null
    private val main = Handler(Looper.getMainLooper())
    private var onSpoken: (() -> Unit)? = null

    fun available() = SpeechRecognizer.isRecognitionAvailable(ctx)

    /** One dictation: the recognized text, then the microphone is off. */
    fun listen(onText: (String) -> Unit, onError: (String) -> Unit) {
        hear({}, { text, _ -> stop(); onText(text) }, { stop(); onError("Tidak ada suara yang dikenali") }, { stop(); onError(it) })
    }

    /**
     * One turn of a live conversation: [onPartial] receives the words while they are spoken, [onFinal] the finished
     * turn with the recognizer's confidence (null if not reported), [onSilence] when nothing was said. The sensor
     * stays marked active between turns; only [stop] ends it.
     */
    fun hear(onPartial: (String) -> Unit, onFinal: (String, Float?) -> Unit, onSilence: () -> Unit, onError: (String) -> Unit) {
        release()
        val r = SpeechRecognizer.createSpeechRecognizer(ctx)
        rec = r
        r.setRecognitionListener(object : RecognitionListener {
            override fun onResults(results: Bundle?) {
                val text = results?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)?.firstOrNull().orEmpty()
                val conf = results?.getFloatArray(SpeechRecognizer.CONFIDENCE_SCORES)?.firstOrNull()?.takeIf { it >= 0f }
                release()
                if (text.isNotBlank()) onFinal(text, conf) else onSilence()
            }
            override fun onError(error: Int) {
                release()
                when (error) {
                    SpeechRecognizer.ERROR_NO_MATCH, SpeechRecognizer.ERROR_SPEECH_TIMEOUT -> onSilence()
                    else -> onError("Pengenalan suara gagal (kode $error)")
                }
            }
            override fun onReadyForSpeech(params: Bundle?) {}
            override fun onBeginningOfSpeech() {}
            override fun onRmsChanged(rmsdB: Float) {}
            override fun onBufferReceived(buffer: ByteArray?) {}
            override fun onEndOfSpeech() {}
            override fun onPartialResults(partialResults: Bundle?) {
                partialResults?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)?.firstOrNull()?.takeIf { it.isNotBlank() }?.let(onPartial)
            }
            override fun onEvent(eventType: Int, params: Bundle?) {}
        })
        val i = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)
            .putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
            .putExtra(RecognizerIntent.EXTRA_LANGUAGE, "id-ID")
            .putExtra(RecognizerIntent.EXTRA_PREFER_OFFLINE, true)
            .putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true)
        policy.started()
        r.startListening(i)
    }

    private fun release() {
        rec?.let { it.cancel(); it.destroy() }
        rec = null
    }

    /** Stops listening and speaking immediately and releases the recognizer. */
    fun stop() {
        release()
        onSpoken = null
        tts?.stop()
        policy.stopAll()
    }

    /** Speaks [text]; [done] runs on the main thread when speech ends (or cannot start). */
    fun speak(text: String, done: (() -> Unit)? = null) {
        val t = tts
        if (t == null) {
            tts = TextToSpeech(ctx) { ok ->
                if (ok == TextToSpeech.SUCCESS) { tts?.language = Locale("id", "ID"); speak(text, done) } else main.post { done?.invoke() }
            }
            return
        }
        onSpoken = done
        t.setOnUtteranceProgressListener(object : UtteranceProgressListener() {
            override fun onStart(utteranceId: String?) {}
            override fun onDone(utteranceId: String?) = finished()
            @Deprecated("Deprecated in Java") override fun onError(utteranceId: String?) = finished()
            override fun onStop(utteranceId: String?, interrupted: Boolean) {}
        })
        if (t.speak(text.take(800), TextToSpeech.QUEUE_FLUSH, null, "aura") != TextToSpeech.SUCCESS) finished()
    }

    /** Cuts speech short (the owner wants to talk); the pending [speak] callback still runs. */
    fun hush() { tts?.stop(); finished() }

    private fun finished() {
        main.post { val f = onSpoken; onSpoken = null; f?.invoke() }
    }

    fun shutdown() { stop(); tts?.shutdown(); tts = null }
}
