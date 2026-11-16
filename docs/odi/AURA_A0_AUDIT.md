# AURA A0 — audit ulang baca-saja (16 November 2026)

Acuan: ODI AURA Engineering Implementation Blueprint v1.1 (§27: mulai dari A0; tanpa edit berkas, instalasi, migrasi, perubahan kredensial, restart, atau deploy). Tidak ada satu pun dilakukan. Label: **VERIFIED** (diamati langsung hari ini), **INFERRED** (disimpulkan dari kode/dokumen, belum diuji), **PROPOSED**, **UNKNOWN**. Tidak ada nilai rahasia yang dibaca atau dicetak (hanya nama).

## 1. Identitas dan runtime
| Hal | Temuan | Label |
|---|---|---|
| Repositori | `~/aura`, `firstboy219/aura` (privat), `main` @ `5573790`, bersih (0 berkas kotor), 98 commit | VERIFIED |
| Kode | Go Core: 106 berkas sumber + 76 berkas uji, 31 paket (terbesar: `engine` 18.757 baris, `tools` 4.116, `api` 3.200, `gateway` 2.152, `app` 2.102); skrip pekerja Python/JSON-RPC lewat stdio | VERIFIED |
| Dokumen | 16 dokumen inti + 68 ADR (hanya 12 "accepted", 55 "Proposed"); `DEVELOPMENT_ROADMAP.md` masih menulis "Current phase: P2" padahal fitur sampai P5+ sudah berjalan | VERIFIED |
| Basis data | SQLite WAL, migrasi `0001…0018`; tabel dari `tasks`, `task_steps`, `task_events`, `approvals`, `capability_grants`, `audit_events`, `checkpoints`, `secrets`, `providers`, `model_tiers`, `usage_records` sampai `memories`, `skills`, `triggers`, `members`, `app_accounts`, `web_owner/web_sessions` (55 tabel) | VERIFIED |
| Data hidup (baseline 14:38 UTC) | 214 skill, 318 memori, 5.798 tugas, 9.708 peristiwa audit, 13 members, 1 app_account | VERIFIED |
| API | `contracts/api/openapi.yaml`: 51 jalur `/api/v1/…` (tugas, persetujuan, peristiwa/stream, memori, skill, triggers, workflows, kesehatan/sumber daya, admin audit/usage, UI) | VERIFIED |
| Server | 4 vCPU / 15 GiB / 309 GB, tanpa GPU; CPU sempat jenuh (Chromium macet, sudah di-restart) | VERIFIED (ODI_PHASE0_AUDIT) |

## 2. Isolasi dan kontrol keamanan (docker inspect, hari ini)
| Kontainer | User | Rootfs | Cap | Jaringan | Batas | Mount |
|---|---|---|---|---|---|---|
| aura-core | `aura` | read-only | ALL dibuang, no-new-privileges | aura-net, aura-browser-net, aura-shell-net; hanya 127.0.0.1:7400 | 1 GB, 1 CPU, 256 PID | data (rw), config (ro), **/opt/aura/secrets (ro)**, 3 soket |
| aura-worker | 10002 | read-only | ALL dibuang | **tidak ada jaringan** | 768 MB, 64 PID | hanya soket |
| aura-browser | 10003 | read-only | ALL dibuang | jaringan **internal** (tanpa rute internet) | 1 GB, 512 PID | hanya soket |
| aura-shell | 10004 | read-only | ALL dibuang | jaringan **internal** | 2 GB, 512 PID | `/work` (rw) + **2 folder milik JARVIS (ro)** |
| aura-tunnel | 10001 | read-only | ALL dibuang | aura-net | 128 MB | token tunnel (ro) |
VERIFIED. Pekerja Python tidak punya jaringan, rahasia, maupun akses DB: sesuai Blueprint §4 dan §11 (AT-05 kemungkinan terpenuhi; belum dibuktikan oleh uji khusus).
- **Browser egress:** semua lalu lintas browser lewat proksi di Core dengan allowlist domain per sesi, port 80/443, penjaga SSRF (DNS di-resolve sekali dan IP yang diperiksa yang dihubungi). VERIFIED (kode `internal/egress`, komentar dan uji).
- **Brankas rahasia:** AES-256-GCM di tabel `secrets`; nama rahasia di DB: `gemini`, `openrouter`; file di `/opt/aura/secrets`: `master.key`, `openrouter_key`, `cloudflare_tunnel_token`. Kunci utama dipasang di kontainer Core yang sama dengan DB, jadi brankas melindungi salinan DB/cadangan, **bukan** kompromi Core. VERIFIED (struktur); kekuatan terhadap kompromi Core: INFERRED.
- **Audit:** `audit_events` dan `task_events` append-only lewat pemicu SQLite (UPDATE/DELETE ditolak). **Tidak ada rantai hash**; pengguna DB berhak dapat mencabut pemicu. VERIFIED. Pemisahan log operasional vs audit keamanan: sebagian (dua tabel), tanpa retensi independen. INFERRED.
- **Persetujuan:** tabel `approvals` mengikat `action_hash` + `params_json` + tool + target + `grant_id`; status `pending/approved/rejected/expired/consumed/invalidated`; konsumsi dicatat (`APPROVAL_CONSUMED`); API menolak hash tidak cocok dengan HTTP 409. 8 `consumed`, 6 `expired`, 8 `invalidated`. VERIFIED. Belum ada: kekuatan autentikasi penyetuju, nonce eksplisit, status `revoked`. VERIFIED (tidak ada kolom).
- **Grant:** 8.536 grant, semua kedaluwarsa/dicabut; hanya `token_sha256` disimpan (bukan token). VERIFIED.
- **Kebijakan:** tingkat A0 (otomatis), A1 (notifikasi, selalu ditolak), A2 (butuh persetujuan), A3 (terlarang). Blueprint memakai R0–R4. VERIFIED; pemetaan = keputusan Pemilik.
- **Mesin status tugas (skema):** `CREATED, PLANNING, RUNNING, WAITING_APPROVAL, WAITING_INPUT, WAITING_EXTERNAL…, PAUSED, COMPLETED, FAILED, CANCELLED, BUDGET_EXCEEDED`. Belum eksplisit: `VALIDATING, COGNITION_PENDING, POLICY_CHECK, VERIFYING, PARTIAL, EXPIRED, RECOVERY_REQUIRED`. VERIFIED.

## 3. Jalur kognisi dan kredensial (peta konsumen; hanya nama)
- **Tidak ada referensi NEXUS** di kode, docs, atau konfigurasi (grep 0 berkas). VERIFIED.
- Gateway model berada di Core (`internal/gateway`): penyedia `openrouter` dan `gemini` (kunci di brankas), tier `fast/standard/deep` dengan prioritas berantai (mis. deep: 5 model Gemini → 2 nemotron → 2 Claude lewat OpenRouter); kegagalan atau biaya memicu turun ke prioritas berikutnya. VERIFIED. Ini **bertentangan dengan Blueprint §7** (fallback hanya karena NEXUS mati/dinonaktifkan Pemilik) dan menjadi pekerjaan A4.
- Pemakaian 7 hari (9–16 Nov): 8.834 panggilan, biaya tercatat < $0,1. VERIFIED.
- Konsumen kunci: AURA memakai `openrouter_key` + `gemini`; JARVIS memiliki kunci terpisah (`OPENROUTER_API_KEY`, `OPENROUTER_KEY_CHAT`, `OPENROUTER_KEY_LATAR`, `GROQ_API_KEY`, `DEEPGRAM_API_KEY`, `QDRANT_API_KEY`, `N8N_API_KEY`, dll.). **Kunci OpenRouter AURA identik dengan `OPENROUTER_API_KEY` JARVIS** (VERIFIED lewat perbandingan hash terpotong 12 hex, nilai tidak pernah dicetak): baik file `/opt/aura/secrets/openrouter_key` maupun salinan di brankas AURA (`vault:openrouter`, yang dipakai provider; diperbarui 10 Nov) bernilai sama. `OPENROUTER_KEY_CHAT` dan `OPENROUTER_KEY_LATAR` JARVIS kosong. Konsekuensi: satu kunci dipakai bersama JARVIS+AURA, jadi batas laju/kredit dan pencabutan berlaku untuk keduanya. Pemilik meminta kunci disamakan (16 Nov); sudah sama, tidak ada perubahan. Tidak ada rotasi/pencabutan dilakukan.
- Egress Core: jaringan `aura-net` bukan internal, jadi Core dapat langsung ke internet (penyedia model, pencarian web, Telegram). Firewall host nonaktif. Allowlist tingkat jaringan untuk Core tidak ada. VERIFIED.
- `aura-shell` memasang dua folder JARVIS (`infra/data/dev/handoff` dan `dev-mirror`, read-only): kopling berkas lintas-platform yang perlu dicatat terhadap aturan isolasi (§2). VERIFIED.

## 4. Fitur Blueprint yang belum ada atau belum terbukti
| Bagian Blueprint | Status | Catatan |
|---|---|---|
| §7 NEXUS gateway, bukti kesehatan, otorisasi fallback, pencabutan saat pulih | **Belum ada** | tidak ada klien NEXUS, bukti kesehatan, atau `FallbackAuthorization` |
| §8 Risiko R0–R4, revokasi dinamis, penghentian darurat | Sebagian | ada A0–A3 dan `/system/stop`; revokasi per perangkat/policy belum |
| §9 Persetujuan terikat digest + replay | Sebagian besar | digest, konsumsi, kedaluwarsa ada; nonce/kekuatan autentikasi/revoked belum |
| §10 Registri alat bermanifes + tanda tangan | Sebagian | L2 tools terikat sha256 sumber; manifes penuh (tujuan jaringan, data class, tanda tangan) belum |
| §11 Protokol pekerja versi, pembatalan | Sebagian | JSON-RPC stdio ada (`internal/rpc`, `workerhost`); versi/pembatalan koperatif belum diverifikasi |
| §12 Peramban + pekerja desktop lokal + takeover | Peramban ada; desktop lokal **belum ada** | ada pengambilalihan peramban (P5a); tidak ada pairing perangkat |
| §13 Registri perangkat + pairing kunci publik | **Belum ada** | hanya `members`, `app_accounts`, sesi web |
| §14 Memori operasional dan peristiwa | Sebagian | `task_events` berurutan per tugas (`UNIQUE(task_id, seq)`); provenance dan kedaluwarsa memori belum diaudit |
| §15 Level verifikasi V0–V3, status jujur | UNKNOWN | belum diperiksa di kode |
| §16 Self-engineering L0–L3 | Sebagian | kotak pasir L2 + Lab ada; klasifikasi L0–L3 eksplisit belum |
| §17 Pemicu proaktif + jam tenang | Sebagian | `triggers` ada; kebijakan lewat-jadwal/tumpang tindih UNKNOWN |
| §20 Audit tahan rusak | Sebagian | append-only; tanpa rantai hash/penyimpanan independen |
| §22 Pemulihan | Cadangan ada, **pulih AURA belum diuji** ke kontainer AURA | integritas SQLite ok; RPO/RTO belum |
| §23 Uji AT-01…12 | Lihat §5 | |

## 5. Pemetaan katalog uji penerimaan (Lampiran C)
| AT | Status saat ini |
|---|---|
| AT-01 NEXUS-only | **Gagal secara desain** (tidak ada NEXUS; gateway memakai penyedia luar) |
| AT-02, AT-03 | Tidak berlaku sampai gerbang fallback dibangun |
| AT-04 approval binding | Sebagian: uji `approval_mismatch`/konsumsi ada (2 uji bernama); perubahan satu parameter, replay, kedaluwarsa belum seluruhnya terbukti. INFERRED |
| AT-05 isolasi pekerja | Konfigurasi terbukti (§2); uji otomatis khusus belum ditemukan. INFERRED |
| AT-06 idempotensi | `tasks.idempotency_key UNIQUE`; 2 uji bernama. INFERRED |
| AT-07 prompt injection | Tidak ada uji bernama; UNKNOWN |
| AT-08 revokasi | Tidak ada uji bernama; UNKNOWN |
| AT-09 status jujur | UNKNOWN |
| AT-10 pemulihan | 9 uji bernama crash/recover (6 berkas). INFERRED |
| AT-11 takeover desktop | Tidak berlaku (tidak ada pekerja desktop) |
| AT-12 restore | Pulih SQLite diverifikasi hanya `integrity_check` (belum diuji penuh) |
Kata kunci nama uji hanya perkiraan awal; peta uji sebenarnya dibuat di A1.

## 6. Baseline uji
- **Go:** `sudo ./dev test-go` (pelari resmi proyek, seluruh repo dipasang): **26 paket lulus, 0 gagal, exit 0** pada `main` @ `5573790` (16 Nov 2026). VERIFIED.
- Koreksi: catatan saya sebelumnya bahwa `TestOpenAPIConformance` dan `TestCrashRecovery` gagal tidak terbukti dengan pelari resmi (keduanya tidak gagal); percobaan awal saya hanya memasang `core/go` sehingga uji yang membaca `contracts/` tidak menemukan berkasnya.
- Python (`./dev test-py`) dan uji shell (`./dev test-shell`): belum dijalankan di A0. UNKNOWN.

## 7. Keputusan Pemilik yang dibutuhkan (Blueprint §29) — tidak ditebak
1. **Isolasi pekerja:** pekerja sudah tanpa jaringan/rahasia/DB, read-only, cap dibuang. Apakah itu cukup, atau perlu seccomp/AppArmor/Landlock/microVM (diuji sesuai dukungan host)?
2. **Kebijakan risiko:** persetujuan definisi R0–R4 dan pemetaannya dari A0–A3 sekarang; apakah ada kelas R3 yang pernah boleh didelegasikan.
3. **Pemulihan:** RPO/RTO, retensi cadangan, frekuensi uji pulih, kanal pemberitahuan insiden. (Off-site cadangan belum ada.)
4. **Fallback:** ambang kesehatan NEXUS, kode galat, jendela waktu, penyedia fallback, kelas data, anggaran per tugas, dan kapan butuh persetujuan Pemilik. Sampai NEXUS ada, rute penyedia luar yang sekarang tetap dipertahankan (keputusan Pemilik 16 Nov).
5. **Kredensial:** rencana migrasi kunci OpenRouter bersama hanya setelah semua konsumen terpetakan (JARVIS dan AURA).
6. **Desktop lokal:** OS yang didukung, UX pairing, kemampuan layar/input, indikator privasi.
7. **SQLite:** ambang skala terukur sebelum mempertimbangkan perubahan.
8. **Audit:** pilihan tahan rusak (rantai hash, penyimpanan independen) dan siapa yang boleh mengekspor.
9. **Folder JARVIS yang dipasang ke `aura-shell`:** dipertahankan sebagai handoff read-only atau diganti API?

## 8. Usulan lingkup A1 (PROPOSED; belum dikerjakan, butuh persetujuan Pemilik)
Tujuan A1: membekukan kontrak tanpa mengubah perilaku produksi.
- Dokumen: pemetaan status tugas sekarang → status kanonik Blueprint (lapisan pemetaan aditif), skema amplop peristiwa dan error, kontrak gateway NEXUS (memakai `ODI_CONTRACTS/`), semantik risiko/persetujuan, versi protokol pekerja.
- Uji kontrak dan uji penerimaan AT-01…12 sebagai **uji yang gagal dengan jelas** untuk fitur yang belum ada (tidak melemahkan uji); AT yang sudah terpenuhi mendapat uji khusus.
- Peta kode ke uji (menggantikan perkiraan nama uji).
- Rekonsiliasi dokumen (roadmap vs ADR; ADR 55 "Proposed" dimintakan keputusan).
- Tanpa perubahan skema, tanpa perubahan jalur panggilan model, tanpa deploy. Cabang khusus; AURA dideploy oleh saya hanya setelah Tuan menyetujui lingkup.
Urutan berikut (sesuai peta jalan Blueprint): A2 siklus hidup inti → A3 kebijakan dan grant → A4 gerbang NEXUS (tergantung NEXUS mini) → A5 pekerja/alat → A6 peramban/perangkat → A7 memori/peristiwa → A8 self-engineering → A9 pengerasan.
