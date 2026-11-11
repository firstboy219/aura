package ai.aura.client

import ai.aura.client.ui.AuraApp
import android.Manifest
import android.content.pm.PackageManager
import android.graphics.BitmapFactory
import android.graphics.Color
import android.os.Build
import android.os.Bundle
import android.view.WindowManager
import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap

/**
 * AURA's Android client (ADR-0052, ADR-0060). The activity owns only what Android requires — permissions, the
 * window, the lifecycle; everything the owner sees is drawn by Compose from [AppState], through the public API.
 */
class MainActivity : ComponentActivity(), Host {
    private val policy = SensorPolicy()
    private lateinit var store: SecureStore
    private lateinit var voice: Voice
    private lateinit var state: AppState
    private lateinit var music: Music
    private var front = false
    private var pendingMic: (() -> Unit)? = null
    private var pendingAudio: (() -> Unit)? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge(SystemBarStyle.dark(Color.TRANSPARENT), SystemBarStyle.dark(Color.TRANSPARENT))
        super.onCreate(savedInstanceState)
        store = SecureStore(this)
        voice = Voice(this, policy)
        voice.vocabulary = store.vocabulary
        music = Music(this)
        state = AppState(store, voice, policy, this)
        setContent { AuraApp(state) }
    }

    override fun onResume() {
        super.onResume()
        front = true
        state.resume()
    }

    override fun onPause() {
        super.onPause()
        front = false
        voice.stop() // the microphone never stays on when the app is not in front
        state.pause()
    }

    override fun onDestroy() {
        voice.shutdown()
        music.stop()
        super.onDestroy()
    }

    override fun micGranted() = checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED

    override fun requestMic(then: () -> Unit) {
        pendingMic = then
        requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_MIC)
    }

    override fun foreground() = front

    private fun audioPermission() = if (Build.VERSION.SDK_INT >= 33) Manifest.permission.READ_MEDIA_AUDIO else Manifest.permission.READ_EXTERNAL_STORAGE

    override fun audioGranted() = checkSelfPermission(audioPermission()) == PackageManager.PERMISSION_GRANTED

    override fun requestAudio(then: () -> Unit) {
        pendingAudio = then
        requestPermissions(arrayOf(audioPermission()), REQ_AUDIO)
    }

    override fun songs(): List<Tunes.Song> = music.library()
    override fun playSong(uri: String, done: () -> Unit, failed: (String) -> Unit) = music.play(uri, done, failed)
    override fun pauseSong() = music.pause()
    override fun resumeSong() = music.resume()
    override fun stopSong() = music.stop()

    override fun setReminders(list: List<Api.ReminderItem>) = Alarms.schedule(this, list)

    override fun openFile(name: String, bytes: ByteArray): Boolean {
        val safe = name.map { if (it.isLetterOrDigit() || it in "._-") it else '_' }.joinToString("").ifEmpty { "file" }
        val dir = java.io.File(cacheDir, "shared").apply { mkdirs() }
        dir.listFiles()?.forEach { it.delete() } // only the document being opened is shared
        val f = java.io.File(dir, safe).apply { writeBytes(bytes) }
        val uri = androidx.core.content.FileProvider.getUriForFile(this, "$packageName.files", f)
        val type = when (safe.substringAfterLast('.').lowercase()) {
            "pdf" -> "application/pdf"
            "docx" -> "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
            "xlsx" -> "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
            "pptx" -> "application/vnd.openxmlformats-officedocument.presentationml.presentation"
            "png" -> "image/png"
            "jpg", "jpeg" -> "image/jpeg"
            "csv" -> "text/csv"
            else -> "text/plain"
        }
        val view = android.content.Intent(android.content.Intent.ACTION_VIEW).setDataAndType(uri, type)
            .addFlags(android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION)
        return try { startActivity(android.content.Intent.createChooser(view, safe)); true } catch (_: android.content.ActivityNotFoundException) { false }
    }

    /** A picture the Core stored, scaled down while decoding so a large one cannot exhaust memory. */
    override fun decode(bytes: ByteArray): ImageBitmap? {
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return null
        var sample = 1
        while (maxOf(bounds.outWidth, bounds.outHeight) / sample > 640) sample *= 2
        return BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = sample })?.asImageBitmap()
    }

    override fun keepScreenOn(on: Boolean) {
        if (on) window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON) else window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
    }

    override fun requestNotify() {
        if (Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIFY)
    }

    override fun backgroundChecks(on: Boolean) = NotifyJob.schedule(this, on)

    override fun device(): String = Build.MANUFACTURER + " " + Build.MODEL

    override fun version(): String = packageManager.getPackageInfo(packageName, 0).versionName ?: ""

    @Deprecated("Deprecated in Java")
    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQ_AUDIO) {
            val then = pendingAudio
            pendingAudio = null
            if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) then?.invoke() else state.musicDenied()
            return
        }
        if (requestCode != REQ_MIC) return
        val then = pendingMic
        pendingMic = null
        if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) then?.invoke()
        else state.message = "Izin mikrofon ditolak; AURA tetap bisa dipakai dengan mengetik"
    }

    private companion object {
        const val REQ_MIC = 7
        const val REQ_NOTIFY = 8
        const val REQ_AUDIO = 9
    }
}
