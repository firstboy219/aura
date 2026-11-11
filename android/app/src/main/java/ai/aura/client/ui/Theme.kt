package ai.aura.client.ui

import ai.aura.client.Api
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.tween
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp

/** The three lights of an accent: they colour the aurora, the orb and every highlight. */
data class Palette(val a: Color, val b: Color, val c: Color)

/** The resolved look of the moment (ADR-0060): colours from the accent, motion from the mood, arrangement from the layout. */
data class Look(val palette: Palette = palette("aurora"), val mood: String = "calm", val layout: String = "stack")

object Ink {
    val Base = Color(0xFF060912)
    val Raised = Color(0xFF0E1424)
    val Text = Color(0xFFF3F6FB)
    val Muted = Color(0xFF9BA6BC)
    val Faint = Color(0xFF66708A)
    val Glass = Color(0x14FFFFFF)
    val Stroke = Color(0x24FFFFFF)
    val Success = Color(0xFF34D399)
    val Warning = Color(0xFFFBBF24)
    val Danger = Color(0xFFFB7185)
    val Info = Color(0xFF60A5FA)
}

fun palette(accent: String): Palette = when (accent) {
    "ocean" -> Palette(Color(0xFF38BDF8), Color(0xFF6366F1), Color(0xFF22D3EE))
    "emerald" -> Palette(Color(0xFF34D399), Color(0xFF14B8A6), Color(0xFFA3E635))
    "amber" -> Palette(Color(0xFFFBBF24), Color(0xFFFB923C), Color(0xFFF472B6))
    "rose" -> Palette(Color(0xFFFB7185), Color(0xFFE879F9), Color(0xFFA78BFA))
    "violet" -> Palette(Color(0xFFA78BFA), Color(0xFF7C3AED), Color(0xFFF0ABFC))
    else -> Palette(Color(0xFF8B7BFF), Color(0xFF22D3EE), Color(0xFF34D399)) // aurora
}

fun toneColor(tone: String): Color = when (tone) {
    "success" -> Ink.Success
    "warning" -> Ink.Warning
    "danger" -> Ink.Danger
    else -> Ink.Info
}

val LocalLook = compositionLocalOf { Look() }

private val Type = Typography(
    displaySmall = TextStyle(fontSize = 34.sp, lineHeight = 40.sp, fontWeight = FontWeight.Bold, letterSpacing = (-0.5).sp),
    headlineMedium = TextStyle(fontSize = 26.sp, lineHeight = 32.sp, fontWeight = FontWeight.Bold, letterSpacing = (-0.3).sp),
    headlineSmall = TextStyle(fontSize = 22.sp, lineHeight = 28.sp, fontWeight = FontWeight.SemiBold),
    titleLarge = TextStyle(fontSize = 19.sp, lineHeight = 25.sp, fontWeight = FontWeight.SemiBold),
    titleMedium = TextStyle(fontSize = 16.sp, lineHeight = 22.sp, fontWeight = FontWeight.SemiBold),
    titleSmall = TextStyle(fontSize = 14.sp, lineHeight = 20.sp, fontWeight = FontWeight.SemiBold),
    bodyLarge = TextStyle(fontSize = 16.sp, lineHeight = 24.sp),
    bodyMedium = TextStyle(fontSize = 14.sp, lineHeight = 21.sp),
    bodySmall = TextStyle(fontSize = 12.5.sp, lineHeight = 18.sp),
    labelLarge = TextStyle(fontSize = 14.sp, lineHeight = 18.sp, fontWeight = FontWeight.SemiBold),
    labelMedium = TextStyle(fontSize = 12.sp, lineHeight = 16.sp, fontWeight = FontWeight.Medium, letterSpacing = 0.4.sp),
    labelSmall = TextStyle(fontSize = 11.sp, lineHeight = 14.sp, fontWeight = FontWeight.Medium, letterSpacing = 0.6.sp),
)

/** The theme follows the scene: when the accent changes, every colour glides to the new one instead of jumping. */
@Composable
fun AuraTheme(scene: Api.Scene, content: @Composable () -> Unit) {
    val target = palette(scene.accent)
    val a by animateColorAsState(target.a, tween(1400), label = "a")
    val b by animateColorAsState(target.b, tween(1400), label = "b")
    val c by animateColorAsState(target.c, tween(1400), label = "c")
    val look = Look(Palette(a, b, c), scene.mood.ifEmpty { "calm" }, scene.layout.ifEmpty { "stack" })
    val colors = darkColorScheme(
        primary = a, onPrimary = Ink.Base, secondary = b, onSecondary = Ink.Base, tertiary = c, onTertiary = Ink.Base,
        background = Ink.Base, onBackground = Ink.Text, surface = Ink.Raised, onSurface = Ink.Text,
        surfaceVariant = Color(0xFF182035), onSurfaceVariant = Ink.Muted, surfaceContainer = Color(0xFF111829),
        surfaceContainerHigh = Color(0xFF161E32), surfaceContainerHighest = Color(0xFF1B243B), surfaceContainerLow = Color(0xFF0C1220),
        error = Ink.Danger, onError = Ink.Base, outline = Ink.Stroke, outlineVariant = Color(0x18FFFFFF),
        primaryContainer = a.copy(alpha = 0.22f), onPrimaryContainer = Ink.Text,
        secondaryContainer = b.copy(alpha = 0.22f), onSecondaryContainer = Ink.Text,
    )
    CompositionLocalProvider(LocalLook provides look) {
        MaterialTheme(colorScheme = colors, typography = Type, content = content)
    }
}
