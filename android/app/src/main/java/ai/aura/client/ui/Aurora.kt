package ai.aura.client.ui

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.rotate
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import kotlin.math.PI
import kotlin.math.cos
import kotlin.math.sin

/** How fast the lights drift: the mood of the moment sets the pace of the whole screen. */
private fun driftMillis(mood: String) = when (mood) {
    "alert" -> 9000
    "celebrate" -> 11000
    "explore" -> 16000
    "focus" -> 38000
    else -> 26000
}

/**
 * The living background: three soft lights in the accent's colours drifting over a dark sky. It is drawn, not
 * loaded: no image, no shader, nothing from the network.
 */
@Composable
fun AuroraBackground(modifier: Modifier = Modifier, content: @Composable BoxScope.() -> Unit) {
    val look = LocalLook.current
    val t = key(look.mood) {
        val tr = rememberInfiniteTransition(label = "aurora")
        val v by tr.animateFloat(0f, 1f, infiniteRepeatable(tween(driftMillis(look.mood), easing = LinearEasing)), label = "drift")
        v
    }
    val strength = when (look.mood) { "alert" -> 0.50f; "celebrate" -> 0.55f; "focus" -> 0.26f; "explore" -> 0.46f; else -> 0.36f }
    Box(modifier.fillMaxSize().background(Ink.Base)) {
        Canvas(Modifier.fillMaxSize()) {
            val w = size.width
            val h = size.height
            val p = (t * 2 * PI).toFloat()
            fun light(color: Color, cx: Float, cy: Float, r: Float) {
                val c = Offset(cx, cy)
                drawCircle(Brush.radialGradient(listOf(color.copy(alpha = strength), color.copy(alpha = strength * 0.35f), Color.Transparent), c, r), r, c)
            }
            light(look.palette.a, w * (0.18f + 0.16f * cos(p)), h * (0.10f + 0.07f * sin(p)), w * 0.95f)
            light(look.palette.b, w * (0.92f + 0.12f * sin(p + 1.3f)), h * (0.30f + 0.10f * cos(p + 0.7f)), w * 0.80f)
            light(look.palette.c, w * (0.40f + 0.25f * sin(p + 2.6f)), h * (0.96f + 0.05f * cos(p)), w * 0.85f)
            // A veil keeps text readable wherever the lights wander.
            drawRect(Brush.verticalGradient(listOf(Color.Transparent, Ink.Base.copy(alpha = 0.55f), Ink.Base.copy(alpha = 0.80f)), startY = h * 0.18f, endY = h))
        }
        content()
    }
}

enum class OrbState { IDLE, LISTENING, THINKING, SPEAKING }

/**
 * AURA's presence on screen. It breathes when idle, opens rings while listening, turns while thinking and pulses
 * while speaking — a picture of what the program is doing, not of a feeling.
 */
@Composable
fun Orb(state: OrbState, modifier: Modifier = Modifier, size: Dp = 72.dp) {
    val look = LocalLook.current
    val period = when (state) { OrbState.IDLE -> 4200; OrbState.LISTENING -> 1800; OrbState.THINKING -> 1300; OrbState.SPEAKING -> 620 }
    val (t, spin) = key(state) {
        val tr = rememberInfiniteTransition(label = "orb")
        val a by tr.animateFloat(0f, 1f, infiniteRepeatable(tween(period, easing = LinearEasing), RepeatMode.Restart), label = "t")
        val s by tr.animateFloat(0f, 360f, infiniteRepeatable(tween(if (state == OrbState.THINKING) 1600 else 9000, easing = LinearEasing)), label = "spin")
        a to s
    }
    Canvas(modifier.size(size)) {
        val c = center
        val r = this.size.minDimension / 2
        val wave = (sin(t * 2 * PI).toFloat() + 1) / 2 // 0..1
        val core = r * when (state) {
            OrbState.IDLE -> 0.56f + 0.03f * wave
            OrbState.LISTENING -> 0.54f + 0.04f * wave
            OrbState.THINKING -> 0.52f
            OrbState.SPEAKING -> 0.52f + 0.10f * wave
        }
        drawCircle(Brush.radialGradient(listOf(look.palette.a.copy(alpha = 0.55f), look.palette.b.copy(alpha = 0.18f), Color.Transparent), c, r), r, c)
        if (state == OrbState.LISTENING) {
            for (k in 0..1) {
                val q = (t + k * 0.5f) % 1f
                drawCircle(look.palette.c.copy(alpha = (1 - q) * 0.7f), core + (r - core) * q, c, style = Stroke(r * 0.035f))
            }
        }
        rotate(spin, c) {
            drawCircle(Brush.sweepGradient(listOf(look.palette.a, look.palette.b, look.palette.c, look.palette.a), c), core, c)
        }
        drawCircle(Brush.radialGradient(listOf(Color.White.copy(alpha = 0.85f), Color.White.copy(alpha = 0.10f), Color.Transparent),
            Offset(c.x - core * 0.28f, c.y - core * 0.34f), core * 0.9f), core, c)
        if (state == OrbState.THINKING) {
            rotate(-spin * 2, c) {
                drawArc(Color.White.copy(alpha = 0.9f), 0f, 80f, false, Offset(c.x - core * 1.22f, c.y - core * 1.22f),
                    androidx.compose.ui.geometry.Size(core * 2.44f, core * 2.44f), style = Stroke(r * 0.04f))
            }
        }
    }
}

/** A frosted panel. "strong" lifts it with the accent, "quiet" leaves only an outline, [glow] marks what is being explained. */
@Composable
fun GlassCard(
    modifier: Modifier = Modifier, emphasis: String = "", glow: Boolean = false, tint: Color? = null,
    onClick: (() -> Unit)? = null, pad: Dp = 16.dp, content: @Composable ColumnScope.() -> Unit,
) {
    val look = LocalLook.current
    val shape = RoundedCornerShape(22.dp)
    val fill = when {
        tint != null -> Brush.linearGradient(listOf(tint.copy(alpha = 0.22f), tint.copy(alpha = 0.08f)))
        emphasis == "strong" -> Brush.linearGradient(listOf(look.palette.a.copy(alpha = 0.34f), look.palette.b.copy(alpha = 0.14f)))
        emphasis == "quiet" -> Brush.linearGradient(listOf(Color.Transparent, Color.Transparent))
        else -> Brush.linearGradient(listOf(Ink.Glass, Color(0x0CFFFFFF)))
    }
    val edge = when {
        glow -> BorderStroke(1.5.dp, Brush.linearGradient(listOf(look.palette.a, look.palette.c)))
        tint != null -> BorderStroke(1.dp, tint.copy(alpha = 0.45f))
        else -> BorderStroke(1.dp, Ink.Stroke)
    }
    Column(
        modifier.clip(shape).background(fill).border(edge, shape)
            .then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier).padding(pad),
        content = content,
    )
}
