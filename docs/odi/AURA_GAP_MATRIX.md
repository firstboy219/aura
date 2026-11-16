# AURA — matriks kesenjangan ringkas (A0, 16 Nov 2026)

Prioritas: P0 harus sebelum NEXUS dapat memegang kognisi; P1 sebelum produksi penuh; P2 berikutnya.

| # | Kesenjangan | Bukti | Prioritas | Fase |
|---|---|---|---|---|
| 1 | Tidak ada gerbang NEXUS dan kebijakan fallback AURA | grep NEXUS 0; gateway berantai OpenRouter/Gemini | P0 | A4 |
| 2 | Fallback model berantai karena galat/biaya (bertentangan dengan §7) | `model_tiers` prioritas 0–8 per tier | P0 | A4 (setelah NEXUS mini) |
| 3 | Core punya egress internet tanpa allowlist jaringan; firewall host nonaktif | `aura-net` internal=false; ufw inactive | P0 | A4/A9 |
| 4 | Kunci OpenRouter dipakai bersama JARVIS+AURA (identik, terverifikasi); konsumen lain di luar kedua repo belum dipetakan | hash sama | P0 | A1 (peta) |
| 5 | Audit tahan rusak: tanpa rantai hash/retensi independen | pemicu append-only saja | P1 | A3/A9 |
| 6 | Status tugas kanonik belum eksplisit (VALIDATING, POLICY_CHECK, VERIFYING, PARTIAL, EXPIRED, RECOVERY_REQUIRED) | skema `tasks.state` | P1 | A2 |
| 7 | Persetujuan: kekuatan autentikasi, nonce, status revoked | skema `approvals` | P1 | A3 |
| 8 | Registri perangkat dan pairing kunci publik tidak ada | tidak ada tabel/kode | P1 | A6 |
| 9 | Pulih AURA belum diuji ke kontainer AURA; RPO/RTO belum | N0 | P1 | A9 |
| 10 | Level verifikasi V0–V3 dan status jujur belum diaudit | UNKNOWN | P1 | A2 |
| 11 | Uji penerimaan AT-01…12 belum lengkap (injeksi, revokasi tanpa uji bernama) | pencarian nama uji | P1 | A1/A3 |
| 12 | Klasifikasi L0–L3 eksplisit untuk self-engineering | kode L2/Lab ada | P2 | A8 |
| 13 | Manifes alat penuh (tujuan jaringan, data class, tanda tangan) | L2 sha256 saja | P2 | A5 |
| 14 | Pekerja desktop lokal belum ada | tidak ada | P2 | A6 |
| 15 | Dokumen: roadmap P2 vs fitur P5+, 55 ADR "Proposed" | docs | P2 | A1 |
| 16 | Kopling folder JARVIS ke `aura-shell` | mounts | P2 | keputusan Pemilik |
