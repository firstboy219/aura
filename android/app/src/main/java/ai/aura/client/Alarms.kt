package ai.aura.client

import android.app.AlarmManager
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * Reminders ring on the phone at their time (ADR-0063), also when the app is closed: the phone sets an alarm for
 * each reminder due in the next day and shows it itself. The background check (every 15 minutes at best) would be
 * late; it skips a reminder the phone already showed.
 */
object Alarms {
    const val ACTION = "ai.aura.client.REMINDER"
    private const val CHANNEL = "aura-reminders"
    private const val HORIZON = 36 * 3600 * 1000L

    /** The alarms to set: enabled reminders due from now to the horizon, keyed "id@time". Pure logic. */
    fun plan(list: List<Api.ReminderItem>, now: Long): Map<String, Pair<Long, String>> =
        list.filter { it.enabled && it.nextAt > now - 60_000 && it.nextAt < now + HORIZON }
            .associate { "${it.id}@${it.nextAt}" to (it.nextAt to it.text) }

    /** Replaces the phone's reminder alarms with [list]. */
    fun schedule(ctx: Context, list: List<Api.ReminderItem>, now: Long = System.currentTimeMillis()) {
        val store = SecureStore(ctx)
        val am = ctx.getSystemService(AlarmManager::class.java) ?: return
        val want = plan(list, now)
        for (key in store.alarms - want.keys) am.cancel(intent(ctx, key, ""))
        for ((key, v) in want) {
            if (key in store.firedReminders) continue
            am.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, v.first, intent(ctx, key, v.second))
        }
        store.alarms = want.keys
    }

    private fun intent(ctx: Context, key: String, text: String): PendingIntent {
        val i = Intent(ctx, ReminderReceiver::class.java).setAction(ACTION).putExtra("key", key).putExtra("text", text)
        return PendingIntent.getBroadcast(ctx, key.hashCode(), i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    fun show(ctx: Context, key: String, text: String) {
        val nm = ctx.getSystemService(NotificationManager::class.java) ?: return
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "Pengingat", NotificationManager.IMPORTANCE_HIGH))
        val open = PendingIntent.getActivity(ctx, 0, Intent(ctx, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val n = Notification.Builder(ctx, CHANNEL).setSmallIcon(R.drawable.ic_aura).setContentTitle("⏰ Pengingat")
            .setContentText(text).setStyle(Notification.BigTextStyle().bigText(text)).setContentIntent(open).setAutoCancel(true).build()
        try { nm.notify(key.hashCode(), n) } catch (_: SecurityException) { } // notifications permission not granted
        val store = SecureStore(ctx)
        store.firedReminders = store.firedReminders + key
    }
}

/** Shows a reminder when its alarm rings. The text came from the Core's reminder list; nothing else runs. */
class ReminderReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action != Alarms.ACTION) return
        val key = intent.getStringExtra("key").orEmpty()
        val text = intent.getStringExtra("text").orEmpty()
        if (key.isNotEmpty() && text.isNotEmpty()) Alarms.show(ctx, key, text)
    }
}
