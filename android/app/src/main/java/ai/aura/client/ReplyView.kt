package ai.aura.client

/** Plain words for the parts of a reply (ADR-0055) that every screen shows the same way. Pure logic, unit-tested. */
object ReplyView {
    fun noteText(n: Api.Note): String = when (n.code) {
        "model_knowledge" -> "Dijawab dari pengetahuan model, tanpa alat — belum diperiksa terhadap sumber."
        "core_summary" -> "Model penulis jawaban tidak tersedia; ini ringkasan apa adanya dari Core."
        "ungrounded_numbers" -> "Angka berikut tidak ditemukan di bukti tugas — periksa dulu: ${n.detail}"
        "voice_confirm" -> "AURA belum yakin maksud ucapan Tuan dan meminta konfirmasi; belum ada yang dikerjakan."
        "task_failed" -> "Tugas ini tidak selesai; isi balasan ini belum diverifikasi. Alasan: ${n.detail}"
        "view_trimmed" -> "Sebagian tampilan dibuang oleh Core karena tidak aman atau tidak sah."
        "screen_designed" -> "Tampilan baru sudah aktif. Tuan bisa mengembalikannya di Lainnya → Tampilan."
        else -> n.code
    }

    fun fmt(v: Double): String = if (v == Math.floor(v) && Math.abs(v) < 1e15) "%,d".format(v.toLong()) else "%,.2f".format(v)
}
