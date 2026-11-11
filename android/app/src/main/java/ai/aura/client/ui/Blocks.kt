package ai.aura.client.ui

import ai.aura.client.Api
import ai.aura.client.HomeLogic
import ai.aura.client.Narration
import ai.aura.client.ReplyView
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.scaleIn
import androidx.compose.animation.slideInVertically
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * Draws AURA's blocks (ADR-0055, ADR-0060) with the app's own components. A block is data, never markup or code:
 * every string is shown as plain text, a link opens only when it is http(s), and an action or a form only proposes
 * a request that the owner sends. An unknown block type is skipped.
 */

/** Blocks in rows, appearing one after another: [reveal] is how many are on screen, [focus] the one being explained. */
@Composable
fun Blocks(
    blocks: List<Api.Block>, modifier: Modifier = Modifier, layout: String = "stack", reveal: Int = blocks.size, focus: Int = -1,
    onAction: (String) -> Unit, widget: (@Composable (Api.Block) -> Unit)? = null,
) {
    val placed = if (layout == "grid") blocks.map { if (it.span.isEmpty() && compact(it)) it.copy(span = "half") else it } else blocks
    Column(modifier, verticalArrangement = Arrangement.spacedBy(if (layout == "story") 18.dp else 12.dp)) {
        for (row in HomeLogic.rows(placed)) {
            AnimatedVisibility(
                visible = row.first() < reveal,
                enter = fadeIn(tween(420)) + slideInVertically(spring(dampingRatio = 0.78f, stiffness = 220f)) { it / 3 } +
                    expandVertically(tween(380, easing = FastOutSlowInEasing)) + scaleIn(tween(420), initialScale = 0.96f),
            ) {
                if (row.size == 1) {
                    BlockCard(placed[row[0]], row[0] == focus, Modifier.fillMaxWidth(), onAction, widget)
                } else {
                    Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        for (i in row) BlockCard(placed[i], i == focus, Modifier.weight(1f).fillMaxHeight(), onAction, widget)
                    }
                }
            }
        }
    }
}

private fun compact(b: Api.Block) = when (b.type) {
    "metrics" -> b.items.size <= 2
    "progress" -> b.items.size <= 2
    "quote", "callout" -> b.text.length <= 90
    else -> false
}

@Composable
private fun BlockCard(b: Api.Block, focused: Boolean, modifier: Modifier, onAction: (String) -> Unit, widget: (@Composable (Api.Block) -> Unit)?) {
    if (b.type == "widget") {
        if (widget != null) Box(modifier) { widget(b) }
        return
    }
    if (b.type !in KNOWN) return
    val tint = when (b.type) {
        "callout" -> toneColor(b.tone)
        "hero" -> if (b.tone.isNotEmpty()) toneColor(b.tone) else null
        else -> null
    }
    GlassCard(modifier, emphasis = if (b.type == "hero" && b.emphasis.isEmpty()) "strong" else b.emphasis, glow = focused, tint = tint) {
        if (b.type != "hero" && b.type != "quote" && (b.title.isNotEmpty() || b.icon.isNotEmpty())) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (b.icon.isNotEmpty()) Text(b.icon, fontSize = 18.sp, modifier = Modifier.padding(end = 8.dp))
                Text(b.title, style = MaterialTheme.typography.titleSmall, color = Ink.Muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            Spacer(Modifier.height(10.dp))
        }
        BlockBody(b, onAction)
    }
}

private val KNOWN = setOf("text", "callout", "metrics", "kv", "table", "list", "steps", "chart", "links", "code", "actions",
    "hero", "timeline", "progress", "compare", "quote", "form")

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun BlockBody(b: Api.Block, onAction: (String) -> Unit) {
    val look = LocalLook.current
    val type = MaterialTheme.typography
    when (b.type) {
        "text" -> SelectionContainer { Text(b.text, style = type.bodyLarge, color = Ink.Text) }
        "callout" -> Text(b.text, style = type.bodyLarge, color = Ink.Text)
        "hero" -> {
            if (b.icon.isNotEmpty()) Text(b.icon, fontSize = 40.sp, modifier = Modifier.padding(bottom = 6.dp))
            Text(b.title, style = type.headlineMedium, color = Ink.Text)
            if (b.text.isNotEmpty()) Text(b.text, style = type.bodyLarge, color = Ink.Text.copy(alpha = 0.82f), modifier = Modifier.padding(top = 6.dp))
        }
        "quote" -> {
            Text("“", fontSize = 44.sp, lineHeight = 30.sp, color = look.palette.a, fontWeight = FontWeight.Bold)
            Text(b.text, style = type.titleLarge, fontStyle = FontStyle.Italic, color = Ink.Text)
            if (b.title.isNotEmpty()) Text("— ${b.title}", style = type.bodySmall, color = Ink.Muted, modifier = Modifier.padding(top = 8.dp))
        }
        "code" -> SelectionContainer {
            Text(b.text, fontFamily = FontFamily.Monospace, fontSize = 12.5.sp, lineHeight = 18.sp, color = Color(0xFFD7E3FF),
                modifier = Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).background(Color(0x66000000))
                    .horizontalScroll(rememberScrollState()).padding(12.dp))
        }
        "metrics" -> FlowRow(horizontalArrangement = Arrangement.spacedBy(18.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            for (it in b.items) Column(Modifier.widthIn(min = 84.dp)) {
                Text(it.value, style = type.headlineSmall, color = Ink.Text, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(it.label, style = type.labelMedium, color = Ink.Muted)
                if (it.note.isNotEmpty()) Text(it.note, style = type.labelSmall, color = look.palette.c)
            }
        }
        "kv" -> Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            for (it in b.items) Row {
                Text(it.label, style = type.bodyMedium, color = Ink.Muted, modifier = Modifier.weight(0.42f))
                Column(Modifier.weight(0.58f)) {
                    Text(it.value, style = type.bodyMedium, color = Ink.Text, fontWeight = FontWeight.Medium)
                    if (it.note.isNotEmpty()) Text(it.note, style = type.labelSmall, color = Ink.Faint)
                }
            }
        }
        "list" -> Column(verticalArrangement = Arrangement.spacedBy(9.dp)) {
            for (it in b.items) Row {
                Box(Modifier.padding(top = 8.dp, end = 10.dp).size(6.dp).clip(CircleShape).background(look.palette.a))
                Column {
                    Text(it.label, style = type.bodyLarge, color = Ink.Text)
                    val more = listOf(it.value, it.note).filter { s -> s.isNotEmpty() }.joinToString(" · ")
                    if (more.isNotEmpty()) Text(more, style = type.bodySmall, color = Ink.Muted)
                }
            }
        }
        "steps" -> Column {
            b.items.forEachIndexed { i, it ->
                Row(Modifier.height(IntrinsicSize.Min)) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally, modifier = Modifier.fillMaxHeight()) {
                        Box(Modifier.size(26.dp).clip(CircleShape).background(Brush.linearGradient(listOf(look.palette.a, look.palette.b))), Alignment.Center) {
                            Text("${i + 1}", style = type.labelMedium, color = Ink.Base, fontWeight = FontWeight.Bold)
                        }
                        if (i < b.items.lastIndex) Box(Modifier.width(2.dp).weight(1f).background(Ink.Stroke))
                    }
                    Column(Modifier.padding(start = 12.dp, bottom = if (i < b.items.lastIndex) 14.dp else 0.dp)) {
                        Text(it.label, style = type.titleSmall, color = Ink.Text)
                        if (it.value.isNotEmpty()) Text(it.value, style = type.bodyMedium, color = Ink.Text.copy(alpha = 0.8f))
                        if (it.note.isNotEmpty()) Text(it.note, style = type.labelSmall, color = Ink.Faint)
                    }
                }
            }
        }
        "timeline" -> Column {
            b.items.forEachIndexed { i, it ->
                Row(Modifier.height(IntrinsicSize.Min)) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally, modifier = Modifier.fillMaxHeight().padding(top = 5.dp)) {
                        Box(Modifier.size(10.dp).clip(CircleShape).background(look.palette.c))
                        if (i < b.items.lastIndex) Box(Modifier.width(2.dp).weight(1f).background(Ink.Stroke))
                    }
                    Column(Modifier.padding(start = 12.dp, bottom = if (i < b.items.lastIndex) 14.dp else 0.dp)) {
                        Text(it.label, style = type.labelMedium, color = look.palette.c)
                        Text(it.value, style = type.bodyLarge, color = Ink.Text)
                        if (it.note.isNotEmpty()) Text(it.note, style = type.bodySmall, color = Ink.Muted)
                    }
                }
            }
        }
        "progress" -> Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
            for (it in b.items) {
                val share = ((it.number ?: 0.0) / 100.0).toFloat().coerceIn(0f, 1f)
                val shown = remember(it.label) { Animatable(0f) }
                LaunchedEffect(share) { shown.animateTo(share, tween(900, easing = FastOutSlowInEasing)) }
                Column {
                    Row {
                        Text(it.label, style = type.bodyMedium, color = Ink.Text, modifier = Modifier.weight(1f))
                        Text("${ReplyView.fmt(it.number ?: 0.0)}%", style = type.labelLarge, color = look.palette.a)
                    }
                    Box(Modifier.padding(top = 6.dp).fillMaxWidth().height(8.dp).clip(CircleShape).background(Ink.Stroke)) {
                        Box(Modifier.fillMaxWidth(shown.value).fillMaxHeight().clip(CircleShape)
                            .background(Brush.horizontalGradient(listOf(look.palette.a, look.palette.c))))
                    }
                    if (it.note.isNotEmpty()) Text(it.note, style = type.labelSmall, color = Ink.Faint, modifier = Modifier.padding(top = 3.dp))
                }
            }
        }
        "table" -> Grid(b.columns, b.rows, -1)
        "compare" -> {
            val best = b.columns.indexOfFirst { it.equals(b.text.trim(), ignoreCase = true) }
            Grid(listOf("") + b.columns, b.rows, if (best >= 0) best + 1 else -1)
            if (b.text.isNotEmpty()) Text("★ Rekomendasi: ${b.text}", style = type.labelLarge, color = look.palette.c, modifier = Modifier.padding(top = 10.dp))
        }
        "chart" -> Chart(b)
        "links" -> {
            val open = LocalUriHandler.current
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                for (it in b.items) {
                    val ok = it.url.startsWith("https://") || it.url.startsWith("http://")
                    Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(14.dp)).background(Ink.Glass)
                        .then(if (ok) Modifier.clickable { runCatching { open.openUri(it.url) } } else Modifier).padding(12.dp),
                        verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text(it.label.ifEmpty { it.url }, style = type.bodyMedium, color = Ink.Text, maxLines = 2, overflow = TextOverflow.Ellipsis)
                            Text(it.url, style = type.labelSmall, color = Ink.Faint, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                        if (ok) Text("↗", color = look.palette.a, fontSize = 18.sp, modifier = Modifier.padding(start = 8.dp))
                    }
                }
            }
        }
        "actions" -> FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            for (it in b.items) Pill(it.label.ifEmpty { it.value }) { onAction(it.value.ifEmpty { it.label }) }
        }
        "form" -> Form(b, onAction)
    }
}

/** A one-tap request. */
@Composable
fun Pill(label: String, icon: String = "", filled: Boolean = false, onClick: () -> Unit) {
    val look = LocalLook.current
    val shape = RoundedCornerShape(50)
    Row(
        Modifier.clip(shape)
            .background(if (filled) Brush.horizontalGradient(listOf(look.palette.a, look.palette.b)) else Brush.horizontalGradient(listOf(Ink.Glass, Ink.Glass)))
            .border(1.dp, if (filled) Color.Transparent else look.palette.a.copy(alpha = 0.55f), shape)
            .clickable(onClick = onClick).padding(horizontal = 14.dp, vertical = 9.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (icon.isNotEmpty()) Text(icon, fontSize = 15.sp, modifier = Modifier.padding(end = 6.dp))
        Text(label, style = MaterialTheme.typography.labelLarge, color = if (filled) Ink.Base else Ink.Text, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/** A table that scrolls sideways; [best] highlights one column (the recommended option of a comparison). */
@Composable
private fun Grid(columns: List<String>, rows: List<List<String>>, best: Int) {
    val look = LocalLook.current
    val n = maxOf(columns.size, rows.maxOfOrNull { it.size } ?: 0)
    if (n == 0) return
    Column(Modifier.horizontalScroll(rememberScrollState())) {
        @Composable
        fun line(cells: List<String>, head: Boolean) = Row(Modifier.height(IntrinsicSize.Min)) {
            for (i in 0 until n) {
                Text(cells.getOrElse(i) { "" },
                    style = if (head) MaterialTheme.typography.labelMedium else MaterialTheme.typography.bodyMedium,
                    color = if (head) Ink.Muted else Ink.Text, fontWeight = if (head || i == best) FontWeight.SemiBold else FontWeight.Normal,
                    modifier = Modifier.width(if (n <= 2) 150.dp else 122.dp).fillMaxHeight()
                        .background(if (i == best) look.palette.c.copy(alpha = 0.14f) else Color.Transparent).padding(horizontal = 8.dp, vertical = 7.dp))
            }
        }
        if (columns.any { it.isNotEmpty() }) {
            line(columns, true)
            Box(Modifier.width((if (n <= 2) 150 else 122).dp * n).height(1.dp).background(Ink.Stroke))
        }
        for (r in rows) line(r, false)
    }
}

private val SERIES = listOf(Color(0xFF8B7BFF), Color(0xFF22D3EE), Color(0xFF34D399), Color(0xFFFBBF24), Color(0xFFFB7185),
    Color(0xFFF0ABFC), Color(0xFF60A5FA), Color(0xFFA3E635))

/** A bar, line or pie chart that draws itself in. */
@Composable
private fun Chart(b: Api.Block) {
    val look = LocalLook.current
    val points = b.items.filter { it.number != null }
    if (points.isEmpty()) return
    val values = points.map { it.number!!.toFloat() }
    val grow = remember(b) { Animatable(0f) }
    LaunchedEffect(b) { grow.animateTo(1f, tween(1000, easing = FastOutSlowInEasing)) }
    val colors = listOf(look.palette.a, look.palette.b, look.palette.c) + SERIES
    Canvas(Modifier.fillMaxWidth().height(if (b.chart == "pie") 180.dp else 160.dp)) {
        val w = size.width
        val h = size.height
        val g = grow.value
        when (b.chart) {
            "pie" -> {
                val total = values.sumOf { maxOf(it, 0f).toDouble() }.toFloat().takeIf { it > 0 } ?: return@Canvas
                val d = minOf(w, h) * 0.92f
                val tl = Offset((w - d) / 2, (h - d) / 2)
                var start = -90f
                values.forEachIndexed { i, v ->
                    val sweep = maxOf(v, 0f) / total * 360f * g
                    drawArc(colors[i % colors.size], start, sweep, false, tl, Size(d, d), style = Stroke(d * 0.20f, cap = StrokeCap.Butt))
                    start += sweep
                }
            }
            "line" -> {
                val lo = minOf(values.min(), 0f)
                val span = (values.max() - lo).takeIf { it > 0 } ?: 1f
                val step = if (values.size > 1) w / (values.size - 1) else 0f
                val pts = values.mapIndexed { i, v -> Offset(if (values.size > 1) i * step else w / 2, h - 8 - (v - lo) / span * (h - 20) * g) }
                val path = Path().apply { pts.forEachIndexed { i, p -> if (i == 0) moveTo(p.x, p.y) else lineTo(p.x, p.y) } }
                val area = Path().apply { addPath(path); lineTo(pts.last().x, h); lineTo(pts.first().x, h); close() }
                drawPath(area, Brush.verticalGradient(listOf(look.palette.a.copy(alpha = 0.35f), Color.Transparent)))
                drawPath(path, Brush.horizontalGradient(listOf(look.palette.a, look.palette.c)), style = Stroke(5f, cap = StrokeCap.Round))
                pts.forEach { drawCircle(Color.White, 5f, it) }
            }
            else -> {
                val top = values.maxOf { maxOf(it, 0f) }.takeIf { it > 0 } ?: 1f
                val slot = w / values.size
                val bar = minOf(slot * 0.62f, 90f)
                values.forEachIndexed { i, v ->
                    val bh = maxOf(v, 0f) / top * (h - 6) * g
                    drawRoundRect(Brush.verticalGradient(listOf(colors[i % colors.size], colors[i % colors.size].copy(alpha = 0.45f)), startY = h - bh, endY = h),
                        Offset(i * slot + (slot - bar) / 2, h - bh), Size(bar, bh), androidx.compose.ui.geometry.CornerRadius(10f, 10f))
                }
            }
        }
    }
    Legend(points, colors, b.chart != "line")
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun Legend(points: List<Api.Item>, colors: List<Color>, dots: Boolean) {
    FlowRow(Modifier.padding(top = 10.dp), horizontalArrangement = Arrangement.spacedBy(14.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        points.forEachIndexed { i, p ->
            Row(verticalAlignment = Alignment.CenterVertically) {
                if (dots) Box(Modifier.padding(end = 6.dp).size(8.dp).clip(CircleShape).background(colors[i % colors.size]))
                Text("${p.label} ", style = MaterialTheme.typography.labelMedium, color = Ink.Muted)
                Text(ReplyView.fmt(p.number ?: 0.0), style = MaterialTheme.typography.labelMedium, color = Ink.Text)
            }
        }
    }
}

/** A form AURA asked the owner to fill: the answers go into the request it wrote, and the owner sends it. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun Form(b: Api.Block, onAction: (String) -> Unit) {
    val look = LocalLook.current
    val values = remember(b) { mutableStateMapOf<String, String>() }
    val fields = Narration.formFields(b.text)
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        for (name in fields) {
            val item = b.items.firstOrNull { it.label == name }
            val options = Narration.choices(item?.value.orEmpty())
            if (options.isEmpty()) {
                val note = item?.note.orEmpty()
                val hint: (@Composable () -> Unit)? = if (note.isEmpty()) null else { { Text(note) } }
                OutlinedTextField(values[name].orEmpty(), { values[name] = it.take(300) }, Modifier.fillMaxWidth(), label = { Text(name) },
                    supportingText = hint, shape = RoundedCornerShape(14.dp))
            } else {
                Column {
                    Text(name, style = MaterialTheme.typography.labelMedium, color = Ink.Muted, modifier = Modifier.padding(bottom = 6.dp))
                    FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        for (o in options) Pill(o, filled = values[name] == o) { values[name] = o }
                    }
                    item?.note?.takeIf { it.isNotEmpty() }?.let { Text(it, style = MaterialTheme.typography.labelSmall, color = Ink.Faint, modifier = Modifier.padding(top = 4.dp)) }
                }
            }
        }
        val request = Narration.fillForm(b.text, values)
        Box(Modifier.clip(RoundedCornerShape(50))
            .background(if (request != null) Brush.horizontalGradient(listOf(look.palette.a, look.palette.b)) else Brush.horizontalGradient(listOf(Ink.Stroke, Ink.Stroke)))
            .then(if (request != null) Modifier.clickable { onAction(request) } else Modifier).padding(horizontal = 22.dp, vertical = 11.dp)) {
            Text("Kirim ke AURA", style = MaterialTheme.typography.labelLarge, color = if (request != null) Ink.Base else Ink.Faint)
        }
    }
}
