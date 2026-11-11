package ai.aura.client.ui

import ai.aura.client.Api
import ai.aura.client.AppState
import ai.aura.client.HomeLogic
import ai.aura.client.ReplyView
import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.List
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Email
import androidx.compose.material.icons.filled.Home
import androidx.compose.material.icons.filled.Menu
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay
import org.json.JSONObject

/**
 * The screens of the app (ADR-0060). They read [AppState] and call its functions; nothing here talks to the network.
 * The look — colours, motion, arrangement — follows the scene of the moment, so the app is never twice quite the same.
 */
@Composable
fun AuraApp(s: AppState) {
    BackHandler(enabled = s.signedIn && (s.talkOpen || s.openTask != null || s.page != null || s.tab != "home" || s.typing ||
        s.stage.isNotEmpty() || s.heard.isNotEmpty() || s.caption.isNotEmpty())) { s.back() }
    AuraScreen(s)
}

@Composable
fun AuraScreen(s: AppState) {
    AuraTheme(s.scene) {
        AuroraBackground(Modifier.fillMaxSize()) {
            if (!s.signedIn) LoginScreen(s) else Shell(s)
            Toast(s)
        }
    }
}

@Composable
private fun BoxScope.Shell(s: AppState) {
    Column(Modifier.fillMaxSize().systemBarsPadding().imePadding()) {
        AnimatedVisibility(s.micOn) { SensorBanner(s) }
        AnimatedVisibility(s.offline) {
            Text("Tidak terhubung ke AURA — mencoba lagi…", Modifier.fillMaxWidth().background(Ink.Danger.copy(alpha = 0.18f)).padding(8.dp),
                style = MaterialTheme.typography.labelMedium, color = Ink.Text, textAlign = TextAlign.Center)
        }
        Box(Modifier.weight(1f).fillMaxWidth()) {
            AnimatedContent(
                targetState = s.page ?: s.tab,
                transitionSpec = { (fadeIn(tween(280)) + slideInVertically(tween(280)) { it / 20 }) togetherWith fadeOut(tween(140)) },
                label = "page",
            ) { where ->
                when (where) {
                    "home" -> WorkspaceScreen(s)
                    "board" -> BoardPage(s)
                    "chat" -> ChatScreen(s)
                    "inbox" -> InboxScreen(s)
                    "more" -> MoreScreen(s)
                    "look" -> LookPage(s)
                    "settings" -> SettingsPage(s)
                    "memory" -> ListPage(s, "Yang saya ingat", "Ingatan adalah fakta dan preferensi yang AURA simpan dari percakapan dengan Tuan.",
                        s.memories.map { Triple(LocalLook.current.palette.c, it.content, it.kind) })
                    "skills" -> ListPage(s, "Keahlian", "Cara kerja yang sudah AURA pelajari dan boleh dipakai ulang.",
                        s.skills.map { Triple(if (it.status == "active") Ink.Success else Ink.Faint, it.name, (listOf(it.status) + it.triggers.take(4)).joinToString(" · ")) })
                    "schedules" -> SchedulesPage(s)
                    "server" -> ServerLogPage(s)
                    "limits" -> ListPage(s, "Yang belum saya bisa", "Keterbatasan yang AURA catat sendiri, dengan statusnya. Ini daftar jujur, bukan janji.",
                        s.limitations.map { Triple(if (it.status == "resolved") Ink.Success else Ink.Warning, it.title, it.category + " · " + it.status) })
                    "self" -> SelfPage(s)
                }
            }
        }
        // The workspace has its own panel to talk to AURA; the other pages keep the bar.
        if (!s.onWorkspace) BottomBar(s)
    }
    if (s.openTask != null) TaskDetail(s)
}

@Composable
private fun BoxScope.Toast(s: AppState) {
    val m = s.message
    var shown by remember { mutableStateOf("") }
    if (m != null) shown = m
    LaunchedEffect(m) {
        if (m != null) { delay(3800); if (s.message == m) s.message = null }
    }
    AnimatedVisibility(m != null, Modifier.align(Alignment.BottomCenter).systemBarsPadding().imePadding().padding(bottom = 96.dp, start = 24.dp, end = 24.dp),
        enter = fadeIn() + slideInVertically { it / 2 }, exit = fadeOut() + slideOutVertically { it / 2 }) {
        Text(shown, Modifier.clip(RoundedCornerShape(18.dp)).background(Color(0xF0182035)).border(1.dp, Ink.Stroke, RoundedCornerShape(18.dp))
            .clickable { s.message = null }.padding(horizontal = 18.dp, vertical = 12.dp),
            style = MaterialTheme.typography.bodyMedium, color = Ink.Text)
    }
}

/** Always visible while the microphone is on, with one button that stops everything. */
@Composable
private fun SensorBanner(s: AppState) {
    Row(Modifier.fillMaxWidth().background(Ink.Danger.copy(alpha = 0.85f)).padding(horizontal = 14.dp, vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Text("● Mikrofon dipakai AURA", Modifier.weight(1f), style = MaterialTheme.typography.labelLarge, color = Color.White)
        TextButton(onClick = { s.stopSensors() }) { Text("Hentikan", color = Color.White) }
    }
}

// ------------------------------------------------------------------ shared pieces

@Composable
fun Field(value: String, onChange: (String) -> Unit, label: String, modifier: Modifier = Modifier, password: Boolean = false, single: Boolean = true) {
    OutlinedTextField(
        value, onChange, modifier.fillMaxWidth(), label = { Text(label) }, singleLine = single, maxLines = if (single) 1 else 5,
        visualTransformation = if (password) PasswordVisualTransformation() else VisualTransformation.None,
        keyboardOptions = if (password) KeyboardOptions(keyboardType = KeyboardType.Password) else KeyboardOptions.Default,
        shape = RoundedCornerShape(16.dp),
    )
}

@Composable
private fun RoundButton(filled: Boolean, onClick: () -> Unit, content: @Composable () -> Unit) {
    val look = LocalLook.current
    Box(
        Modifier.padding(start = 4.dp).size(42.dp).clip(CircleShape)
            .background(if (filled) Brush.linearGradient(listOf(look.palette.a, look.palette.b)) else Brush.linearGradient(listOf(Ink.Glass, Ink.Glass)))
            .clickable(onClick = onClick),
        contentAlignment = Alignment.Center,
    ) { content() }
}

/** Where the owner types or dictates a request, and chooses whether it may use free models. */
@Composable
fun Composer(s: AppState, modifier: Modifier = Modifier) {
    val look = LocalLook.current
    val shape = RoundedCornerShape(26.dp)
    Column(modifier) {
        Row(
            Modifier.fillMaxWidth().clip(shape).background(Ink.Raised.copy(alpha = 0.88f)).border(1.dp, look.palette.a.copy(alpha = 0.35f), shape)
                .padding(start = 18.dp, end = 6.dp, top = 5.dp, bottom = 5.dp),
            verticalAlignment = Alignment.Bottom,
        ) {
            BasicTextField(
                value = s.draft, onValueChange = { s.draft = it }, modifier = Modifier.weight(1f).padding(vertical = 10.dp),
                textStyle = MaterialTheme.typography.bodyLarge.copy(color = Ink.Text), cursorBrush = SolidColor(look.palette.a), maxLines = 5,
                decorationBox = { inner ->
                    Box {
                        if (s.draft.isEmpty()) Text("Tanya atau perintahkan AURA…", style = MaterialTheme.typography.bodyLarge, color = Ink.Faint)
                        inner()
                    }
                },
            )
            RoundButton(false, { s.dictate() }) { Text("🎙", fontSize = 18.sp) }
            RoundButton(true, { s.ask(s.draft) }) { Icon(Icons.AutoMirrored.Filled.Send, "Kirim", tint = Ink.Base, modifier = Modifier.size(20.dp)) }
        }
        Text(
            if (s.public) "🌐 Publik — tanpa data pribadi, boleh model gratis" else "🔒 Pribadi — hanya model yang tidak menyimpan isi",
            Modifier.padding(start = 8.dp, top = 4.dp).clip(RoundedCornerShape(10.dp)).clickable { s.public = !s.public }.padding(horizontal = 8.dp, vertical = 4.dp),
            style = MaterialTheme.typography.labelMedium, color = if (s.public) look.palette.c else Ink.Faint,
        )
    }
}

@Composable
private fun AnswerBox(hint: String, onSend: (String) -> Unit) {
    var text by remember { mutableStateOf("") }
    Row(verticalAlignment = Alignment.CenterVertically) {
        Field(text, { text = it }, hint, Modifier.weight(1f), single = false)
        RoundButton(true, { onSend(text); text = "" }) { Icon(Icons.AutoMirrored.Filled.Send, "Kirim", tint = Ink.Base, modifier = Modifier.size(20.dp)) }
    }
}

@Composable
private fun Heading(title: String, sub: String = "", onBack: (() -> Unit)? = null) {
    Row(Modifier.fillMaxWidth().padding(bottom = 12.dp, top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        if (onBack != null) Icon(Icons.AutoMirrored.Filled.ArrowBack, "Kembali", tint = Ink.Text,
            modifier = Modifier.padding(end = 10.dp).clip(CircleShape).clickable(onClick = onBack).padding(6.dp))
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.headlineMedium, color = Ink.Text)
            if (sub.isNotEmpty()) Text(sub, style = MaterialTheme.typography.bodySmall, color = Ink.Muted)
        }
    }
}

@Composable
private fun Page(content: @Composable ColumnScope.() -> Unit) {
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 12.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp), content = content)
}

// ------------------------------------------------------------------ login

@Composable
private fun LoginScreen(s: AppState) {
    var server by remember { mutableStateOf(s.store.server) }
    var user by remember { mutableStateOf(s.store.username) }
    var pass by remember { mutableStateOf("") }
    Column(
        Modifier.fillMaxSize().systemBarsPadding().imePadding().verticalScroll(rememberScrollState()).padding(horizontal = 28.dp, vertical = 24.dp),
        horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Spacer(Modifier.height(36.dp))
        Orb(if (s.busy) OrbState.THINKING else OrbState.IDLE, size = 132.dp)
        Text("AURA", style = MaterialTheme.typography.displaySmall, color = Ink.Text)
        Text("Asisten pribadi Tuan. Masuk dengan akun AURA.", style = MaterialTheme.typography.bodyMedium, color = Ink.Muted, textAlign = TextAlign.Center)
        Spacer(Modifier.height(8.dp))
        Field(server, { server = it }, "Alamat server")
        Field(user, { user = it }, "Nama pengguna")
        Field(pass, { pass = it }, "Kata sandi", password = true)
        Button(onClick = { s.login(server, user, pass); pass = "" }, enabled = !s.busy, modifier = Modifier.fillMaxWidth().height(52.dp), shape = RoundedCornerShape(18.dp)) {
            Text(if (s.busy) "Menghubungkan…" else "Masuk")
        }
        Text("Kata sandi tidak disimpan. Ponsel hanya menyimpan token perangkat terenkripsi (Android Keystore), yang dicabut saat keluar.",
            style = MaterialTheme.typography.bodySmall, color = Ink.Faint, textAlign = TextAlign.Center)
    }
}

// ------------------------------------------------------------------ the board: the screen AURA designed

@Composable
private fun BoardPage(s: AppState) {
    Page {
        Heading("Papan", "Ringkasan yang AURA rancang untuk Tuan") { s.page = null }
        BoardBody(s)
        Spacer(Modifier.height(8.dp))
    }
}

// ------------------------------------------------------------------ conversation

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun ChatScreen(s: AppState) {
    Column(Modifier.fillMaxSize()) {
        if (s.tasks.isEmpty()) {
            Column(Modifier.weight(1f).fillMaxWidth().padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.Center) {
                Orb(OrbState.IDLE, size = 96.dp)
                Spacer(Modifier.height(16.dp))
                Text("Apa yang bisa saya kerjakan?", style = MaterialTheme.typography.headlineSmall, color = Ink.Text)
                Spacer(Modifier.height(16.dp))
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.CenterHorizontally), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    for (x in HomeLogic.suggestions(s.home?.context?.dayPart ?: "")) Pill(x.label, x.icon) { s.suggest(x) }
                }
            }
        } else {
            LazyColumn(Modifier.weight(1f).fillMaxWidth(), reverseLayout = true, contentPadding = PaddingValues(horizontal = 16.dp, vertical = 12.dp),
                verticalArrangement = Arrangement.spacedBy(22.dp)) {
                items(s.tasks, key = { it.id }) { t -> TaskCard(s, t) }
            }
        }
        Composer(s, Modifier.padding(horizontal = 12.dp, vertical = 6.dp))
    }
}

/** One exchange: what the owner asked, then what AURA is doing or what it answered. */
@Composable
private fun TaskCard(s: AppState, t: Api.Task) {
    val look = LocalLook.current
    Column(Modifier.fillMaxWidth().animateContentSize(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
            Text(t.objective, Modifier.widthIn(max = 310.dp).clip(RoundedCornerShape(20.dp, 20.dp, 6.dp, 20.dp))
                .background(Brush.linearGradient(listOf(look.palette.a.copy(alpha = 0.38f), look.palette.b.copy(alpha = 0.26f))))
                .clickable { s.open(t.id) }.padding(horizontal = 14.dp, vertical = 10.dp),
                style = MaterialTheme.typography.bodyLarge, color = Ink.Text, maxLines = 8, overflow = TextOverflow.Ellipsis)
        }
        t.heardAs?.takeIf { !it.trim().equals(t.objective.trim(), ignoreCase = true) }?.let {
            Text("Saya pahami: $it", Modifier.fillMaxWidth(), style = MaterialTheme.typography.bodySmall, color = Ink.Faint, textAlign = TextAlign.End)
        }
        val r = t.reply
        when {
            HomeLogic.working(t.state) -> Row(verticalAlignment = Alignment.CenterVertically) {
                Orb(OrbState.THINKING, size = 30.dp)
                Spacer(Modifier.width(10.dp))
                Text(s.activity[t.id] ?: "Bekerja…", Modifier.weight(1f), style = MaterialTheme.typography.bodyMedium, color = look.palette.c)
                TextButton(onClick = { s.cancel(t.id) }) { Text("Batalkan", color = Ink.Faint) }
            }
            t.state == "WAITING_INPUT" -> GlassCard(Modifier.fillMaxWidth(), tint = Ink.Info) {
                Text(s.questions[t.id] ?: "Saya perlu jawaban Tuan untuk melanjutkan.", style = MaterialTheme.typography.bodyLarge, color = Ink.Text)
                Spacer(Modifier.height(8.dp))
                AnswerBox("Jawaban Tuan") { s.answer(t.id, it) }
            }
            t.state == "WAITING_APPROVAL" -> GlassCard(Modifier.fillMaxWidth(), tint = Ink.Warning, onClick = { s.go("inbox") }) {
                Text("🛡 Menunggu persetujuan Tuan — buka Kotak masuk", style = MaterialTheme.typography.bodyMedium, color = Ink.Text)
            }
            r == null -> Problem(t)
        }
        if (r != null && !HomeLogic.working(t.state)) {
            ReplyBody(s, r, t.id)
            if (t.state != "COMPLETED" && !t.state.startsWith("WAITING")) Problem(t)
            Row(verticalAlignment = Alignment.CenterVertically) {
                Pill("Jelaskan", "🔊") { s.present(t) }
                Spacer(Modifier.width(10.dp))
                Text(listOf(HomeLogic.ago(t.updatedAt), if (t.costUsd > 0) "$" + ReplyView.fmt(t.costUsd) else "", if (t.public) "publik" else "")
                    .filter { it.isNotEmpty() }.joinToString(" · "), Modifier.weight(1f), style = MaterialTheme.typography.labelSmall, color = Ink.Faint)
                TextButton(onClick = { s.open(t.id) }) { Text("Rincian", color = Ink.Muted) }
            }
        }
    }
}

@Composable
private fun Problem(t: Api.Task) {
    GlassCard(Modifier.fillMaxWidth(), tint = if (t.state == "CANCELLED") Ink.Faint else Ink.Danger, pad = 14.dp) {
        Text(HomeLogic.stateLabel(t.state), style = MaterialTheme.typography.titleSmall, color = Ink.Text)
        HomeLogic.explain(t.reasonCode)?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = Ink.Muted) }
    }
}

/** AURA's answer: the words, then what it shows, then the honest notes about where the answer came from. */
@Composable
private fun ReplyBody(s: AppState, r: Api.Reply, taskId: String) {
    if (r.text.isNotEmpty()) SelectionContainer { Text(r.text, style = MaterialTheme.typography.bodyLarge, color = Ink.Text) }
    if (r.view.isNotEmpty()) Blocks(r.view, layout = r.scene.layout.ifEmpty { "stack" }, onAction = { s.propose(it) }, s = s)
    for (n in r.notes) Text("ⓘ " + ReplyView.noteText(n), style = MaterialTheme.typography.bodySmall, color = Ink.Faint, fontStyle = FontStyle.Italic)
    for (f in r.files) Text("📎 $f", Modifier.clip(RoundedCornerShape(8.dp)).clickable { s.openFile(taskId, f) }.padding(vertical = 4.dp),
        style = MaterialTheme.typography.bodyMedium, color = LocalLook.current.palette.c)
}

// ------------------------------------------------------------------ inbox: approvals, questions, news

@Composable
private fun InboxScreen(s: AppState) {
    Page {
        Heading("Kotak masuk", "Yang menunggu keputusan atau jawaban Tuan")
        if (s.approvals.isEmpty() && s.questions.isEmpty() && s.notices.isEmpty()) {
            GlassCard(Modifier.fillMaxWidth(), emphasis = "quiet") { Text("Tidak ada yang menunggu. Semua tenang.", style = MaterialTheme.typography.bodyMedium, color = Ink.Muted) }
        }
        for (a in s.approvals) ApprovalCard(s, a)
        for ((id, q) in s.questions) GlassCard(Modifier.fillMaxWidth(), tint = Ink.Info) {
            Text("❓ AURA bertanya", style = MaterialTheme.typography.titleSmall, color = Ink.Muted)
            Spacer(Modifier.height(6.dp))
            Text(q, style = MaterialTheme.typography.bodyLarge, color = Ink.Text)
            Spacer(Modifier.height(8.dp))
            AnswerBox("Jawaban Tuan") { s.answer(id, it) }
        }
        if (s.notices.isNotEmpty()) GlassCard(Modifier.fillMaxWidth()) {
            Text("🔔 Kabar", style = MaterialTheme.typography.titleSmall, color = Ink.Muted)
            Spacer(Modifier.height(6.dp))
            for (n in s.notices.take(30)) Entry(if (n.read) Ink.Faint else Ink.Info, n.title, if (n.read) "" else "baru") { s.read(n) }
        }
    }
}

/** An action that waits for the owner. Approving sends the hash of exactly what is shown here; the Core checks it. */
@Composable
private fun ApprovalCard(s: AppState, a: Api.Approval) {
    var confirm by remember { mutableStateOf(false) }
    GlassCard(Modifier.fillMaxWidth(), tint = Ink.Warning) {
        Text("🛡 Perlu persetujuan", style = MaterialTheme.typography.titleSmall, color = Ink.Muted)
        Spacer(Modifier.height(6.dp))
        if (a.objective.isNotEmpty()) Text(a.objective, style = MaterialTheme.typography.bodyLarge, color = Ink.Text)
        Spacer(Modifier.height(8.dp))
        Fact("Tindakan", a.toolId)
        Fact("Sasaran", a.target)
        if (a.why.isNotEmpty()) Fact("Alasan", a.why)
        if (a.estimatedCost.isNotEmpty()) Fact("Perkiraan biaya", a.estimatedCost)
        if (a.expiresAt.isNotEmpty()) Fact("Berlaku sampai", a.expiresAt)
        Fact("Sidik tindakan", a.actionHash.take(12) + "…")
        Spacer(Modifier.height(10.dp))
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            Button(onClick = { confirm = true }, shape = RoundedCornerShape(14.dp)) { Text("Setujui") }
            OutlinedButton(onClick = { s.reject(a) }, shape = RoundedCornerShape(14.dp)) { Text("Tolak") }
        }
    }
    if (confirm) AlertDialog(
        onDismissRequest = { confirm = false },
        title = { Text("Setujui tindakan ini?") },
        text = { Text("${a.toolId}\n${a.target}\n\nPersetujuan hanya berlaku untuk tindakan persis ini (sidik ${a.actionHash.take(12)}…), satu kali.") },
        confirmButton = { TextButton(onClick = { confirm = false; s.approve(a) }) { Text("Setujui") } },
        dismissButton = { TextButton(onClick = { confirm = false }) { Text("Batal") } },
    )
}

@Composable
private fun Fact(label: String, value: String) {
    Row(Modifier.padding(vertical = 2.dp)) {
        Text(label, Modifier.width(118.dp), style = MaterialTheme.typography.bodySmall, color = Ink.Faint)
        Text(value, Modifier.weight(1f), style = MaterialTheme.typography.bodySmall, color = Ink.Text)
    }
}

// ------------------------------------------------------------------ more

@Composable
private fun MoreScreen(s: AppState) {
    Page {
        Heading("Lainnya", if (s.username.isNotEmpty()) "Masuk sebagai ${s.username}" else "")
        s.self?.let { me ->
            GlassCard(Modifier.fillMaxWidth(), emphasis = "strong", onClick = { s.openPage("self") }) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Orb(OrbState.IDLE, size = 44.dp)
                    Spacer(Modifier.width(12.dp))
                    Column {
                        Text("AURA ${me.version}", style = MaterialTheme.typography.titleMedium, color = Ink.Text)
                        Text("${me.succeeded} dari ${me.finished} tugas berhasil · ${me.verified} terverifikasi", style = MaterialTheme.typography.bodySmall, color = Ink.Muted)
                    }
                }
            }
        }
        val menu = listOf(
            listOf("board", "🧩", "Papan", "Ringkasan yang AURA rancang"), listOf("look", "🎨", "Tampilan", "Minta AURA merancang ulang papan"),
            listOf("note", "📝", "Catatan", "Daftar catatan Tuan"), listOf("player", "🎵", "Musik", "Putar lagu di ponsel ini"), listOf("memory", "💾", "Ingatan", "Yang AURA ingat tentang Tuan"),
            listOf("skills", "🧠", "Keahlian", "Cara kerja yang sudah dipelajari"), listOf("schedules", "⏰", "Jadwal", "Tugas rutin dan pengingat"),
            listOf("limits", "🧭", "Keterbatasan", "Yang belum AURA bisa"), listOf("server", "🖥️", "Perintah server", "Log pengawas: semua perintah AURA"), listOf("settings", "⚙️", "Pengaturan", "Sensor, privasi, akun"),
        )
        for (pair in menu.chunked(2)) Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            for (m in pair) GlassCard(Modifier.weight(1f), onClick = { if (m[0] == "note" || m[0] == "player") s.openTool(m[0]) else s.openPage(m[0]) }) {
                Text(m[1], fontSize = 24.sp)
                Spacer(Modifier.height(8.dp))
                Text(m[2], style = MaterialTheme.typography.titleMedium, color = Ink.Text)
                Text(m[3], style = MaterialTheme.typography.bodySmall, color = Ink.Muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

@Composable
private fun ListPage(s: AppState, title: String, about: String, rows: List<Triple<Color, String, String>>) {
    Page {
        Heading(title, about) { s.page = null }
        if (rows.isEmpty()) Text("Belum ada.", style = MaterialTheme.typography.bodyMedium, color = Ink.Faint)
        else GlassCard(Modifier.fillMaxWidth()) { for ((dot, head, sub) in rows) Entry(dot, head, sub) }
    }
}

/**
 * Every server command AURA ran or wanted to run (ADR-0065): what the watcher decided and why, what happened, and
 * which internet hosts it reached. The log is written by the Core; this page only reads it.
 */
@Composable
private fun ServerLogPage(s: AppState) {
    var open by remember { mutableStateOf<String?>(null) }
    Page {
        Heading("Perintah server", "AURA menjalankan perintah di kotak pasirnya sendiri. Pengawas menilai tiap perintah sebelum jalan.") { s.page = null }
        Text("Aman = jalan otomatis · Perlu izin = menunggu persetujuan Tuan · Ditolak = tidak pernah dijalankan",
            style = MaterialTheme.typography.bodySmall, color = Ink.Faint)
        if (s.shellLog.isEmpty()) Text("Belum ada perintah.", style = MaterialTheme.typography.bodyMedium, color = Ink.Faint)
        for (r in s.shellLog) GlassCard(Modifier.fillMaxWidth(), tint = when (r.verdict) { "blocked" -> Ink.Danger; "review" -> Ink.Warning; else -> null },
            onClick = { open = if (open == r.id) null else r.id }) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(when (r.verdict) { "blocked" -> "⛔ Ditolak"; "review" -> "✋ Perlu izin"; else -> "✅ Aman" }, style = MaterialTheme.typography.labelLarge,
                    color = Ink.Text, modifier = Modifier.weight(1f))
                Text(HomeLogic.serverStatus(r.status, r.exitCode, r.durationMs), style = MaterialTheme.typography.labelMedium, color = Ink.Muted)
            }
            Text("$ " + r.command, style = MaterialTheme.typography.bodySmall, color = Ink.Text, fontFamily = FontFamily.Monospace,
                maxLines = if (open == r.id) 40 else 3, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 6.dp))
            if (r.reasons.isNotEmpty()) Text(r.reasons.joinToString(" · "), style = MaterialTheme.typography.labelSmall, color = LocalLook.current.palette.c)
            if (r.hosts.isNotEmpty()) Text("🌐 " + r.hosts.joinToString(", "), style = MaterialTheme.typography.labelSmall, color = Ink.Faint)
            if (open == r.id) {
                if (r.stdout.isNotEmpty()) SelectionContainer { Text(r.stdout.take(4000), style = MaterialTheme.typography.bodySmall, color = Ink.Muted, fontFamily = FontFamily.Monospace) }
                if (r.stderr.isNotEmpty()) SelectionContainer { Text(r.stderr.take(2000), style = MaterialTheme.typography.bodySmall, color = Ink.Warning, fontFamily = FontFamily.Monospace) }
                if (r.error.isNotEmpty()) Text(r.error, style = MaterialTheme.typography.bodySmall, color = Ink.Danger)
                Text(HomeLogic.ago(java.time.Instant.ofEpochMilli(r.createdAt).toString()), style = MaterialTheme.typography.labelSmall, color = Ink.Faint)
            }
        }
    }
}

/** Reminders and schedules (ADR-0063). Ask AURA in conversation to add one ("ingatkan saya …", "setiap pagi …"). */
@Composable
private fun SchedulesPage(s: AppState) {
    Page {
        Heading("Jadwal", "Pengingat dan tugas yang AURA jalankan sendiri pada waktunya. Katakan \"ingatkan saya …\" untuk menambah.") { s.page = null }
        val active = s.reminders.filter { it.enabled }
        if (active.isEmpty() && s.triggers.isEmpty()) Text("Belum ada.", style = MaterialTheme.typography.bodyMedium, color = Ink.Faint)
        if (active.isNotEmpty()) GlassCard(Modifier.fillMaxWidth()) {
            Text("⏰ Pengingat", style = MaterialTheme.typography.titleSmall, color = Ink.Muted)
            for (r in active) Row(Modifier.fillMaxWidth().padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(r.text, style = MaterialTheme.typography.bodyLarge, color = Ink.Text)
                    Text(if (r.daily.isNotEmpty()) "Setiap hari ${r.daily}" else HomeLogic.whenText(java.time.Instant.ofEpochMilli(r.nextAt)
                        .atZone(java.time.ZoneId.systemDefault()).toLocalDateTime().withSecond(0).withNano(0).toString()),
                        style = MaterialTheme.typography.labelMedium, color = LocalLook.current.palette.c)
                }
                TextButton(onClick = { s.deleteReminder(r) }) { Text("Hapus", color = Ink.Faint) }
            }
        }
        if (s.triggers.isNotEmpty()) GlassCard(Modifier.fillMaxWidth()) {
            Text("🔁 Tugas terjadwal", style = MaterialTheme.typography.titleSmall, color = Ink.Muted)
            for (it in s.triggers) Entry(if (it.enabled) Ink.Success else Ink.Faint, it.objective.ifEmpty { it.name },
                listOf(if (it.dailyAt.isNotEmpty()) "Setiap hari ${it.dailyAt}" else it.kind, if (it.enabled) "" else "nonaktif").filter { x -> x.isNotEmpty() }.joinToString(" · "))
        }
    }
}

@Composable
private fun SelfPage(s: AppState) {
    Page {
        Heading("Tentang AURA", "Ringkasan yang AURA susun dari catatannya sendiri") { s.page = null }
        val me = s.self
        if (me == null) Text("Memuat…", color = Ink.Faint) else {
            GlassCard(Modifier.fillMaxWidth(), emphasis = "strong") {
                Stats(listOf("Berhasil" to "${me.succeeded}/${me.finished}", "Terverifikasi" to "${me.verified}", "Keahlian" to "${me.skills}", "Ingatan" to "${me.memories}"))
            }
            GlassCard(Modifier.fillMaxWidth()) { SelectionContainer { Text(me.summary, style = MaterialTheme.typography.bodyMedium, color = Ink.Text) } }
            Text("Server AURA ${me.version}", style = MaterialTheme.typography.labelSmall, color = Ink.Faint)
        }
        s.home?.context?.providerIssues?.takeIf { it.isNotEmpty() }?.let {
            GlassCard(Modifier.fillMaxWidth(), tint = Ink.Danger) { Text("Penyedia model bermasalah: " + it.joinToString(", "), style = MaterialTheme.typography.bodyMedium, color = Ink.Text) }
        }
    }
}

/** AURA designs the home screen on request; every design is kept as a version the owner can return to. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun LookPage(s: AppState) {
    var wish by remember { mutableStateOf("") }
    Page {
        Heading("Tampilan", "Papan dirancang AURA sesuai permintaan Tuan") { s.page = null }
        GlassCard(Modifier.fillMaxWidth(), emphasis = "strong") {
            Text("Katakan seperti apa papan yang Tuan mau. AURA menyusun rancangannya sebagai data yang diperiksa server — bukan kode — jadi aman dan selalu bisa dikembalikan.",
                style = MaterialTheme.typography.bodyMedium, color = Ink.Text)
            Spacer(Modifier.height(10.dp))
            Field(wish, { wish = it }, "Contoh: fokus ke yang butuh keputusan saya", single = false)
            Spacer(Modifier.height(10.dp))
            Button(onClick = { s.redesign(wish); wish = "" }, enabled = wish.isNotBlank(), shape = RoundedCornerShape(14.dp)) { Text("Rancang ulang") }
        }
        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            for (x in listOf("Ringkas, fokus pada yang butuh keputusan saya", "Jadwal dan keahlian di atas, nuansa tenang zamrud",
                "Seperti dasbor: angka hari ini, aktivitas, lalu saran", "Gaya cerita, lapang, warna ungu")) Pill(x) { wish = x }
        }
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            OutlinedButton(onClick = { s.screenAction("rollback") }, shape = RoundedCornerShape(14.dp)) { Text("Versi sebelumnya") }
            OutlinedButton(onClick = { s.screenAction("reset") }, shape = RoundedCornerShape(14.dp)) { Text("Pakai bawaan") }
        }
        if (s.versions.isNotEmpty()) Text("Riwayat rancangan", style = MaterialTheme.typography.titleSmall, color = Ink.Muted)
        for (v in s.versions) GlassCard(Modifier.fillMaxWidth(), glow = v.active) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text("Versi ${v.version}" + if (v.active) " · dipakai" else "", style = MaterialTheme.typography.titleSmall, color = Ink.Text)
                    if (v.request.isNotEmpty()) Text("“${v.request}”", style = MaterialTheme.typography.bodySmall, color = Ink.Muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    if (v.summary.isNotEmpty()) Text(v.summary, style = MaterialTheme.typography.bodySmall, color = Ink.Faint, maxLines = 3, overflow = TextOverflow.Ellipsis)
                    Text("${v.screen.blocks.size} bagian" + listOf(v.screen.scene.mood, v.screen.scene.accent, v.screen.scene.layout).filter { it.isNotEmpty() }.joinToString("") { " · $it" },
                        style = MaterialTheme.typography.labelSmall, color = Ink.Faint)
                }
                if (!v.active) TextButton(onClick = { s.screenAction("use", v.version) }) { Text("Pakai") }
            }
        }
    }
}

@Composable
private fun SettingsPage(s: AppState) {
    var server by remember { mutableStateOf(s.store.server) }
    var mic by remember { mutableStateOf(s.store.micAllowed) }
    var speak by remember { mutableStateOf(s.store.speakResults) }
    var checks by remember { mutableStateOf(s.store.backgroundChecks) }
    Page {
        Heading("Pengaturan") { s.page = null }
        GlassCard(Modifier.fillMaxWidth()) {
            Text("Sensor dan privasi", style = MaterialTheme.typography.titleSmall, color = Ink.Muted)
            Toggle("Izinkan mikrofon", "Untuk dikte dan percakapan langsung, hanya setelah Tuan menekan 🎙 atau orb", mic) { mic = it; s.setMic(it) }
            Toggle("Bacakan hasil", "Text-to-speech di perangkat", speak) { speak = it; s.store.speakResults = it }
            Toggle("Cek di latar belakang", "Notifikasi dan persetujuan; diatur Android, minimal tiap 15 menit", checks) { checks = it; s.setBackground(it) }
            Spacer(Modifier.height(8.dp))
            OutlinedButton(onClick = { s.stopSensors() }, shape = RoundedCornerShape(14.dp)) { Text("Hentikan semua sensor sekarang") }
        }
        GlassCard(Modifier.fillMaxWidth(), emphasis = "quiet") {
            Text("Suara dikenali oleh pengenal suara perangkat (offline bila tersedia) dan hanya teksnya yang dikirim ke AURA; " +
                "percakapan langsung berhenti saat aplikasi ditutup, saat hening, atau saat Tuan berkata \"berhenti\"; " +
                "persetujuan tindakan tidak pernah diterima lewat suara; rekaman suara tidak disimpan. " +
                "Kamera dan layar tidak dipakai di versi ini. Musik diputar dari koleksi di ponsel; daftar lagu tidak dikirim ke AURA. " +
                "Catatan disimpan di server AURA milik Tuan. Kata sandi tidak disimpan; ponsel hanya menyimpan token perangkat " +
                "terenkripsi (Android Keystore), yang dicabut saat keluar. Tampilan yang dirancang AURA adalah data, bukan kode.",
                style = MaterialTheme.typography.bodySmall, color = Ink.Muted)
        }
        GlassCard(Modifier.fillMaxWidth()) {
            Text("Akun", style = MaterialTheme.typography.titleSmall, color = Ink.Muted)
            Spacer(Modifier.height(8.dp))
            Field(server, { server = it }, "Alamat server")
            Spacer(Modifier.height(10.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                OutlinedButton(onClick = { s.saveServer(server) }, shape = RoundedCornerShape(14.dp)) { Text("Simpan & uji") }
                OutlinedButton(onClick = { s.logout() }, shape = RoundedCornerShape(14.dp)) { Text("Keluar dari perangkat ini") }
            }
        }
        Text("Aplikasi versi ${s.version}", style = MaterialTheme.typography.labelSmall, color = Ink.Faint)
    }
}

@Composable
private fun Toggle(title: String, sub: String, on: Boolean, change: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f).padding(end = 12.dp)) {
            Text(title, style = MaterialTheme.typography.bodyLarge, color = Ink.Text)
            Text(sub, style = MaterialTheme.typography.bodySmall, color = Ink.Faint)
        }
        Switch(on, change)
    }
}

// ------------------------------------------------------------------ one task, in full

@Composable
private fun TaskDetail(s: AppState) {
    val t = s.openTask ?: return
    AuroraBackground(Modifier.fillMaxSize().pointerInput(Unit) { detectTapGestures { } }) {
        Column(Modifier.fillMaxSize().systemBarsPadding().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp, vertical = 12.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Heading("Rincian tugas", HomeLogic.stateLabel(t.state) + (HomeLogic.ago(t.updatedAt).let { if (it.isEmpty()) "" else " · $it" })) { s.closeTask() }
            GlassCard(Modifier.fillMaxWidth(), emphasis = "strong") {
                SelectionContainer { Text(t.objective, style = MaterialTheme.typography.bodyLarge, color = Ink.Text) }
                t.heardAs?.let { Text("Dipahami sebagai: $it", style = MaterialTheme.typography.bodySmall, color = Ink.Muted) }
                Spacer(Modifier.height(10.dp))
                Stats(listOf("Status" to HomeLogic.stateLabel(t.state), "Biaya" to "$" + ReplyView.fmt(t.costUsd), "Kelas data" to if (t.public) "publik" else "pribadi"))
            }
            if (t.state != "COMPLETED" && !HomeLogic.working(t.state)) HomeLogic.explain(t.reasonCode)?.let {
                GlassCard(Modifier.fillMaxWidth(), tint = Ink.Danger) { Text(it, style = MaterialTheme.typography.bodyMedium, color = Ink.Text) }
            }
            if (HomeLogic.working(t.state)) OutlinedButton(onClick = { s.cancel(t.id) }, shape = RoundedCornerShape(14.dp)) { Text("Batalkan tugas") }
            t.reply?.let { r ->
                ReplyBody(s, r, t.id)
                Pill("Jelaskan dengan suara", "🔊") { s.closeTask(); s.present(t) }
            }
            if (s.openEvents.isNotEmpty()) GlassCard(Modifier.fillMaxWidth(), emphasis = "quiet") {
                Text("Jejak kerja", style = MaterialTheme.typography.titleSmall, color = Ink.Muted)
                Spacer(Modifier.height(6.dp))
                for (e in s.openEvents) {
                    val words = HomeLogic.activity(e.type, runCatching { JSONObject(e.data) }.getOrElse { JSONObject() })
                    Row(Modifier.padding(vertical = 3.dp)) {
                        Text("${e.seq}", Modifier.width(34.dp), style = MaterialTheme.typography.labelSmall, color = Ink.Faint, fontFamily = FontFamily.Monospace)
                        Column {
                            Text(words ?: e.type, style = MaterialTheme.typography.bodySmall, color = Ink.Text)
                            if (words != null) Text(e.type, style = MaterialTheme.typography.labelSmall, color = Ink.Faint, fontFamily = FontFamily.Monospace)
                        }
                    }
                }
            }
            Spacer(Modifier.height(16.dp))
        }
    }
}

// ------------------------------------------------------------------ navigation

@Composable
private fun BottomBar(s: AppState) {
    val waiting = s.approvals.size + s.questions.size + s.notices.count { !it.read }
    Box(Modifier.fillMaxWidth()) {
        Row(
            Modifier.fillMaxWidth().padding(top = 14.dp).background(Brush.verticalGradient(listOf(Color.Transparent, Ink.Base.copy(alpha = 0.92f))))
                .padding(horizontal = 6.dp, vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Tab(s, "home", "Ruang kerja", Icons.Filled.Home, 0, Modifier.weight(1f))
            Tab(s, "chat", "Percakapan", Icons.AutoMirrored.Filled.List, 0, Modifier.weight(1f))
            Spacer(Modifier.weight(1f))
            Tab(s, "inbox", "Kotak masuk", Icons.Filled.Email, waiting, Modifier.weight(1f))
            Tab(s, "more", "Lainnya", Icons.Filled.Menu, 0, Modifier.weight(1f))
        }
        // The orb is the way to talk: one tap starts a live conversation.
        Orb(if (s.tasks.any { HomeLogic.working(it.state) }) OrbState.THINKING else OrbState.IDLE,
            Modifier.align(Alignment.TopCenter).clip(CircleShape).clickable { s.startTalk() }, size = 68.dp)
    }
}

@Composable
private fun Tab(s: AppState, id: String, label: String, icon: ImageVector, badge: Int, modifier: Modifier) {
    val on = s.tab == id
    val tint = if (on) LocalLook.current.palette.a else Ink.Faint
    Column(modifier.clip(RoundedCornerShape(14.dp)).clickable { s.go(id) }.padding(vertical = 6.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Box {
            Icon(icon, label, tint = tint, modifier = Modifier.size(24.dp))
            if (badge > 0) Text(if (badge > 9) "9+" else "$badge", Modifier.align(Alignment.TopEnd).offset(x = 10.dp, y = (-6).dp).clip(CircleShape)
                .background(Ink.Danger).padding(horizontal = 5.dp, vertical = 1.dp), fontSize = 10.sp, color = Color.White)
        }
        Text(label, style = MaterialTheme.typography.labelSmall, color = tint, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}
