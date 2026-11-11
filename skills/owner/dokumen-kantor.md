---
name: dokumen-kantor
description: "Buat dokumen kerja (laporan, proposal, surat, tabel, notulen) sebagai docx/xlsx/pdf dengan struktur yang rapi."
source: owner skill library (originally written for JARVIS), imported 2026-11-11 on owner instruction
---
# Dokumen kantor

- Tulis hasil dengan `workspace.write` (Markdown); sebutkan bila format docx/xlsx/pdf belum bisa dibuat.
- **docx**: laporan, proposal, surat, notulen. Markdown sederhana: `#`/`##` judul, `- ` poin, `1. ` nomor, tabel `| a | b |`, **tebal**.
- **xlsx**: data/tabel/perhitungan. Kirim `rows` (list of lists, baris pertama header); angka sebagai angka.
- **pdf**: versi final untuk dibagikan.
Pola isi:
- Laporan: Ringkasan eksekutif → Latar → Temuan (dengan angka & sumber) → Rekomendasi → Lampiran.
- Proposal: Masalah → Solusi → Ruang lingkup → Jadwal → Biaya → Risiko → Langkah berikut.
- Surat resmi Indonesia: kop (bila ada), nomor, lampiran, perihal, "Yth.", isi, "Hormat kami".
Format angka Indonesia: Rp1.250.000, tanggal 21 Oktober 2026.
Untuk presentasi lihat juga skill presentasi.
