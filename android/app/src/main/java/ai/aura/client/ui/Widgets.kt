package ai.aura.client.ui

import ai.aura.client.Api
import ai.aura.client.AppState
import ai.aura.client.HomeLogic
import ai.aura.client.ReplyView
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import java.time.LocalDate
import java.time.format.DateTimeFormatter
import java.util.Locale

/**
 * The live parts of a screen AURA designed (ADR-0060). A widget names a source from a closed list; the app loads
 * that data itself through the public API and draws it with its own components. An unknown source draws nothing.
 */
@Composable
fun Widget(s: AppState, b: Api.Block) {
    val n = if (b.limit > 0) b.limit else 4
    LaunchedEffect(b.source) { s.need(b.source) }
    when (b.source) {
        "greeting" -> Greeting(s)
        "ask" -> AskBox(s)
        "attention" -> Attention(s)
        "activity" -> Working(s, n)
        "recent" -> Section(b, "Terakhir dikerjakan", "🗂", s.tasks.isEmpty(), "Belum ada tugas.") {
            for (t in s.tasks.take(n)) Entry(stateDot(t.state), t.objective, HomeLogic.stateLabel(t.state) + sep(HomeLogic.ago(t.updatedAt))) { s.open(t.id) }
        }
        "approvals" -> Section(b, "Menunggu persetujuan", "🛡", s.approvals.isEmpty(), "Tidak ada yang menunggu persetujuan.") {
            for (a in s.approvals.take(n)) Entry(Ink.Warning, a.objective.ifEmpty { a.toolId }, a.toolId + sep(a.target)) { s.go("inbox") }
        }
        "notifications" -> s.notices.filter { !it.read }.let { unread ->
            Section(b, "Kabar baru", "🔔", unread.isEmpty(), "Tidak ada kabar baru.") {
                for (x in unread.take(n)) Entry(Ink.Info, x.title, "") { s.read(x) }
            }
        }
        "suggestions" -> Suggestions(s, b, n)
        "skills" -> Section(b, "Keahlian", "🧠", s.skills.isEmpty(), "Belum ada keahlian.") {
            for (k in s.skills.take(n)) Entry(if (k.status == "active") Ink.Success else Ink.Faint, k.name, k.triggers.take(3).joinToString(", ")) { s.openPage("skills") }
        }
        "memories" -> Section(b, "Yang saya ingat", "💾", s.memories.isEmpty(), "Belum ada ingatan.") {
            for (m in s.memories.take(n)) Entry(LocalLook.current.palette.c, m.content, m.kind) { s.openPage("memory") }
        }
        "schedules" -> Section(b, "Jadwal", "⏰", s.triggers.isEmpty(), "Belum ada jadwal.") {
            for (t in s.triggers.take(n)) Entry(if (t.enabled) Ink.Success else Ink.Faint, t.name.ifEmpty { t.objective }, t.dailyAt.ifEmpty { t.kind }) { s.openPage("schedules") }
        }
        "limitations" -> Section(b, "Yang belum saya bisa", "🧭", s.limitations.isEmpty(), "Tidak ada keterbatasan tercatat.") {
            for (l in s.limitations.take(n)) Entry(if (l.status == "resolved") Ink.Success else Ink.Warning, l.title, l.category) { s.openPage("limits") }
        }
        "self" -> SelfCard(s, b)
        "usage" -> Usage(s, b)
    }
}

private fun sep(s: String) = if (s.isEmpty()) "" else " · $s"

fun stateDot(state: String): Color = when {
    state == "COMPLETED" -> Ink.Success
    state == "FAILED" || state == "BUDGET_EXCEEDED" -> Ink.Danger
    state.startsWith("WAITING") -> Ink.Warning
    HomeLogic.working(state) -> Ink.Info
    else -> Ink.Faint
}

@Composable
private fun Section(b: Api.Block, title: String, icon: String, empty: Boolean, none: String, content: @Composable () -> Unit) {
    GlassCard(Modifier.fillMaxWidth(), emphasis = b.emphasis) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(b.icon.ifEmpty { icon }, fontSize = 16.sp, modifier = Modifier.padding(end = 8.dp))
            Text(b.title.ifEmpty { title }, style = MaterialTheme.typography.titleSmall, color = Ink.Muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Spacer(Modifier.height(8.dp))
        if (empty) Text(none, style = MaterialTheme.typography.bodySmall, color = Ink.Faint) else content()
    }
}

/** One line of a list: a coloured dot, what it is, and a quiet second line. */
@Composable
fun Entry(dot: Color, title: String, sub: String, onClick: (() -> Unit)? = null) {
    Row(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)
            .padding(vertical = 7.dp, horizontal = 2.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(8.dp).clip(CircleShape).background(dot))
        Spacer(Modifier.width(10.dp))
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.bodyMedium, color = Ink.Text, maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (sub.isNotEmpty()) Text(sub, style = MaterialTheme.typography.bodySmall, color = Ink.Faint, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

@Composable
private fun Greeting(s: AppState) {
    val c = s.home?.context ?: Api.HomeContext()
    val day = LocalDate.now().format(DateTimeFormatter.ofPattern("EEEE, d MMMM", Locale.forLanguageTag("id-ID")))
    Column(Modifier.fillMaxWidth().padding(top = 6.dp, bottom = 4.dp, start = 4.dp, end = 4.dp)) {
        Text(day.uppercase(), style = MaterialTheme.typography.labelSmall, color = LocalLook.current.palette.a)
        Spacer(Modifier.height(6.dp))
        Text(HomeLogic.greeting(c.dayPart), style = MaterialTheme.typography.displaySmall, color = Ink.Text)
        Spacer(Modifier.height(6.dp))
        Text(HomeLogic.headline(c), style = MaterialTheme.typography.bodyLarge, color = Ink.Muted)
    }
}

/** The quickest way to ask: type here, or tap the orb and speak. */
@Composable
private fun AskBox(s: AppState) {
    Composer(s, Modifier.fillMaxWidth())
}

/** What needs the owner now. Draws nothing when nothing does. */
@Composable
private fun Attention(s: AppState) {
    val c = s.home?.context ?: return
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        if (s.approvals.isNotEmpty() || c.approvals > 0) Alert("🛡", "${maxOf(s.approvals.size, c.approvals)} tindakan menunggu persetujuan Tuan",
            s.approvals.firstOrNull()?.let { it.objective.ifEmpty { it.toolId } } ?: "", Ink.Warning) { s.go("inbox") }
        for (q in s.questions.values) Alert("❓", "Saya perlu jawaban Tuan", q, Ink.Info) { s.go("inbox") }
        if (c.providerIssues.isNotEmpty()) Alert("⚠️", "Penyedia model bermasalah", c.providerIssues.joinToString(", "), Ink.Danger) { s.openPage("self") }
        if (c.failedToday > 0) Alert("✖", "${c.failedToday} tugas gagal hari ini", "Lihat rinciannya di Percakapan", Ink.Danger) { s.go("chat") }
        if (c.unread > 0) Alert("🔔", "${c.unread} kabar baru", "", Ink.Info) { s.go("inbox") }
    }
}

@Composable
private fun Alert(icon: String, title: String, sub: String, tint: Color, onClick: () -> Unit) {
    GlassCard(Modifier.fillMaxWidth(), tint = tint, onClick = onClick, pad = 14.dp) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(icon, fontSize = 20.sp, modifier = Modifier.padding(end = 12.dp))
            Column(Modifier.weight(1f)) {
                Text(title, style = MaterialTheme.typography.titleSmall, color = Ink.Text)
                if (sub.isNotEmpty()) Text(sub, style = MaterialTheme.typography.bodySmall, color = Ink.Muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            Text("›", fontSize = 22.sp, color = tint)
        }
    }
}

/** What AURA is doing right now, step by step, from the event stream. Draws nothing when idle. */
@Composable
private fun Working(s: AppState, n: Int) {
    val running = s.tasks.filter { HomeLogic.working(it.state) }.take(n)
    if (running.isEmpty()) return
    GlassCard(Modifier.fillMaxWidth(), emphasis = "strong") {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Orb(OrbState.THINKING, size = 30.dp)
            Spacer(Modifier.width(10.dp))
            Text("Sedang dikerjakan", style = MaterialTheme.typography.titleSmall, color = Ink.Text)
        }
        for (t in running) {
            Spacer(Modifier.height(10.dp))
            Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).clickable { s.open(t.id) }.padding(horizontal = 3.dp)) {
                Text(t.objective, style = MaterialTheme.typography.bodyMedium, color = Ink.Text, maxLines = 2, overflow = TextOverflow.Ellipsis)
                Text(s.activity[t.id] ?: "Bekerja…", style = MaterialTheme.typography.bodySmall, color = LocalLook.current.palette.c)
                Spacer(Modifier.height(6.dp))
                LinearProgressIndicator(Modifier.fillMaxWidth().height(3.dp).clip(RoundedCornerShape(2.dp)),
                    color = LocalLook.current.palette.a, trackColor = Ink.Glass)
            }
        }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun Suggestions(s: AppState, b: Api.Block, n: Int) {
    val all = HomeLogic.suggestions(s.home?.context?.dayPart ?: "")
    Column(Modifier.fillMaxWidth()) {
        if (b.title.isNotEmpty()) Text(b.title, style = MaterialTheme.typography.titleSmall, color = Ink.Muted, modifier = Modifier.padding(start = 4.dp, bottom = 8.dp))
        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            for (x in all.take(n)) Pill(x.label, x.icon) { s.suggest(x) }
        }
    }
}

@Composable
private fun SelfCard(s: AppState, b: Api.Block) {
    val me = s.self
    Section(b, "Tentang AURA", "✨", me == null, "Memuat…") {
        if (me != null) {
            Text(me.summary, style = MaterialTheme.typography.bodyMedium, color = Ink.Text, maxLines = 6, overflow = TextOverflow.Ellipsis)
            Spacer(Modifier.height(10.dp))
            Stats(listOf("Selesai" to "${me.succeeded}/${me.finished}", "Terverifikasi" to "${me.verified}", "Keahlian" to "${me.skills}", "Ingatan" to "${me.memories}"))
        }
    }
}

@Composable
private fun Usage(s: AppState, b: Api.Block) {
    val c = s.home?.context ?: Api.HomeContext()
    Section(b, "Hari ini", "📈", false, "") {
        Stats(listOf("Selesai" to "${c.doneToday}", "Berjalan" to "${c.running}", "Gagal" to "${c.failedToday}", "Biaya" to "$" + ReplyView.fmt(c.costTodayUsd)))
    }
}

/** A row of small numbers with their labels. */
@Composable
fun Stats(items: List<Pair<String, String>>) {
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        for ((label, value) in items) Column(Modifier.weight(1f)) {
            Text(value, style = MaterialTheme.typography.titleLarge, color = Ink.Text, maxLines = 1)
            Text(label, style = MaterialTheme.typography.labelSmall, color = Ink.Faint, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}
