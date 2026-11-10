package ai.aura.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import android.speech.tts.TextToSpeech
import java.util.Locale

/**
 * Voice input and output on the device: Android's speech recognizer (offline preferred) returns text only, and no
 * audio is recorded or stored by AURA; speech output uses the device's text-to-speech. No external voice provider.
 */
class Voice(private val ctx: Context, private val policy: SensorPolicy) {
    private var rec: SpeechRecognizer? = null
    private var tts: TextToSpeech? = null

    fun available() = SpeechRecognizer.isRecognitionAvailable(ctx)

    fun listen(onText: (String) -> Unit, onError: (String) -> Unit) {
        val r = SpeechRecognizer.createSpeechRecognizer(ctx)
        rec = r
        r.setRecognitionListener(object : RecognitionListener {
            override fun onResults(results: Bundle?) {
                val text = results?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)?.firstOrNull().orEmpty()
                stop()
                if (text.isNotBlank()) onText(text) else onError("Tidak ada suara yang dikenali")
            }
            override fun onError(error: Int) { stop(); onError("Pengenalan suara gagal (kode $error)") }
            override fun onReadyForSpeech(params: Bundle?) {}
            override fun onBeginningOfSpeech() {}
            override fun onRmsChanged(rmsdB: Float) {}
            override fun onBufferReceived(buffer: ByteArray?) {}
            override fun onEndOfSpeech() {}
            override fun onPartialResults(partialResults: Bundle?) {}
            override fun onEvent(eventType: Int, params: Bundle?) {}
        })
        val i = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)
            .putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
            .putExtra(RecognizerIntent.EXTRA_LANGUAGE, "id-ID")
            .putExtra(RecognizerIntent.EXTRA_PREFER_OFFLINE, true)
        policy.started()
        r.startListening(i)
    }

    /** Stops listening immediately and releases the recognizer. */
    fun stop() {
        rec?.let { it.cancel(); it.destroy() }
        rec = null
        policy.stopAll()
    }

    fun speak(text: String) {
        val t = tts
        if (t == null) {
            tts = TextToSpeech(ctx) { ok -> if (ok == TextToSpeech.SUCCESS) { tts?.language = Locale("id", "ID"); speak(text) } }
            return
        }
        t.speak(text.take(800), TextToSpeech.QUEUE_FLUSH, null, "aura")
    }

    fun shutdown() { stop(); tts?.shutdown(); tts = null }
}
