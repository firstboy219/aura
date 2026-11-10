package ai.aura.client

/**
 * Sensor rules (Blueprint D.1 P7): a sensor runs only when the owner allowed it in the app, the OS permission is
 * granted, the app is in the foreground and the owner just asked for it; it is always visible while active and
 * one action stops everything. Pure logic, unit-tested.
 */
class SensorPolicy {
    enum class State { IDLE, LISTENING }

    var state = State.IDLE
        private set
    private val listeners = mutableListOf<(State) -> Unit>()

    fun onChange(l: (State) -> Unit) { listeners += l }

    /** Returns null when the microphone may start, otherwise the reason it may not. */
    fun whyNot(allowedInApp: Boolean, osPermission: Boolean, foreground: Boolean): String? = when {
        !allowedInApp -> "Mikrofon dimatikan di Pengaturan AURA"
        !osPermission -> "Izin mikrofon Android belum diberikan"
        !foreground -> "Mikrofon hanya dipakai saat aplikasi terbuka"
        state == State.LISTENING -> "Sedang mendengarkan"
        else -> null
    }

    fun started() = set(State.LISTENING)

    /** The single stop for every sensor; safe to call any time. */
    fun stopAll() = set(State.IDLE)

    private fun set(s: State) {
        if (state == s) return
        state = s
        listeners.forEach { it(s) }
    }
}
