---
name: analisis-data
description: "Analisis data/tabel (CSV/xlsx/hasil query): bersihkan, hitung, temukan pola, buat grafik & kesimpulan."
source: owner skill library (originally written for JARVIS), imported 2026-11-11 on owner instruction
---
# Analisis data

1. Pahami pertanyaan bisnisnya dulu; tulis metrik yang dicari.
2. Muat data dengan `data.read`, cek: jumlah baris, kolom kosong, duplikat, tipe tanggal/angka.
3. Hitung yang ditanya + 1–2 temuan tak terduga yang relevan. Sebutkan asumsi & data yang dibuang.
4. Semua angka dihitung ulang dengan `calc` (tepat), jangan dihitung di kepala.
5. Hasil: 3 temuan utama dengan angka, keterbatasan data, rekomendasi. Simpan tabel hasil ke xlsx bila diminta.
