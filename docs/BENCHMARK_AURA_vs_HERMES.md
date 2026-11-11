# AURA vs Hermes (JARVIS) — uji banding 2026-11-11

Status bukti: **implementation-verified** untuk sisi AURA (11 tugas dijalankan, jawaban dicocokkan dengan kunci
jawaban yang diambil dari sumber yang sama); **Hermes tidak dapat dijalankan** pada lingkungan JARVIS saat uji,
sehingga tidak ada skor Hermes untuk tugas yang sama. Tidak ada angka yang diperkirakan.

## 1. Hermes tidak bisa berjalan di JARVIS saat ini
Diuji dengan arahan kerja asli JARVIS (`WORKER_BRIEF`) lewat klien Hermes milik JARVIS, satu pertanyaan sepele,
pada empat alias model milik pemilik. Semuanya gagal sebelum Hermes sempat bekerja:

| Alias | Hasil | Penyebab (dari log Hermes dan tabel `llm_usage`) |
|---|---|---|
| jarvis-web, jarvis-analisis, jarvis-cepat | gagal dalam 3 detik | permintaan Hermes ±24–37 ribu token ditolak Groq (HTTP 413, batas 8.000 token/menit) dan kunci OpenRouter ditolak (HTTP 401) |
| jarvis-produktivitas | gagal | batas panjang keluaran model |
| jarvis-latar-belakang | gagal | jendela konteks model 8.000 token, Hermes mensyaratkan minimal 64.000 |

Bukti produksi (hanya baca, `llm_usage` panggilan `caller=hermes`): 1–9 Nov, 25–527 panggilan sukses per hari;
**10 Nov: 5 sukses, 173 gagal; 11 Nov: 0 sukses, 176 gagal.** Penyebab utamanya adalah ketersediaan model, bukan
mutu Hermes; tetapi itu menunjukkan Hermes membutuhkan model besar berkonteks panjang (rata-rata prompt ±36 ribu
token per panggilan), sedangkan AURA berjalan pada model gratis karena promptnya kecil.

Tugas jalur Hermes di JARVIS, 10 hari terakhir (tabel `tasks`, kolom `runtime`): **132 selesai, 94 gagal,
2 dibatalkan (58% selesai)**. Semua tugas JARVIS pada jendela yang sama (termasuk jalur ringan dan tugas HP): 139
selesai, 94 gagal, 2 dibatalkan.

Efek samping uji pada JARVIS: beberapa baris panggilan gagal di `llm_usage` (tidak terpisah dari kegagalan harian
Hermes yang memang sedang terjadi) dan 5 sesi Hermes bernama `uji-…`. Tidak ada
memori, skill, jadwal, dokumen atau pesan HP yang dibuat. Konfigurasi JARVIS tidak diubah.

## 2. Hasil AURA (11 tugas, dijalankan berurutan, model gratis, biaya $0)
Kunci jawaban: CoinGecko, Open-Meteo, Kemenag via myquran, hitungan eksak, teks RFC 2606.

| Tugas | Hasil | Waktu | Catatan |
|---|---|---|---|
| T1 berita 5 headline + media + URL | headline asli dari RSS, media tercantum; **URL tidak ditampilkan** | 15 d | diperbaiki di 0.17.2 (aturan blok tautan) |
| T2 harga Bitcoin (IDR) | **benar**, persis kunci (Rp 1.342.719.911) | 39 d | |
| T3 cuaca Jakarta besok | **benar** (26–33°C, hujan 94% vs kunci 25,5–32,6; 94) | 33 d | |
| T4 Maghrib Jakarta | **benar** (17.51, kunci 17:51) | 18 d | |
| T5 hitung diskon + PPN | **benar** (Rp 2.142.855.000) | 69 d | menulis file yang tidak perlu |
| T6 baca RFC 2606 | **benar** (.test .example .invalid .localhost) | 36 d | |
| T7 3 kota terbesar menurut Wikipedia | **gagal** (POLICY_DENIED) | 174 d | pencarian Bing mengembalikan toko topi dan "Procaine"; alamat tebakan ditolak pagar |
| T8 produk fiktif | **jujur, tidak mengarang** ("tidak ada catatan resmi, kemungkinan fiktif"); status tugas FAILED | 198 d | verifikator menganggap "harga tidak ditemukan" sebagai gagal |
| T9 jalankan `uname -s` | **menolak dengan jujur** (tidak punya terminal) | 18 d | celah kemampuan, disengaja |
| T10 ibu kota Australia | **benar** (Canberra) | 15 d | |
| T11 harga emas Antam + URL | **gagal jujur**; menawarkan harga XAUT sebagai gantinya | 117 d | Bing mengembalikan hasil gigi "amalgam" |

Ringkasan: 6 benar penuh, 1 sebagian (T1), 2 jujur-tetapi-tidak-selesai (T8, T9), 2 gagal (T7, T11).
**Jawaban karangan: 0 dari 11.** Median waktu 36 detik.

### Uji ulang setelah perbaikan 0.17.2 (empat tugas yang lemah)
| Tugas | Sebelum | Sesudah |
|---|---|---|
| T1 berita + URL | tanpa URL | **benar**: 5 tautan artikel Republika/ANTARA, kelimanya dicek HTTP 200 dengan judul cocok (51 d) |
| T7 3 kota terbesar | gagal (toko topi, "Procaine") | **masih gagal**, tetapi sumbernya kini Wikipedia yang benar (daftar kabupaten/kota, Jakarta) dan tidak ada sampah; model tidak menemukan halaman daftar penduduk dan tidak mengekstrak angkanya (333 d) |
| T8 produk fiktif | FAILED | **selesai**: "data tidak tersedia", tanpa karangan (63 d) |
| T11 emas Antam | gagal (gigi "amalgam") | **masih gagal jujur**: hanya menemukan profil perusahaan di Wikipedia (222 d) |

Skor akhir AURA: **8 benar** (T1, T2, T3, T4, T5, T6, T8, T10), **1 menolak dengan jujur** karena memang tidak mampu
(T9), **2 gagal jujur** (T7, T11). **Karangan: 0 dari 11.**

## 3. Temuan dari uji (dan tindakannya)
1. Pencarian lewat Chromium tidak andal: Bing menjawab kata Indonesia dengan hasil tak nyambung. Membuatnya andal
   berarti menyamarkan browser untuk lolos pemeriksaan bot, yang tidak dilakukan AURA. Tindakan (0.17.2): hasil yang
   tidak nyambung ditolak dan jatuh ke Wikipedia (id+en); kalimat pencarian diminta pendek. Jalan dependable:
   kunci API pencarian (keputusan pemilik).
2. Pagar menolak alamat tebakan model dan tugas berhenti (POLICY_DENIED) alih-alih mencari ulang: rancangan
   keamanan, tetapi pemulihannya masih lemah.
3. Verifikator menandai "tidak ada datanya" sebagai gagal walau jawabannya benar dan jujur (T8).
4. Hermes membutuhkan model berkonteks ≥64 ribu dan throughput besar; di luar itu tidak bisa berjalan sama sekali.

## 4. Yang belum teruji
- Hermes pada tugas yang sama (butuh model besar yang tersedia di JARVIS; mengubah konfigurasi JARVIS tidak dilakukan).
- Serangan injeksi lewat halaman web, dokumen, pengingat, musik, kamera.
- Beban paralel dan uji ulang berulang untuk varians.
