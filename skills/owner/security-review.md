---
name: security-review
description: "Tinjau kode/konfigurasi untuk celah keamanan (injeksi, auth, rahasia, akses) sebelum diusulkan."
source: owner skill library (originally written for JARVIS), imported 2026-11-11 on owner instruction
---
# Review keamanan

Periksa: input tak tervalidasi (SQL/command/path), auth & scope endpoint baru, rahasia di kode/log, SSRF (URL dari pengguna/web), izin file, dependensi baru (versi dipin, sumber tepercaya), data pribadi ke cloud (data privat hanya ke rute privat).
Temuan: tingkat (tinggi/sedang/rendah), skenario serangan konkret, perbaikan. Temuan tinggi harus diselesaikan sebelum perubahan diusulkan ke pemilik.
