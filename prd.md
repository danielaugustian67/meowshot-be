# PRD: Meowshot

> "Instagram + Pokédex untuk kucing jalanan"
> Status: Draft v0.3 | Tanggal: 29 Oktober 2026

---

## 1. Overview

### 1.1 Problem Statement
Orang yang suka memotret kucing jalanan biasanya menyimpan fotonya di galeri HP, bercampur dengan foto lain. Tidak ada cara mudah untuk:
- Mengelompokkan foto per kucing (satu kucing = satu identitas)
- Mengingat di mana dan kapan kucing itu terakhir ditemui
- Memamerkan koleksi ke teman yang punya hobi serupa

### 1.2 Solusi
Aplikasi web (PWA) tempat user mencatat kucing yang ditemui sebagai **kartu kucing** dengan album foto dan riwayat lokasi, lalu membagikannya ke teman yang sudah saling terhubung (mutual friend) atau, jika dipilih, secara publik ke sesama pengguna.

### 1.3 Prinsip Produk
1. **Capture secepat mungkin**: kucing jalanan tidak mau menunggu. Foto → lokasi otomatis → simpan.
2. **Sederhana**: field minimal, detail bisa diisi belakangan.
3. **Privat secara default**: post baru hanya terlihat oleh teman. Publik harus dipilih sadar per post.
4. **Fun, bukan manajemen**: ini hobi, bukan app shelter.

---

## 2. Target User

**Fase awal**: pembuat app dan teman-teman dekatnya (sekitar 5–30 orang), semua saling kenal.

### Persona
**Dina, 25, pecinta kucing**
- Sering ketemu kucing di jalan dan memotretnya lewat HP
- Ingin tahu "kucing ini sudah pernah kufoto belum?" dan "terakhir ketemu di mana?"
- Senang memamerkan foto ke teman yang juga suka kucing

---

## 3. Goals dan Non-Goals

### Goals
- User bisa menyimpan kucing baru dalam kurang dari 10 detik sejak buka app
- User punya album per kucing dengan riwayat lokasi (sighting)
- User bisa berteman, melihat feed, dan berinteraksi (reaksi, komentar)
- Lokasi terkontrol: user menentukan seberapa presisi lokasi dibagikan

### Non-Goals (untuk v1)
- Bukan app manajemen shelter atau adopsi
- Tidak ada fitur follow atau akun terverifikasi: hubungan antar user tetap mutual friend; yang bisa dibuat publik hanya *post*
- Tidak ada akses konten publik tanpa login (v1)
- Tidak ada health log, status adopsi, atau jadwal vet
- Tidak ada database kucing global bersama (lihat Open Questions)
- Tidak ada monetisasi
- Tidak ada app native (PWA saja)

---

## 4. Konsep dan Istilah

| Istilah | Arti |
|---|---|
| **Cat** | Kartu kucing milik user: nama (opsional), catatan, kumpulan foto dan sighting |
| **Sighting** | Satu kejadian bertemu kucing: tanggal, waktu, lokasi |
| **Post** | Unggahan ke feed: foto + caption + kucing terkait + lokasi |
| **Friend** | Hubungan mutual (kirim request → diterima) |

### Keputusan: Kepemilikan Data Kucing
**Model A: koleksi pribadi per user.** Setiap user punya daftar kucing sendiri. Teman melihatnya lewat feed dan profil. Jika dua teman memotret kucing yang sama, masing-masing punya kartu sendiri.

Alasan: lebih sederhana, kepemilikan dan privasi data jelas, sesuai konsep "pamer koleksi".

---

## 5. User Stories

### Capture dan Koleksi
- Sebagai user, aku ingin memotret kucing dan lokasinya tercatat otomatis, supaya tidak perlu mengetik.
- Sebagai user, aku ingin menyimpan kucing tanpa nama dulu, lalu mengisi detail nanti.
- Sebagai user, aku ingin satu album per kucing, supaya foto-fotonya terkumpul rapi.
- Sebagai user, aku ingin mencatat setiap kali bertemu kucing yang sama, supaya tahu pola keberadaannya.
- Sebagai user, aku ingin melihat semua kucingku di peta.

### Sosial
- Sebagai user, aku ingin mencari teman lewat username dan mengirim friend request.
- Sebagai user, aku ingin melihat feed dari teman-temanku.
- Sebagai user, aku ingin memberi reaksi dan komentar di post teman.
- Sebagai user, aku ingin memilih siapa yang bisa melihat post-ku (publik, teman, atau hanya aku).
- Sebagai user, aku ingin menjelajahi post publik dari pengguna lain, supaya menemukan kucing dan orang baru.
- Sebagai user, aku ingin melaporkan post atau komentar yang tidak pantas.
- Sebagai user, aku ingin mengubah post publik menjadi teman-saja atau privat kapan saja.
- Sebagai user, aku ingin mengatur seberapa presisi lokasi yang dibagikan.
- Sebagai user, aku ingin memblokir atau menghapus teman.

### Profil
- Sebagai user, aku ingin profil berisi avatar, bio, dan grid koleksi kucingku.

---

## 6. Functional Requirements

Prioritas: **P0** = wajib MVP, **P1** = sebaiknya ada, **P2** = nice to have.

### 6.1 Auth dan Akun
| ID | Requirement | Prioritas |
|---|---|---|
| AUTH-1 | Registrasi dan login (email + password, atau Google OAuth) | P0 |
| AUTH-2 | Username unik, bisa diubah | P0 |
| AUTH-3 | Reset password | P1 |
| AUTH-4 | Hapus akun beserta seluruh datanya | P1 |
| AUTH-5 | Registrasi dengan invite link/kode (opsional, untuk membatasi pendaftar) | P2 |

### 6.2 Cat Card dan Album
| ID | Requirement | Prioritas |
|---|---|---|
| CAT-1 | Buat kucing: field `nama` (opsional) dan `catatan` (opsional) | P0 |
| CAT-2 | Upload banyak foto per kucing; foto pertama jadi sampul | P0 |
| CAT-3 | Ganti foto sampul, hapus foto, edit caption foto | P0 |
| CAT-4 | Edit dan hapus kucing | P0 |
| CAT-5 | Halaman daftar kucing (grid) dengan pencarian nama | P0 |
| CAT-6 | Filter/sort: terakhir dilihat, tanggal ditemukan, nama | P1 |
| CAT-7 | Tandai kucing favorit (pin di atas) | P2 |

### 6.3 Sighting dan Lokasi
| ID | Requirement | Prioritas |
|---|---|---|
| LOC-1 | Setiap kucing punya riwayat sighting (tanggal + koordinat + label teks) | P0 |
| LOC-2 | Ambil GPS otomatis dari EXIF foto; fallback ke "pakai lokasi saat ini"; fallback ke pilih manual di peta | P0 |
| LOC-3 | Label lokasi teks bebas (misal "Depan Indomaret Jl. Melati") | P0 |
| LOC-4 | Tambah sighting baru ke kucing yang sudah ada ("ketemu lagi") | P0 |
| LOC-5 | Peta berisi semua kucing milik user (marker + cluster) | P0 |
| LOC-6 | Tampilkan "terakhir dilihat: X hari lalu, di Y" di kartu kucing | P1 |
| LOC-7 | Saran kucing yang mirip saat upload, berdasarkan sighting terdekat ("apakah ini si Oyen?") | P1 |
| LOC-8 | Peta teman: lihat kucing teman yang lokasinya dibagikan | P2 |

### 6.4 Quick Capture
| ID | Requirement | Prioritas |
|---|---|---|
| QC-1 | Tombol kamera utama di layar depan; foto langsung tersimpan sebagai kucing baru tanpa nama | P0 |
| QC-2 | Setelah capture, tawarkan "kucing baru" atau "tambah ke kucing yang ada" | P0 |
| QC-3 | Antrean upload offline: foto tersimpan lokal dan disinkronkan saat online | P1 |

### 6.5 Friend System
| ID | Requirement | Prioritas |
|---|---|---|
| FR-1 | Cari user berdasarkan username | P0 |
| FR-2 | Kirim, terima, dan tolak friend request | P0 |
| FR-3 | Daftar teman | P0 |
| FR-4 | Hapus teman | P0 |
| FR-5 | Blokir user (tidak bisa melihat atau mengirim request) | P1 |
| FR-6 | Invite link untuk mengajak teman baru | P2 |

### 6.6 Post dan Feed
| ID | Requirement | Prioritas |
|---|---|---|
| POST-1 | Buat post: pilih kucing, 1–10 foto, caption, visibilitas | P0 |
| POST-2 | Visibilitas: `FRIENDS` (default) atau `PRIVATE` | P0 |
| POST-2b | Visibilitas `PUBLIC` (opt-in per post); lihat bagian 6.10 | P0 |
| POST-3 | Presisi lokasi per post: `EXACT` / `APPROX` (±200 m) / `HIDDEN`. Berlaku sama untuk semua level visibilitas, termasuk `PUBLIC` | P0 |
| POST-4 | Feed dari teman + diri sendiri, urut waktu, cursor pagination | P0 |
| POST-5 | Edit caption dan hapus post | P0 |
| POST-6 | Reaksi emoji (satu per user per post) | P0 |
| POST-7 | Komentar (tambah, hapus milik sendiri; pemilik post bisa hapus komentar di postnya) | P0 |
| POST-8 | Halaman detail post | P0 |

### 6.7 Profil
| ID | Requirement | Prioritas |
|---|---|---|
| PROF-1 | Profil: avatar, username, bio, jumlah kucing | P0 |
| PROF-2 | Grid kucing/post di profil, dengan aturan visibilitas | P0 |
| PROF-3 | Edit profil | P0 |

### 6.8 Notifikasi
| ID | Requirement | Prioritas |
|---|---|---|
| NOTIF-1 | Notifikasi in-app: friend request, request diterima, reaksi, komentar | P1 |
| NOTIF-2 | Web push (PWA) | P2 |

### 6.9 Gamifikasi (Fun)
| ID | Requirement | Prioritas |
|---|---|---|
| FUN-1 | Badge: "Penemu pertama", "10 kucing", "Ketemu kucing yang sama 5x" | P2 |
| FUN-2 | Leaderboard antar teman (bulanan) | P2 |
| FUN-3 | Statistik pribadi: total kucing, area terbanyak | P2 |
| FUN-4 | Memori "On this day" | P2 |
| FUN-5 | Reminder "sudah 2 minggu tidak ketemu si Belang" | P2 |

### 6.10 Publik, Explore, dan Moderasi
| ID | Requirement | Prioritas |
|---|---|---|
| PUB-1 | Opsi visibilitas `PUBLIC` per post, dipilih sadar oleh user | P0 |
| PUB-2 | Lokasi post publik mengikuti pilihan presisi user; `EXACT` diizinkan dan menjadi default (lokasi = tempat foto diambil). User tetap bisa memilih `APPROX`/`HIDDEN` per post | P0 |
| PUB-3 | Halaman Explore: feed post publik terbaru (cursor pagination), terpisah dari feed teman | P0 |
| PUB-4 | Semua konten hanya bisa diakses user yang sudah register/login; tanpa login tidak ada akses | P0 |
| PUB-5 | Profil user bisa dibuka user terdaftar lain; non-teman hanya melihat post `PUBLIC` | P0 |
| PUB-6 | Reaksi dan komentar pada post publik oleh user login | P0 |
| PUB-7 | Pemilik bisa menonaktifkan komentar pada post publik | P2 |
| PUB-8 | Share link / preview link untuk post publik tanpa login | P2 (ditunda) |
| MOD-1 | Laporkan post, komentar, atau user (dengan alasan) | P0 |
| MOD-2 | Blokir berlaku dua arah: konten saling tersembunyi, termasuk di Explore | P0 |
| MOD-3 | Pemilik bisa mengubah visibilitas dan presisi lokasi post kapan saja (`PUBLIC` ↔ `FRIENDS`/`PRIVATE`) | P0 |
| MOD-4 | Panel admin sederhana: antrean laporan, hapus konten, suspend user | P0 |
| MOD-5 | Rate limit untuk Explore, komentar, dan report | P0 |

**Catatan**: tidak ada konsep "teman dari teman". Visibilitas hanya `PUBLIC` (semua user login), `FRIENDS` (teman langsung), atau `PRIVATE` (pemilik).

---

## 7. Aturan Visibilitas dan Privasi

### Matriks Akses
| Viewer | Post `PUBLIC` | Post `FRIENDS` | Post `PRIVATE` | Kucing/foto yang belum diposting |
|---|---|---|---|---|
| Pemilik | Lihat | Lihat | Lihat | Lihat |
| Teman (ACCEPTED) | Lihat | Lihat | Tidak | Tidak |
| User login, bukan teman | Lihat | Tidak | Tidak | Tidak |
| Belum login | Tidak | Tidak | Tidak | Tidak |
| User yang diblokir (dua arah) | Tidak | Tidak | Tidak | Tidak |

### Aturan Lokasi
- `EXACT`: koordinat asli
- `APPROX`: koordinat dibulatkan/di-jitter ke radius sekitar 200 m
- `HIDDEN`: tidak ada koordinat sama sekali, hanya label teks jika user mengisinya
- **Pengaburan dilakukan di server**, bukan di client. API tidak pernah mengirim koordinat asli ke viewer yang tidak berhak.
- Koordinat asli milik user sendiri tetap tersimpan dan terlihat hanya oleh user itu.
- **Post `PUBLIC`**: aturan presisi sama seperti level lain. `EXACT` diizinkan dan menjadi default, karena lokasi hanya menunjukkan tempat foto diambil. User tetap bisa memilih `APPROX`/`HIDDEN` per post dan mengubahnya kapan saja.
- Pengaburan `APPROX` memakai jitter yang **deterministik per post** (misal berbasis hash `postId` + secret), supaya refresh berulang tidak bisa dirata-ratakan untuk menebak titik aslinya.
- Saat user memilih `EXACT` untuk post publik, tampilkan pratinjau lokasi di peta dan satu baris peringatan ("Lokasi ini akan terlihat oleh semua pengguna"), supaya foto yang diambil di dekat rumah tidak terposting tanpa disadari.

### Aturan Foto
- **EXIF dihapus** dari semua foto yang disimpan/ditampilkan. GPS diekstrak sekali saat upload untuk keperluan sighting, lalu metadata dibuang.
- Foto post `FRIENDS`/`PRIVATE` disajikan lewat **signed URL berumur pendek**, bukan URL publik permanen.
- Foto post `PUBLIC` juga lewat signed URL yang hanya diterbitkan untuk user yang login (sesuai keputusan "publik = user login"). Jika nanti ada akses tanpa login, EXIF tetap wajib sudah dihapus.
- Saat post `PUBLIC` diubah kembali ke `FRIENDS`/`PRIVATE`, URL lama harus langsung tidak valid (signed URL berumur pendek memudahkan ini).

---

## 8. Non-Functional Requirements

| Area | Requirement |
|---|---|
| **Performa** | Waktu dari buka app ke foto tersimpan < 10 detik; feed pertama tampil < 2 detik pada koneksi 4G |
| **Upload** | Kompres di client sebelum kirim (target < 1 MB per foto); server membuat thumbnail dan versi display (WebP) |
| **Format foto** | Dukung JPEG, PNG, WebP, dan HEIC (konversi di server) |
| **Offline** | PWA bisa di-install; capture bisa dilakukan offline dan disinkronkan nanti (P1) |
| **Keamanan** | Hash password dengan argon2/bcrypt; rate limit di login, register, friend request, Explore, komentar, dan report; validasi tipe dan ukuran file; cek otorisasi di setiap endpoint |
| **Otorisasi** | Satu fungsi terpusat `canView(viewerId, resource)` untuk mencegah IDOR; tidak diulang manual di tiap handler |
| **Skala** | Target awal puluhan user dan ribuan foto; satu server kecil cukup |
| **Backup** | Backup database terjadwal; foto di object storage dengan versioning atau replikasi |
| **Aksesibilitas** | Alt text untuk foto, kontras warna memadai, bisa dipakai satu tangan di HP |
| **Kompatibilitas** | Chrome dan Safari mobile terbaru (kamera + geolocation) |

---

## 9. Data Model (Draft)

```prisma
model User {
  id           Int      @id @default(autoincrement())
  username     String   @unique
  email        String   @unique
  passwordHash String
  avatarUrl    String?
  bio          String?
  createdAt    DateTime @default(now())
  cats         Cat[]
  posts        Post[]
}

model Friendship {
  id          Int          @id @default(autoincrement())
  requesterId Int
  addresseeId Int
  status      FriendStatus @default(PENDING)
  createdAt   DateTime     @default(now())
  @@unique([requesterId, addresseeId])
  @@index([addresseeId, status])
}

model Cat {
  id        Int        @id @default(autoincrement())
  userId    Int
  name      String?
  notes     String?
  coverPhotoId Int?
  createdAt DateTime   @default(now())
  user      User       @relation(fields: [userId], references: [id], onDelete: Cascade)
  photos    Photo[]
  sightings Sighting[]
  posts     Post[]
  @@index([userId, createdAt])
}

model Photo {
  id       Int       @id @default(autoincrement())
  catId    Int
  postId   Int?
  key      String    // key di object storage, bukan URL publik
  thumbKey String
  caption  String?
  takenAt  DateTime?
  cat      Cat       @relation(fields: [catId], references: [id], onDelete: Cascade)
  post     Post?     @relation(fields: [postId], references: [id], onDelete: SetNull)
}

model Sighting {
  id     Int      @id @default(autoincrement())
  catId  Int
  lat    Float
  lng    Float
  label  String?
  seenAt DateTime @default(now())
  cat    Cat      @relation(fields: [catId], references: [id], onDelete: Cascade)
  @@index([catId, seenAt])
}

model Post {
  id           Int          @id @default(autoincrement())
  userId       Int
  catId        Int
  caption      String?
  visibility   Visibility   @default(FRIENDS)
  locPrecision LocPrecision @default(APPROX)
  sightingId   Int?
  createdAt    DateTime     @default(now())
  user         User         @relation(fields: [userId], references: [id], onDelete: Cascade)
  cat          Cat          @relation(fields: [catId], references: [id], onDelete: Cascade)
  photos       Photo[]
  reactions    Reaction[]
  comments     Comment[]
  @@index([userId, createdAt])
  @@index([visibility, createdAt]) // untuk feed Explore
}

model Reaction {
  postId Int
  userId Int
  emoji  String
  @@id([postId, userId])
}

model Comment {
  id        Int      @id @default(autoincrement())
  postId    Int
  userId    Int
  body      String
  createdAt DateTime @default(now())
  @@index([postId, createdAt])
}

model Notification {
  id        Int      @id @default(autoincrement())
  userId    Int
  type      String   // friend_request, friend_accepted, reaction, comment
  refId     Int?
  readAt    DateTime?
  createdAt DateTime @default(now())
  @@index([userId, readAt])
}

model Report {
  id         Int          @id @default(autoincrement())
  reporterId Int
  targetType ReportTarget // POST, COMMENT, USER
  targetId   Int
  reason     String
  status     ReportStatus @default(OPEN)
  createdAt  DateTime     @default(now())
  @@index([status, createdAt])
}

enum FriendStatus { PENDING ACCEPTED BLOCKED }
enum ReportTarget { POST COMMENT USER }
enum ReportStatus { OPEN RESOLVED DISMISSED }
enum Visibility   { PRIVATE FRIENDS PUBLIC }
enum LocPrecision { EXACT APPROX HIDDEN }
```

**Catatan**
- Friendship: simpan satu baris per pasangan dan cek dua arah saat query (atau normalisasi `(min_id, max_id)`) agar tidak ada duplikat A→B dan B→A.
- Jika nanti perlu query radius ("kucing dalam 1 km"), pertimbangkan PostGIS.

---

## 10. Arsitektur dan Tech Stack (Usulan)

| Layer | Pilihan | Catatan |
|---|---|---|
| Backend | Node.js + Express + Prisma | Alternatif: Go atau Laravel, sesuai preferensi |
| Database | PostgreSQL | Opsional PostGIS |
| Storage foto | Cloudflare R2 / S3 (MinIO untuk dev) | Signed URL untuk foto privat |
| Image processing | `sharp` | Resize, thumbnail, strip EXIF, konversi HEIC |
| Frontend | PWA (React atau vanilla JS) + Leaflet + OpenStreetMap | Service worker untuk offline queue |
| Client utils | `exifr` (baca GPS), `browser-image-compression` | |
| Auth | Session cookie (httpOnly) atau JWT | argon2 untuk hash |
| Deploy | Docker + satu VPS kecil | Reverse proxy dengan HTTPS (wajib untuk kamera/geolocation) |

---

## 11. User Flow Utama

### Flow 1: Quick Capture
1. Buka app → tap tombol kamera
2. Ambil foto
3. App membaca GPS (EXIF → geolocation → manual)
4. Pilih: **Kucing baru** atau **Tambah ke kucing yang ada** (disarankan kucing terdekat)
5. Simpan (nama dan catatan opsional)
6. Opsional: "Bagikan?" → pilih visibilitas (`PUBLIC` / `FRIENDS` / `PRIVATE`) dan presisi lokasi → post.
### Flow 2: Berteman
1. Cari username → kirim request
2. Penerima mendapat notifikasi → terima
3. Keduanya saling melihat post berstatus `FRIENDS`

### Flow 3: Melihat Feed
1. Buka feed → scroll post teman
2. Tap foto → detail post
3. Beri reaksi atau komentar

---

## 12. Success Metrics

Karena ini app untuk lingkaran kecil, metrik bersifat kualitatif dan ringan.

| Metrik | Target |
|---|---|
| Waktu capture (buka app → tersimpan) | < 10 detik |
| Teman yang aktif dalam 30 hari pertama | ≥ 70% dari yang diundang |
| Kucing tercatat per user aktif per bulan | ≥ 3 |
| Persentase sighting dengan lokasi terisi | ≥ 90% |
| Insiden koordinat/foto bocor melebihi visibilitas dan presisi yang dipilih user | 0 |
| Waktu respons terhadap laporan | < 48 jam |

---

## 13. Risiko

| Risiko | Dampak | Mitigasi |
|---|---|---|
| Kebocoran lokasi lewat EXIF atau API | Tinggi (privasi, kucing bisa dicelakai) | Strip EXIF, pengaburan di server, tes otorisasi |
| IDOR pada endpoint post/foto/kucing | Tinggi | `canView` terpusat, integration test per endpoint |
| Biaya storage membengkak | Sedang | Kompres foto, batasi ukuran, lifecycle rule |
| Foto HP besar dan koneksi lambat | Sedang | Kompres di client, offline queue |
| EXIF GPS hilang (foto dari WhatsApp) | Sedang | Fallback geolocation dan peta manual |
| Foto publik yang diambil di dekat rumah user membocorkan alamat user | Sedang-Tinggi | Pratinjau peta + peringatan saat memilih `EXACT`, pilihan `APPROX`/`HIDDEN` per post, bisa diubah setelah post |
| Koordinat kucing di post publik disalahgunakan (kucing dicelakai) | Sedang | Semua akses wajib login (jejak akun), report + suspend user, rate limit Explore |
| Konten tidak pantas / spam / pelecehan di area publik | Sedang | Report, blokir dua arah, panel admin, rate limit (semua masuk MVP) |
| Scraping konten publik | Sedang | Wajib login, rate limit Explore, signed URL |
| Beban moderasi di luar kapasitas pembuat app | Sedang | Report, blokir, dan panel admin masuk MVP karena publik dari awal; tentukan siapa moderatornya sebelum rilis |
| Duplikat kartu kucing | Rendah | Saran kucing mirip berdasarkan lokasi (LOC-7) |

---

## 14. Roadmap

| Fase | Isi | Hasil |
|---|---|---|
| **1. Fondasi** | Auth, profil, CRUD kucing, upload foto + thumbnail, strip EXIF | Bisa menyimpan koleksi pribadi |
| **2. Lokasi** | Sighting, EXIF GPS auto-fill, peta Leaflet, quick capture | Koleksi berbasis lokasi |
| **3. Sosial dan publik** | Friend system, post, feed teman, visibilitas `PUBLIC`/`FRIENDS`/`PRIVATE`, kontrol presisi lokasi, Explore | Bisa pamer ke teman dan publik |
| **4. Interaksi dan moderasi** | Reaksi, komentar, notifikasi in-app, report, blokir dua arah, panel admin, rate limit | Terasa seperti sosmed dan aman dirilis |
| **5. Polish** | PWA install, offline queue, saran kucing mirip, statistik | Nyaman dipakai di lapangan |
| **6. Fun** | Badge, leaderboard, "on this day", reminder, web push | Retensi |

> Karena publik ada dari awal, fase 3 dan 4 dirilis sebagai satu paket: Explore tidak dibuka ke user sebelum report, blokir, dan panel admin siap.

---

## 15. Open Questions

1. ~~Nama app?~~ **Diputuskan**: Meowshot. Perlu dicek ketersediaan domain dan username sosmed.
2. **Model data kucing**: tetap model A (pribadi per user), atau nanti ada fitur "tandai kucing ini sama dengan punya Budi"?
3. **Auth**: email + password atau Google OAuth saja? Perlu invite code untuk membatasi pendaftar?
4. **Hosting dan budget**: VPS apa, dan batas biaya storage per bulan?
5. **Notifikasi**: cukup in-app, atau perlu web push sejak awal? (Catatan: web push di iOS mensyaratkan app di-install ke Home Screen.)
6. **Reaksi**: satu emoji (like) atau beberapa pilihan?
7. **Batas ukuran**: maksimum foto per post dan per kucing?
8. **Bahasa UI**: Indonesia saja, atau dwibahasa?
9. **Data export**: perlu fitur unduh seluruh foto dan data (backup pribadi)?
10. ~~Publik tanpa login?~~ **Diputuskan**: semua user wajib register/login. Link tanpa akun (PUB-8) ditunda.
11. ~~Lokasi post publik~~ **Diputuskan**: tidak dikaburkan secara paksa; `EXACT` diizinkan dan jadi default, user tetap bisa memilih `APPROX`/`HIDDEN`.
12. **Moderator**: siapa yang menangani laporan? Hanya pembuat app, atau ada beberapa admin?
13. **Komentar di post publik**: terbuka untuk semua user login, atau hanya teman?

---

## 16. Out of Scope (Ditunda)

Status kucing (sakit, steril, diadopsi, hilang), health log, log makan, tag kepribadian, link publik tanpa login, fitur follow, mode komunitas/rescuer, database kucing bersama, app native.
