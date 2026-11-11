package ai.aura.client

import ai.aura.client.ui.AuraApp
import android.Manifest
import android.content.pm.PackageManager
import android.graphics.Color
import android.os.Build
import android.os.Bundle
import android.view.WindowManager
import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge

/**
 * AURA's Android client (ADR-0052, ADR-0060). The activity owns only what Android requires — permissions, the
 * window, the lifecycle; everything the owner sees is drawn by Compose from [AppState], through the public API.
 */
class MainActivity : ComponentActivity(), Host {
    private val policy = SensorPolicy()
    private lateinit var store: SecureStore
    private lateinit var voice: Voice
    private lateinit var state: AppState
    private var front = false
    private var pendingMic: (() -> Unit)? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge(SystemBarStyle.dark(Color.TRANSPARENT), SystemBarStyle.dark(Color.TRANSPARENT))
        super.onCreate(savedInstanceState)
        store = SecureStore(this)
        voice = Voice(this, policy)
        voice.vocabulary = store.vocabulary
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
        super.onDestroy()
    }

    override fun micGranted() = checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED

    override fun requestMic(then: () -> Unit) {
        pendingMic = then
        requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_MIC)
    }

    override fun foreground() = front

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
        if (requestCode != REQ_MIC) return
        val then = pendingMic
        pendingMic = null
        if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) then?.invoke()
        else state.message = "Izin mikrofon ditolak; AURA tetap bisa dipakai dengan mengetik"
    }

    private companion object {
        const val REQ_MIC = 7
        const val REQ_NOTIFY = 8
    }
}
