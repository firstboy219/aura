package ai.aura.client

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.job.JobInfo
import android.app.job.JobParameters
import android.app.job.JobScheduler
import android.app.job.JobService
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.app.Notification

/**
 * Background check for new notifications and pending approvals. It uses JobScheduler, so Android decides when it
 * runs (Doze, battery saver, network); there is no always-on service and no sensor use in the background.
 */
class NotifyJob : JobService() {
    override fun onStartJob(params: JobParameters): Boolean {
        Thread {
            try { check(this) } catch (_: Exception) { }
            jobFinished(params, false)
        }.start()
        return true
    }

    override fun onStopJob(params: JobParameters) = true

    companion object {
        private const val JOB_ID = 7401
        private const val CHANNEL = "aura"

        fun schedule(ctx: Context, on: Boolean) {
            val js = ctx.getSystemService(JobScheduler::class.java)
            if (!on) { js.cancel(JOB_ID); return }
            js.schedule(JobInfo.Builder(JOB_ID, ComponentName(ctx, NotifyJob::class.java))
                .setRequiredNetworkType(JobInfo.NETWORK_TYPE_ANY)
                .setPeriodic(15 * 60 * 1000L) // the minimum Android allows
                .setPersisted(false)
                .build())
        }

        fun check(ctx: Context) {
            val s = SecureStore(ctx)
            if (s.token.isEmpty()) return
            val api = Api(s.server, s.token)
            val seen = s.seenNotices.toMutableSet()
            val fresh = mutableListOf<String>()
            runCatching { Alarms.schedule(ctx, api.reminders()) }
            val fired = s.firedReminders
            api.notifications(true).filter { it.id !in seen }.forEach {
                seen += it.id
                if (it.reminder.isEmpty() || it.reminder !in fired) fresh += it.title // a reminder the phone showed itself is not shown twice
            }
            api.approvals().filter { "ap:" + it.id !in seen }.forEach { fresh += "Perlu persetujuan: ${it.toolId} ${it.target}"; seen += "ap:" + it.id }
            s.seenNotices = seen
            if (fresh.isEmpty()) return
            val nm = ctx.getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(NotificationChannel(CHANNEL, "AURA", NotificationManager.IMPORTANCE_DEFAULT))
            val open = PendingIntent.getActivity(ctx, 0, Intent(ctx, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
            val n = Notification.Builder(ctx, CHANNEL).setSmallIcon(R.drawable.ic_aura).setContentTitle("AURA")
                .setContentText(fresh.first() + if (fresh.size > 1) " (+${fresh.size - 1})" else "")
                .setContentIntent(open).setAutoCancel(true).build()
            try { nm.notify(1, n) } catch (_: SecurityException) { } // notifications permission not granted
        }
    }
}
