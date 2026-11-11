package ai.aura.client

import android.content.Context
import android.content.Intent
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.RectF
import android.graphics.Typeface
import android.net.Uri
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.HorizontalScrollView
import android.widget.LinearLayout
import android.widget.TableLayout
import android.widget.TableRow
import android.widget.TextView

/**
 * Draws AURA's reply (ADR-0055) with native widgets. The view is data, never markup: every string becomes plain text,
 * links open only when they are http(s), and an action only proposes a follow-up request that the owner sends.
 */
object ReplyView {
    private val palette = intArrayOf(0xFF1E88E5.toInt(), 0xFF43A047.toInt(), 0xFFFB8C00.toInt(), 0xFFE53935.toInt(),
        0xFF8E24AA.toInt(), 0xFF00ACC1.toInt(), 0xFFD81B60.toInt(), 0xFFC0CA33.toInt())

    fun noteText(n: Api.Note): String = when (n.code) {
        "model_knowledge" -> "Dijawab dari pengetahuan model, tanpa alat — belum diperiksa terhadap sumber."
        "core_summary" -> "Model penulis jawaban tidak tersedia; ini ringkasan apa adanya dari Core."
        "ungrounded_numbers" -> "Angka berikut tidak ditemukan di bukti tugas — periksa dulu: ${n.detail}"
        "voice_confirm" -> "AURA belum yakin maksud ucapan Tuan dan meminta konfirmasi; belum ada yang dikerjakan."
        "task_failed" -> "Tugas ini tidak selesai; isi balasan ini belum diverifikasi. Alasan: ${n.detail}"
        "view_trimmed" -> "Sebagian tampilan dibuang oleh Core karena tidak aman atau tidak sah."
        else -> n.code
    }

    fun build(ctx: Context, r: Api.Reply, onAction: (String) -> Unit): LinearLayout {
        val d = ctx.resources.displayMetrics.density
        fun dp(v: Int) = (v * d).toInt()
        fun text(s: String, size: Float = 15f, bold: Boolean = false, color: Int? = null) = TextView(ctx).apply {
            text = s
            textSize = size
            setTextIsSelectable(true)
            if (bold) setTypeface(typeface, Typeface.BOLD)
            color?.let { setTextColor(it) }
            setPadding(0, dp(3), 0, dp(3))
        }
        val box = LinearLayout(ctx).apply { orientation = LinearLayout.VERTICAL }
        box.addView(text(r.text, 16f))
        for (b in r.view) {
            val body: View? = when (b.type) {
                "text" -> text(b.text)
                "code" -> text(b.text, 13f).apply { typeface = Typeface.MONOSPACE }
                "callout" -> text(b.text).apply {
                    setPadding(dp(10), dp(8), dp(10), dp(8))
                    setBackgroundColor(when (b.tone) {
                        "success" -> 0x3343A047; "warning" -> 0x33FB8C00; "danger" -> 0x33E53935; else -> 0x331E88E5
                    })
                }
                "metrics" -> LinearLayout(ctx).apply {
                    orientation = LinearLayout.VERTICAL
                    b.items.forEach {
                        addView(text(it.label, 12f, color = Color.GRAY))
                        addView(text(it.value, 20f, bold = true))
                        if (it.note.isNotEmpty()) addView(text(it.note, 12f, color = Color.GRAY))
                    }
                }
                "kv" -> text(b.items.joinToString("\n") { it.label + ": " + it.value + if (it.note.isNotEmpty()) " — ${it.note}" else "" })
                "list" -> text(b.items.joinToString("\n") { "• " + it.label + (if (it.value.isNotEmpty()) ": ${it.value}" else "") + if (it.note.isNotEmpty()) "\n   ${it.note}" else "" })
                "steps" -> text(b.items.mapIndexed { i, it -> "${i + 1}. " + it.label + if (it.note.isNotEmpty()) "\n   ${it.note}" else "" }.joinToString("\n"))
                "table" -> HorizontalScrollView(ctx).apply {
                    addView(TableLayout(ctx).apply {
                        fun row(cells: List<String>, head: Boolean) = TableRow(ctx).apply {
                            cells.forEach { c -> addView(text(c, 13f, bold = head).apply { setPadding(0, dp(4), dp(14), dp(4)); maxWidth = dp(220) }) }
                        }
                        if (b.columns.isNotEmpty()) addView(row(b.columns, true))
                        b.rows.forEach { addView(row(it, false)) }
                    })
                }
                "chart" -> b.items.filter { it.number != null }.take(30).takeIf { it.size >= 2 }?.let { pts ->
                    LinearLayout(ctx).apply {
                        orientation = LinearLayout.VERTICAL
                        addView(Chart(ctx, b.chart, pts.map { it.number!!.toFloat() }), ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(180)))
                        pts.forEachIndexed { i, it ->
                            addView(text("■ ${it.label}: ${fmt(it.number!!)}", 12f, color = if (b.chart == "pie") palette[i % palette.size] else Color.GRAY))
                        }
                    }
                }
                "links" -> LinearLayout(ctx).apply {
                    orientation = LinearLayout.VERTICAL
                    b.items.filter { it.url.startsWith("https://") || it.url.startsWith("http://") }.forEach { it ->
                        addView(text("↗ " + it.label.ifEmpty { it.url }, color = palette[0]).apply {
                            setTextIsSelectable(false)
                            setOnClickListener { _ -> runCatching { ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(it.url))) } }
                        })
                    }
                }
                "actions" -> LinearLayout(ctx).apply {
                    orientation = LinearLayout.VERTICAL
                    b.items.filter { it.value.isNotBlank() }.forEach { it ->
                        addView(Button(ctx).apply { text = it.label; isAllCaps = false; setOnClickListener { _ -> onAction(it.value) } })
                    }
                }
                else -> null
            }
            if (body == null) continue
            if (b.title.isNotEmpty()) box.addView(text(b.title, 15f, bold = true).apply { setPadding(0, dp(12), 0, dp(2)) })
            box.addView(body)
        }
        if (r.files.isNotEmpty()) box.addView(text("File yang ditulis: " + r.files.joinToString(", "), 12f, color = Color.GRAY))
        r.notes.forEach { box.addView(text("ⓘ " + noteText(it), 12f, color = Color.rgb(230, 81, 0))) }
        return box
    }

    fun fmt(v: Double): String = if (v == Math.floor(v) && Math.abs(v) < 1e15) "%,d".format(v.toLong()) else "%,.2f".format(v)

    /** A small bar, line or pie chart drawn on a canvas. */
    private class Chart(ctx: Context, val kind: String, val values: List<Float>) : View(ctx) {
        private val paint = Paint(Paint.ANTI_ALIAS_FLAG)

        override fun onDraw(c: Canvas) {
            val w = width.toFloat()
            val h = height.toFloat()
            val pad = 8f * resources.displayMetrics.density
            if (kind == "pie") {
                val total = values.sumOf { maxOf(it, 0f).toDouble() }.toFloat()
                if (total <= 0f) return
                val r = minOf(w, h) / 2 - pad
                val box = RectF(w / 2 - r, h / 2 - r, w / 2 + r, h / 2 + r)
                var start = -90f
                values.forEachIndexed { i, v ->
                    val sweep = maxOf(v, 0f) / total * 360f
                    paint.style = Paint.Style.FILL
                    paint.color = palette[i % palette.size]
                    c.drawArc(box, start, sweep, true, paint)
                    start += sweep
                }
                return
            }
            val max = maxOf(values.max(), 0f)
            val min = minOf(values.min(), 0f)
            val span = if (max - min == 0f) 1f else max - min
            fun y(v: Float) = pad + (h - 2 * pad) * (1 - (v - min) / span)
            val slot = (w - 2 * pad) / values.size
            paint.color = Color.GRAY
            paint.strokeWidth = 1f
            c.drawLine(pad, y(0f), w - pad, y(0f), paint)
            paint.color = palette[0]
            values.forEachIndexed { i, v ->
                val cx = pad + slot * i + slot / 2
                if (kind == "line") {
                    paint.style = Paint.Style.FILL
                    c.drawCircle(cx, y(v), 3f * resources.displayMetrics.density, paint)
                    if (i > 0) {
                        paint.strokeWidth = 2f * resources.displayMetrics.density
                        c.drawLine(cx - slot, y(values[i - 1]), cx, y(v), paint)
                    }
                } else {
                    paint.style = Paint.Style.FILL
                    c.drawRect(cx - slot * 0.35f, minOf(y(v), y(0f)), cx + slot * 0.35f, maxOf(y(v), y(0f)) + 1f, paint)
                }
            }
        }
    }
}
