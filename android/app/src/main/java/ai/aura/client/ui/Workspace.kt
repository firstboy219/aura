package ai.aura.client.ui

import ai.aura.client.Api
import ai.aura.client.AppState
import ai.aura.client.HomeLogic
import ai.aura.client.LiveTalk
import ai.aura.client.Tunes
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.scaleIn
import androidx.compose.animation.shrinkVertically
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalInspectionMode
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay

/**
 * The workspace (ADR-0061): home is an empty canvas and a small panel to talk to AURA. What AURA is asked for appears
 * on the canvas as cards — headlines, the people and things an analysis is about, the notes, the music player — each
 * arriving when AURA starts talking about it. Everything drawn here is data the Core sanitised, never code.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun WorkspaceScreen(s: AppState) {
    var menu by remember { mutableStateOf(false) }
    val thinking = s.stageTask != null || s.talkState == LiveTalk.State.THINKING
    val orb = when {
        s.presenting || s.talkState == LiveTalk.State.SPEAKING -> OrbState.SPEAKING
        s.talkState == LiveTalk.State.LISTENING -> OrbState.LISTENING
        thinking -> OrbState.THINKING
        else -> OrbState.IDLE
    }
    val status = when (orb) {
        OrbState.SPEAKING -> "Menjelaskan…"
        OrbState.LISTENING -> "Mendengarkan…"
        OrbState.THINKING -> s.activity[s.stageTask] ?: s.activity.values.lastOrNull() ?: "Berpikir…"
        OrbState.IDLE -> ""
    }
    val blank = s.stage.isEmpty() && s.caption.isEmpty() && s.heard.isEmpty() && !thinking
    val scroll = rememberScrollState()
    LaunchedEffect(s.stageReveal, s.stage.size) {
        if (s.stageReveal > 1) { delay(460); scroll.animateScrollTo(scroll.maxValue, tween(500)) }
    }
    Column(Modifier.fillMaxSize()) {
        Box(Modifier.weight(1f).fillMaxWidth()) {
            if (blank) {
                Column(Modifier.align(Alignment.Center).padding(32.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                    Text("RUANG KERJA", style = MaterialTheme.typography.labelMedium, color = Ink.Faint.copy(alpha = 0.7f))
                    Spacer(Modifier.height(6.dp))
                    Text("Bicara atau ketik. Yang AURA kerjakan muncul di sini.", style = MaterialTheme.typography.bodySmall,
                        color = Ink.Faint.copy(alpha = 0.55f), textAlign = TextAlign.Center)
                }
            } else {
                Column(Modifier.fillMaxSize().verticalScroll(scroll).padding(horizontal = 16.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                    Spacer(Modifier.height(44.dp))
                    if (s.heard.isNotEmpty()) Text("“${s.heard}”", style = MaterialTheme.typography.titleMedium, color = Ink.Muted,
                        textAlign = TextAlign.Center, maxLines = 4, overflow = TextOverflow.Ellipsis)
                    if (s.heardAs.isNotEmpty()) Text("Saya pahami: ${s.heardAs}", style = MaterialTheme.typography.bodySmall, color = Ink.Faint, textAlign = TextAlign.Center)
                    if (s.caption.isNotEmpty()) {
                        Spacer(Modifier.height(14.dp))
                        AnimatedContent(s.caption, transitionSpec = { fadeIn(tween(260)) togetherWith fadeOut(tween(160)) }, label = "caption") { said ->
                            Text(said, Modifier.fillMaxWidth(), style = if (s.stage.isEmpty()) MaterialTheme.typography.headlineSmall else MaterialTheme.typography.titleLarge,
                                color = Ink.Text, textAlign = TextAlign.Center)
                        }
                    }
                    Spacer(Modifier.height(18.dp))
                    Blocks(s.stage, layout = s.stageScene.layout.ifEmpty { "story" }, reveal = s.stageReveal, focus = s.stageFocus,
                        onAction = { s.propose(it) }, s = s, live = true)
                    Spacer(Modifier.height(24.dp))
                }
                Text("✕", Modifier.align(Alignment.TopEnd).padding(8.dp).clip(CircleShape).background(Ink.Raised.copy(alpha = 0.7f))
                    .clickable { s.stopAll(); s.clearStage() }.padding(horizontal = 12.dp, vertical = 6.dp), color = Ink.Muted, fontSize = 16.sp)
            }
        }
        NowPlaying(s)
        AnimatedVisibility(menu, enter = fadeIn() + expandVertically(), exit = fadeOut() + shrinkVertically()) {
            val waiting = s.approvals.size + s.questions.size + s.notices.count { !it.read }
            FlowRow(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 6.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.CenterHorizontally), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Pill("Percakapan", "💬") { menu = false; s.go("chat") }
                Pill(if (waiting > 0) "Kotak masuk · $waiting" else "Kotak masuk", "📥", filled = waiting > 0) { menu = false; s.go("inbox") }
                Pill("Catatan", "📝") { menu = false; s.openTool("note") }
                Pill("Musik", "🎵") { menu = false; s.openTool("player") }
                Pill("Papan", "🧩") { menu = false; s.openPage("board") }
                Pill("Lainnya", "☰") { menu = false; s.go("more") }
            }
        }
        AnimatedVisibility(s.typing, enter = fadeIn() + expandVertically(), exit = fadeOut() + shrinkVertically()) {
            Composer(s, Modifier.padding(horizontal = 12.dp, vertical = 4.dp))
        }
        if (status.isNotEmpty()) Text(status, Modifier.fillMaxWidth().padding(top = 4.dp), style = MaterialTheme.typography.labelLarge,
            color = LocalLook.current.palette.c, textAlign = TextAlign.Center, maxLines = 1, overflow = TextOverflow.Ellipsis)
        // The panel to talk to AURA. The camera is shown as not yet available: AURA has no way to look at a photo
        // without sending it to a model that may keep it, so the button says so instead of pretending.
        Row(Modifier.fillMaxWidth().padding(horizontal = 10.dp, vertical = 8.dp), horizontalArrangement = Arrangement.SpaceEvenly,
            verticalAlignment = Alignment.CenterVertically) {
            val waiting = s.approvals.size + s.questions.size + s.notices.count { !it.read }
            PanelButton("☰", "Menu", on = menu, badge = waiting) { menu = !menu }
            PanelButton("⌨", "Ketik", on = s.typing) { s.typing = !s.typing }
            Orb(orb, Modifier.clip(CircleShape).clickable { if (orb == OrbState.IDLE) s.startTalk() else if (orb != OrbState.THINKING) s.interrupt() }, size = 76.dp)
            PanelButton("📷", "Kamera", dim = true) { s.message = "Kamera belum tersedia: AURA belum punya cara melihat foto tanpa mengirimnya ke model yang bisa menyimpannya." }
            PanelButton("⏹", "Hentikan", dim = !s.active) { s.stopAll() }
        }
    }
}

@Composable
private fun PanelButton(icon: String, label: String, on: Boolean = false, dim: Boolean = false, badge: Int = 0, onClick: () -> Unit) {
    val look = LocalLook.current
    Column(horizontalAlignment = Alignment.CenterHorizontally, modifier = Modifier.alpha(if (dim) 0.38f else 1f)) {
        Box {
            Box(Modifier.size(48.dp).clip(CircleShape).background(if (on) look.palette.a.copy(alpha = 0.30f) else Ink.Glass)
                .border(1.dp, if (on) look.palette.a else Ink.Stroke, CircleShape).clickable(onClick = onClick), contentAlignment = Alignment.Center) {
                Text(icon, fontSize = 19.sp, color = Ink.Text)
            }
            if (badge > 0) Text(if (badge > 9) "9+" else "$badge", Modifier.align(Alignment.TopEnd).offset(x = 4.dp, y = (-2).dp).clip(CircleShape)
                .background(Ink.Danger).padding(horizontal = 5.dp, vertical = 1.dp), fontSize = 10.sp, color = Color.White)
        }
        Text(label, style = MaterialTheme.typography.labelSmall, color = Ink.Faint, modifier = Modifier.padding(top = 3.dp))
    }
}

/** What is playing, wherever the workspace has moved on to. */
@Composable
private fun NowPlaying(s: AppState) {
    val song = s.queue.getOrNull(s.playingAt)
    AnimatedVisibility(song != null && s.stage.none { it.type == "player" }, enter = fadeIn() + expandVertically(), exit = fadeOut() + shrinkVertically()) {
        Row(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 4.dp).clip(RoundedCornerShape(18.dp)).background(Ink.Raised.copy(alpha = 0.85f))
            .clickable { s.openTool("player") }.padding(horizontal = 14.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Text("🎵", fontSize = 16.sp)
            Text(song?.title.orEmpty(), Modifier.weight(1f).padding(horizontal = 10.dp), style = MaterialTheme.typography.bodyMedium, color = Ink.Text,
                maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(if (s.paused) "▶" else "⏸", Modifier.clip(CircleShape).clickable { s.togglePlay() }.padding(8.dp), fontSize = 16.sp, color = Ink.Text)
        }
    }
}

/** Items of a card arrive one after another on the workspace; elsewhere, and in a preview, they are simply there. */
@Composable
private fun Arriving(index: Int, live: Boolean, content: @Composable () -> Unit) {
    if (!live || LocalInspectionMode.current) { content(); return }
    var shown by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) { delay(180L + 320L * index); shown = true }
    AnimatedVisibility(shown, enter = fadeIn(tween(380)) + slideInVertically(tween(380)) { it / 3 } + expandVertically(tween(320)) + scaleIn(tween(380), initialScale = 0.94f)) {
        content()
    }
}

private fun hostOf(url: String): String = runCatching { java.net.URI(url).host.orEmpty().removePrefix("www.") }.getOrDefault("")

/** News and sources: one headline card per page, with the site it comes from. The Core only lets through pages it read. */
@Composable
fun Headlines(b: Api.Block, live: Boolean) {
    val look = LocalLook.current
    val open = LocalUriHandler.current
    Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
        b.items.forEachIndexed { i, it ->
            val ok = it.url.startsWith("https://") || it.url.startsWith("http://")
            val site = hostOf(it.url)
            Arriving(i, live) {
                Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(16.dp)).background(Ink.Glass).border(1.dp, Ink.Stroke, RoundedCornerShape(16.dp))
                    .then(if (ok) Modifier.clickable { runCatching { open.openUri(it.url) } } else Modifier).padding(12.dp),
                    verticalAlignment = Alignment.CenterVertically) {
                    Box(Modifier.size(40.dp).clip(RoundedCornerShape(12.dp)).background(Brush.linearGradient(listOf(look.palette.a.copy(alpha = 0.55f), look.palette.b.copy(alpha = 0.45f)))),
                        contentAlignment = Alignment.Center) {
                        Text(site.take(1).uppercase().ifEmpty { "↗" }, style = MaterialTheme.typography.titleMedium, color = Ink.Text, fontWeight = FontWeight.Bold)
                    }
                    Column(Modifier.weight(1f).padding(start = 12.dp)) {
                        Text(it.label.ifEmpty { it.url }, style = MaterialTheme.typography.titleSmall, color = Ink.Text, maxLines = 3, overflow = TextOverflow.Ellipsis)
                        if (it.note.isNotEmpty()) Text(it.note, style = MaterialTheme.typography.bodySmall, color = Ink.Muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        Text(site.ifEmpty { it.url }, style = MaterialTheme.typography.labelSmall, color = look.palette.c, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                    if (ok) Text("↗", color = look.palette.a, fontSize = 18.sp, modifier = Modifier.padding(start = 8.dp))
                }
            }
        }
    }
}

private fun initials(name: String): String = name.split(Regex("\\s+")).filter { it.isNotEmpty() }.take(2).joinToString("") { it.take(1).uppercase() }

/**
 * The people, companies and things an analysis is about: a picture, what happened, and what it led to. The picture
 * is one the Core found and stored (never an address a model wrote); without one the card shows initials.
 */
@Composable
fun Figures(b: Api.Block, s: AppState?, live: Boolean) {
    val look = LocalLook.current
    val open = LocalUriHandler.current
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        b.items.forEachIndexed { i, it ->
            if (s != null && it.image.isNotEmpty()) LaunchedEffect(it.image) { s.needPicture(it.image) }
            val pic = s?.pictures?.get(it.image)
            val source = it.url.startsWith("https://")
            Arriving(i, live) {
                Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(18.dp)).background(Ink.Glass).border(1.dp, Ink.Stroke, RoundedCornerShape(18.dp)).padding(10.dp)) {
                    Box(Modifier.size(76.dp).clip(RoundedCornerShape(16.dp))
                        .background(Brush.linearGradient(listOf(look.palette.a.copy(alpha = 0.6f), look.palette.b.copy(alpha = 0.5f))))
                        .then(if (source) Modifier.clickable { runCatching { open.openUri(it.url) } } else Modifier), contentAlignment = Alignment.Center) {
                        if (pic != null) Image(pic, it.label, Modifier.fillMaxSize(), contentScale = ContentScale.Crop, alignment = Alignment.TopCenter)
                        else Text(initials(it.label), style = MaterialTheme.typography.headlineSmall, color = Ink.Text, fontWeight = FontWeight.Bold)
                    }
                    Column(Modifier.weight(1f).padding(start = 12.dp)) {
                        Text(it.label, style = MaterialTheme.typography.titleMedium, color = Ink.Text, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        if (it.value.isNotEmpty()) Text(it.value, style = MaterialTheme.typography.bodyMedium, color = Ink.Text.copy(alpha = 0.85f))
                        if (it.note.isNotEmpty()) Row(Modifier.padding(top = 4.dp)) {
                            Text("→ ", style = MaterialTheme.typography.bodyMedium, color = look.palette.c, fontWeight = FontWeight.Bold)
                            Text(it.note, style = MaterialTheme.typography.bodyMedium, color = look.palette.c)
                        }
                        if (pic != null && source) Text("Gambar: Wikipedia", style = MaterialTheme.typography.labelSmall, color = Ink.Faint, modifier = Modifier.padding(top = 3.dp))
                    }
                }
            }
        }
    }
}

/** Text that is written out letter by letter, once. */
@Composable
private fun Written(text: String, live: Boolean, modifier: Modifier = Modifier, done: Boolean = false) {
    val style = MaterialTheme.typography.bodyLarge
    if (!live || LocalInspectionMode.current) {
        Text(text, modifier, style = style, color = if (done) Ink.Faint else Ink.Text, textDecoration = if (done) TextDecoration.LineThrough else null)
        return
    }
    var n by remember(text) { mutableIntStateOf(0) }
    LaunchedEffect(text) { while (n < text.length) { delay(24); n++ } }
    Text(text.take(n) + if (n < text.length) "▍" else "", modifier, style = style, color = if (done) Ink.Faint else Ink.Text,
        textDecoration = if (done) TextDecoration.LineThrough else null)
}

/**
 * The owner's notes. The notes a reply wrote down are already kept by the Core when the card appears; on the
 * workspace they are written out at the top of the list, and every note can be ticked or removed.
 */
@Composable
fun NotePanel(b: Api.Block, s: AppState?, live: Boolean) {
    val look = LocalLook.current
    val fresh = b.items.map { it.label }.filter { it.isNotEmpty() }
    if (s == null || !live) {
        Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            for (t in fresh) Row {
                Text("✓ ", style = MaterialTheme.typography.bodyLarge, color = look.palette.c)
                Text(t, style = MaterialTheme.typography.bodyLarge, color = Ink.Text)
            }
            Text(if (fresh.isEmpty()) "Daftar catatan dibuka." else "Tersimpan di catatan Tuan.", style = MaterialTheme.typography.labelSmall, color = Ink.Faint)
        }
        return
    }
    // Until the list has arrived from the Core, the new notes are shown from the reply itself.
    val listed = s.notes
    val pending = fresh.filter { t -> listed.none { it.text == t } }
    var draft by remember { mutableStateOf("") }
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(14.dp)).background(Ink.Glass).padding(start = 14.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            BasicTextField(draft, { draft = it.take(500) }, Modifier.weight(1f).padding(vertical = 12.dp), singleLine = true,
                textStyle = MaterialTheme.typography.bodyLarge.copy(color = Ink.Text), cursorBrush = SolidColor(look.palette.a),
                decorationBox = { inner -> Box { if (draft.isEmpty()) Text("Tambah catatan…", style = MaterialTheme.typography.bodyLarge, color = Ink.Faint); inner() } })
            Text("＋", Modifier.clip(CircleShape).clickable { s.addNote(draft); draft = "" }.padding(horizontal = 12.dp, vertical = 8.dp), color = look.palette.a, fontSize = 20.sp)
        }
        Spacer(Modifier.height(4.dp))
        for (t in pending) NoteRow(t, done = false, isNew = true, live = true, onTick = null, onDelete = null)
        for (n in listed.take(40)) NoteRow(n.text, n.done, isNew = n.text in fresh, live = n.text in fresh, onTick = { s.tickNote(n) }, onDelete = { s.deleteNote(n) })
        if (pending.isEmpty() && listed.isEmpty()) Text("Belum ada catatan. Katakan “catat …” atau tulis di atas.", style = MaterialTheme.typography.bodySmall, color = Ink.Faint)
    }
}

@Composable
private fun NoteRow(text: String, done: Boolean, isNew: Boolean, live: Boolean, onTick: (() -> Unit)?, onDelete: (() -> Unit)?) {
    val look = LocalLook.current
    Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).background(if (isNew) look.palette.c.copy(alpha = 0.10f) else Color.Transparent)
        .padding(horizontal = 6.dp, vertical = 7.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(22.dp).clip(CircleShape).background(if (done) look.palette.c else Color.Transparent).border(1.5.dp, if (done) look.palette.c else Ink.Muted, CircleShape)
            .then(if (onTick != null) Modifier.clickable(onClick = onTick) else Modifier), contentAlignment = Alignment.Center) {
            if (done) Text("✓", fontSize = 12.sp, color = Ink.Base, fontWeight = FontWeight.Bold)
        }
        Written(text, live, Modifier.weight(1f).padding(horizontal = 12.dp), done)
        if (isNew) Text("baru", style = MaterialTheme.typography.labelSmall, color = look.palette.c, modifier = Modifier.padding(end = 6.dp))
        if (onDelete != null) Text("✕", Modifier.clip(CircleShape).clickable(onClick = onDelete).padding(6.dp), color = Ink.Faint, fontSize = 13.sp)
    }
}

/**
 * The music player. It plays songs of the phone's own library; what AURA named in its reply only chooses among them.
 * When nothing can be played the card says why, instead of showing a player that does nothing.
 */
@Composable
fun PlayerPanel(b: Api.Block, s: AppState?, live: Boolean) {
    val look = LocalLook.current
    if (s == null || !live) {
        Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
            if (b.text.isNotEmpty()) Text("Diminta: ${b.text}", style = MaterialTheme.typography.bodyMedium, color = Ink.Text)
            for (it in b.items) Text("♪ " + listOf(it.label, it.value).filter { x -> x.isNotEmpty() }.joinToString(" — "), style = MaterialTheme.typography.bodyMedium, color = Ink.Muted)
            Text("Pemutar ada di ruang kerja; lagu diputar dari koleksi di ponsel.", style = MaterialTheme.typography.labelSmall, color = Ink.Faint)
        }
        return
    }
    val now = s.queue.getOrNull(s.playingAt)
    Column {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(64.dp).clip(RoundedCornerShape(16.dp)).background(Brush.linearGradient(listOf(look.palette.a, look.palette.b))), contentAlignment = Alignment.Center) {
                Text(if (now != null && !s.paused) "♫" else "♪", fontSize = 28.sp, color = Ink.Base)
            }
            Column(Modifier.weight(1f).padding(start = 14.dp)) {
                Text(now?.title ?: if (s.queue.isEmpty()) "Belum ada lagu" else "Siap diputar", style = MaterialTheme.typography.titleMedium, color = Ink.Text,
                    maxLines = 2, overflow = TextOverflow.Ellipsis)
                Text(now?.artist?.ifEmpty { "Artis tidak diketahui" } ?: if (b.text.isNotEmpty()) "Diminta: ${b.text}" else "Koleksi di ponsel",
                    style = MaterialTheme.typography.bodySmall, color = Ink.Muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
        if (s.queue.isNotEmpty()) Row(Modifier.fillMaxWidth().padding(top = 12.dp), horizontalArrangement = Arrangement.spacedBy(18.dp, Alignment.CenterHorizontally),
            verticalAlignment = Alignment.CenterVertically) {
            Text("⏮", Modifier.clip(CircleShape).clickable { s.prevSong() }.padding(10.dp), fontSize = 22.sp, color = Ink.Text)
            Box(Modifier.size(58.dp).clip(CircleShape).background(Brush.linearGradient(listOf(look.palette.a, look.palette.b))).clickable { s.togglePlay() },
                contentAlignment = Alignment.Center) { Text(if (now != null && !s.paused) "⏸" else "▶", fontSize = 24.sp, color = Ink.Base) }
            Text("⏭", Modifier.clip(CircleShape).clickable { s.nextSong() }.padding(10.dp), fontSize = 22.sp, color = Ink.Text)
        }
        val why = when (s.musicState) {
            "permission" -> "AURA perlu izin untuk melihat lagu di ponsel ini. Daftar lagu tidak dikirim ke mana pun."
            "empty" -> "Tidak ada lagu di koleksi ponsel ini. AURA baru bisa memutar lagu yang tersimpan di ponsel; streaming belum tersedia."
            "nomatch" -> "Tidak ada lagu di ponsel yang cocok dengan permintaan itu, jadi tidak ada yang diputar otomatis. Ini koleksi yang ada:"
            else -> ""
        }
        if (why.isNotEmpty()) Text(why, style = MaterialTheme.typography.bodySmall, color = Ink.Muted, modifier = Modifier.padding(top = 12.dp))
        if (s.musicState == "permission") Box(Modifier.padding(top = 10.dp)) { Pill("Izinkan akses musik", "🔓", filled = true) { s.allowMusic() } }
        if (s.queue.isNotEmpty()) {
            Text("ANTREAN · ${s.queue.size} lagu", style = MaterialTheme.typography.labelSmall, color = Ink.Faint, modifier = Modifier.padding(top = 14.dp, bottom = 4.dp))
            s.queue.forEachIndexed { i, song -> QueueRow(i, song, i == s.playingAt) { s.playAt(i) } }
        }
        // What AURA named but the phone does not have is said, not hidden.
        val missing = b.items.filter { w -> w.label.isNotEmpty() && s.queue.none { q -> q.title.contains(w.label, ignoreCase = true) } }
        if (s.musicState != "permission" && missing.isNotEmpty()) Text("Tidak ada di ponsel: " + missing.joinToString(", ") { it.label },
            style = MaterialTheme.typography.labelSmall, color = Ink.Faint, modifier = Modifier.padding(top = 8.dp))
    }
}

@Composable
private fun QueueRow(i: Int, song: Tunes.Song, playing: Boolean, onClick: () -> Unit) {
    val look = LocalLook.current
    Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).background(if (playing) look.palette.a.copy(alpha = 0.16f) else Color.Transparent)
        .clickable(onClick = onClick).padding(horizontal = 8.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(if (playing) "♫" else "${i + 1}", Modifier.width(26.dp), style = MaterialTheme.typography.labelLarge, color = if (playing) look.palette.a else Ink.Faint)
        Column(Modifier.weight(1f)) {
            Text(song.title, style = MaterialTheme.typography.bodyMedium, color = Ink.Text, maxLines = 1, overflow = TextOverflow.Ellipsis,
                fontWeight = if (playing) FontWeight.SemiBold else FontWeight.Normal)
            if (song.artist.isNotEmpty()) Text(song.artist, style = MaterialTheme.typography.labelSmall, color = Ink.Faint, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Text(Tunes.length(song.durationMs), style = MaterialTheme.typography.labelSmall, color = Ink.Faint)
    }
}

/** The page AURA designed (ADR-0060); the workspace took its place as home. */
@Composable
fun BoardBody(s: AppState) {
    val h = s.home
    if (h == null) {
        Column(Modifier.fillMaxWidth().padding(top = 80.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Orb(OrbState.THINKING, size = 96.dp)
            Spacer(Modifier.height(16.dp))
            Text(if (s.offline) "Belum bisa menghubungi AURA" else "Menyiapkan papan…", style = MaterialTheme.typography.bodyLarge, color = Ink.Muted)
        }
    } else {
        Blocks(h.screen.blocks, layout = LocalLook.current.layout, onAction = { s.propose(it) }, widget = { Widget(s, it) }, s = s)
    }
}
