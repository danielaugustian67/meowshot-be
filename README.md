# Meowshot Backend

API Go untuk Meowshot ("Instagram + Pokédex untuk kucing jalanan", lihat [prd.md](prd.md)).
Klien: app React Native (Expo) untuk iOS dan Android.

| Bagian | Pilihan |
|---|---|
| Bahasa / router | Go 1.27, `chi` |
| Database | PostgreSQL (`pgx`), migrasi SQL ter-embed, jalan otomatis saat start |
| Storage foto | Backblaze B2 (bucket privat, API S3-compatible), akses lewat signed URL berumur pendek |
| Auth | Email + password (argon2id) dan Google Sign-In (verifikasi ID token); JWT access token + refresh token berotasi |
| Gambar | Decode JPEG/PNG/WebP/HEIC → buang EXIF → WebP display (1600px) + thumbnail (400px), GPS EXIF diambil sekali untuk sighting |

## Menjalankan secara lokal

```bash
docker compose up -d
```

```bash
cp .env.example .env
```

Isi `.env` untuk dev dengan MinIO (pengganti B2 lokal):

```
DATABASE_URL=postgres://meowshot:meowshot@localhost:5432/meowshot?sslmode=disable
B2_ENDPOINT=http://192.168.x.x:9000   # IP LAN laptop, supaya signed URL bisa dibuka dari HP
B2_REGION=us-east-1
B2_BUCKET=meowshot-photos
B2_KEY_ID=meowshot
B2_APP_KEY=meowshot-secret
```

```bash
set -a && source .env && set +a && go run ./cmd/api
```

Perintah lain:

- `go run ./cmd/api migrate`: hanya menjalankan migrasi
- `go run ./cmd/api promote email@contoh.com`: menjadikan user admin (panel moderasi)

Tes (butuh Postgres kosong; schema `public` akan dihapus):

```bash
TEST_DATABASE_URL="postgres://meowshot:meowshot@localhost:5432/meowshot_test?sslmode=disable" go test ./...
```

## Deploy ke VPS

Satu VPS menjalankan tiga container lewat [compose.prod.yml](compose.prod.yml): Postgres, API, dan Caddy (HTTPS otomatis dari Let's Encrypt, lihat [Caddyfile](Caddyfile)). Hanya port 80/443 yang terbuka ke internet. Panduan ini mengasumsikan Ubuntu/Debian dengan RAM minimal 1 GB.

### 1. Siapkan server (sekali saja)

Login sebagai root (atau user dengan sudo), lalu pasang Docker:

```bash
curl -fsSL https://get.docker.com | sh
```

Buka firewall untuk SSH, HTTP, dan HTTPS:

```bash
ufw allow OpenSSH && ufw allow 80/tcp && ufw allow 443/tcp && ufw allow 443/udp && ufw --force enable
```

Kalau RAM 1 GB, tambahkan swap supaya build Go dan decode foto tidak kehabisan memori:

```bash
fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile && echo '/swapfile none swap sw 0 0' >> /etc/fstab
```

### 2. Domain

Buat A record (misal `api.domainmu.com`) yang mengarah ke IP VPS. Belum punya domain? Pakai `IP-dengan-strip.sslip.io`, misal VPS `203.0.113.10` → `203-0-113-10.sslip.io`. Ini langsung jalan tanpa setting DNS.

### 3. Kirim kode ke VPS

Paling mudah lewat repo GitHub privat (`git clone` di VPS ke `/opt/meowshot`). Tanpa GitHub, kirim dari laptop dengan rsync:

```bash
rsync -az --exclude .git --exclude '.env*' ./ root@IP_VPS:/opt/meowshot/
```

### 4. Isi env dan jalankan

Di VPS:

```bash
cd /opt/meowshot && cp .env.production.example .env.production && chmod 600 .env.production
```

Isi `.env.production` (`nano .env.production`). `DATABASE_URL`, `ADDR`, dan `TRUST_PROXY` sudah diatur di compose, jadi tidak perlu diisi. Buat `POSTGRES_PASSWORD`, `JWT_SECRET`, dan `LOCATION_SECRET` masing-masing dengan:

```bash
openssl rand -hex 32
```

Lalu build dan jalankan:

```bash
docker compose -f compose.prod.yml --env-file .env.production up -d --build
```

Cek log sampai muncul `listening`, dan pastikan Caddy berhasil mengambil sertifikat:

```bash
docker compose -f compose.prod.yml --env-file .env.production logs -f api caddy
```

Tes dari laptop: `curl https://DOMAIN/healthz`. Base URL untuk app mobile: `https://DOMAIN/v1`.

Setelah daftar akun lewat app, jadikan dirimu admin:

```bash
docker compose -f compose.prod.yml --env-file .env.production exec api meowshot promote email@kamu.com
```

### Update versi

Kirim kode baru (`git pull` atau rsync lagi), lalu jalankan ulang perintah `up -d --build` di atas. Migrasi database jalan otomatis saat API start.

### Backup database

[scripts/backup-db.sh](scripts/backup-db.sh) menyimpan dump ke `backups/` dan menghapus yang lebih dari 14 hari. Jadwalkan tiap jam 3 pagi dengan `crontab -e`:

```
0 3 * * * cd /opt/meowshot && ./scripts/backup-db.sh >> backups/backup.log 2>&1
```

Backup ini masih di VPS yang sama; sesekali salin ke laptop atau ke bucket B2 terpisah. Foto sudah aman di B2. Restore:

```bash
gunzip -c backups/FILE.sql.gz | docker compose -f compose.prod.yml --env-file .env.production exec -T db psql -U meowshot -d meowshot
```

Catatan:

- Volume `caddy_data` menyimpan sertifikat HTTPS. Jangan dihapus, karena Let's Encrypt membatasi jumlah penerbitan ulang.
- Rate limit disimpan di memori API, jadi reset setiap restart. Untuk satu VPS ini cukup.
- `IMAGE_WORKERS=1` mencegah dua foto besar di-decode bersamaan di VPS kecil.

## Setup Backblaze B2 (production)

1. Buat bucket **Private** (misal `meowshot-photos`). Endpoint S3 terlihat di halaman bucket, misal `s3.us-west-004.backblazeb2.com` → `B2_ENDPOINT=https://s3.us-west-004.backblazeb2.com`, `B2_REGION=us-west-004`.
2. Buat Application Key yang dibatasi ke bucket itu dengan izin read & write → `B2_KEY_ID` (keyID) dan `B2_APP_KEY` (applicationKey).
3. Lifecycle rule: pilih "Keep only the last version". Kalau tidak, file yang dihapus lewat API hanya disembunyikan dan tetap ditagih.
4. Foto tidak pernah punya URL publik. Setiap respons API berisi signed URL yang berlaku selama `SIGNED_URL_TTL` (default 15 menit). App sebaiknya tidak menyimpan URL ini lama-lama; ambil ulang dari API.

## Setup Google Sign-In (Expo)

1. Di Google Cloud Console buat OAuth Client ID: **Web** (wajib), **iOS** (bundle id app), **Android** (package name + SHA-1).
2. Isi `GOOGLE_CLIENT_IDS` dengan semua client ID itu, dipisah koma.
3. Di app, pakai `@react-native-google-signin/google-signin` (butuh development build, tidak jalan di Expo Go) dengan `webClientId` = Web client ID. Setelah sign-in, kirim `idToken` ke `POST /v1/auth/google`.

Akun Google dicocokkan lewat Google `sub`, lalu lewat email. Kalau email itu sudah terdaftar via password tapi belum pernah diverifikasi, password lama dihapus saat Google di-link (mencegah pembajakan akun yang didaftarkan duluan oleh orang lain).

## Panduan integrasi mobile

### Token

- Semua endpoint kecuali `/v1/auth/*` butuh header `Authorization: Bearer <access_token>`.
- `access_token` berlaku 15 menit. Saat dapat `401`, panggil `POST /v1/auth/refresh` dengan `refresh_token`, simpan pasangan token baru, lalu ulangi request.
- Refresh token **berotasi dan sekali pakai**. Kalau token lama dipakai lagi, semua sesi user dicabut. Jadi jalankan refresh **satu per satu** (pakai satu promise bersama), jangan paralel.
- Simpan token di `expo-secure-store`, bukan AsyncStorage.

### Upload foto (quick capture)

```ts
const form = new FormData();
form.append('photo', { uri: asset.uri, name: 'cat.jpg', type: 'image/jpeg' } as any);
if (coords) {
  form.append('lat', String(coords.latitude));
  form.append('lng', String(coords.longitude));
  form.append('location_source', pickedOnMap ? 'manual' : 'device');
}
const res = await fetch(`${API}/v1/captures`, {
  method: 'POST',
  headers: { Authorization: `Bearer ${token}` },
  body: form,
});
// => { cat, photo, sighting, suggestions: [{ cat, distance_m }] }
```

- Kompres di HP dulu (`expo-image-manipulator`, lebar ~1600, kualitas 0.8). Kompresi ini menghapus EXIF, jadi ambil GPS dari `expo-image-picker` (`exif: true`) atau `expo-location` lalu kirim sebagai `lat`/`lng`. Kalau EXIF masih ada, server memakai GPS EXIF lebih dulu, kecuali `location_source=manual`.
- Kalau user memilih "ini si Oyen" dari `suggestions`, panggil `POST /v1/cats/{catBaru}/merge` dengan `{"into_cat_id": idOyen}`.

### Error

Semua error berbentuk `{"error": {"code": "...", "message": "..."}}`. Kode penting: `unauthorized`, `account_suspended`, `rate_limited`, `validation_failed`, `username_taken`, `email_taken`, `invite_required`.

### Pagination

List memakai `?cursor=&limit=`; respons `{ "items": [...], "next_cursor": "123" | null }`.

## Endpoint

Semua di bawah prefix `/v1`.

| Method & path | Keterangan | PRD |
|---|---|---|
| `POST /auth/register` | `{email, password, username, invite_code?}` | AUTH-1, AUTH-5 |
| `POST /auth/login` | `{email (atau username), password}` | AUTH-1 |
| `POST /auth/google` | `{id_token, invite_code?}` → `is_new_user` untuk menawarkan ganti username | AUTH-1 |
| `POST /auth/refresh`, `POST /auth/logout` | `{refresh_token}` | |
| `POST /auth/password/forgot` | `{email}` → kode 6 digit via email | AUTH-3 |
| `POST /auth/password/reset` | `{email, code, new_password}` | AUTH-3 |
| `GET/PATCH/DELETE /me` | profil sendiri; `PATCH {username?, bio?}`; `DELETE {password}` menghapus akun + semua foto | AUTH-2, AUTH-4, PROF-3 |
| `PUT /me/password` | `{current_password, new_password}` | |
| `PUT/DELETE /me/avatar` | multipart `photo` | PROF-3 |
| `GET /cats?q=&sort=last_seen\|found\|name` | grid kucing + pencarian; favorit di atas | CAT-5..7 |
| `POST /cats`, `GET/PATCH/DELETE /cats/{id}` | `{name?, notes?, is_favorite?}`; string kosong menghapus field | CAT-1, CAT-4 |
| `POST /cats/{id}/merge` | `{into_cat_id}` | QC-2 |
| `PUT /cats/{id}/cover` | `{photo_id}` | CAT-3 |
| `GET/POST /cats/{id}/photos` | upload multipart `photo, caption?, add_sighting?, lat?, lng?, label?, location_source?, seen_at?` | CAT-2 |
| `PATCH/DELETE /photos/{id}` | `{caption}` | CAT-3 |
| `GET/POST /cats/{id}/sightings`, `PATCH/DELETE /sightings/{id}` | "ketemu lagi" `{lat?, lng?, label?, seen_at?}` | LOC-1, LOC-3, LOC-4 |
| `POST /captures` | quick capture multipart (lihat di atas), `cat_id?` untuk langsung ke kucing yang ada | QC-1, QC-2, LOC-2 |
| `GET /cats/nearby?lat=&lng=&radius_m=` | saran "apakah ini si Oyen?" | LOC-7 |
| `GET /map/cats` | satu marker per kucing (sighting terakhir yang punya koordinat) | LOC-5 |
| `GET /users/search?q=` | prefix username | FR-1 |
| `GET /users/{username}`, `GET /users/{username}/posts` | profil + post yang boleh dilihat | PROF-1, PROF-2, PUB-5 |
| `GET /friends`, `DELETE /friends/{userId}` | | FR-3, FR-4 |
| `GET /friends/requests?direction=incoming\|outgoing` | | FR-2 |
| `POST /friends/requests` `{username}` | otomatis diterima kalau pihak lain sudah mengirim request | FR-2 |
| `POST /friends/requests/{id}/accept`, `DELETE /friends/requests/{id}` | terima / tolak / batalkan | FR-2 |
| `GET/POST /blocks`, `DELETE /blocks/{userId}` | `{user_id}`; dua arah | FR-5, MOD-2 |
| `GET /feed` | teman + diri sendiri | POST-4 |
| `GET /explore` | post `PUBLIC` | PUB-3 |
| `POST /posts` | `{cat_id, photo_ids[1..10], caption, visibility, loc_precision, sighting_id?}` | POST-1..3 |
| `GET/PATCH/DELETE /posts/{id}` | `PATCH {caption?, visibility?, loc_precision?, comments_disabled?}` | POST-5, POST-8, MOD-3, PUB-7 |
| `PUT/DELETE /posts/{id}/reaction` | `{emoji}` salah satu dari ❤️ 😻 😹 🐾 👍 😢 | POST-6 |
| `GET/POST /posts/{id}/comments`, `DELETE /comments/{id}` | | POST-7 |
| `GET /notifications`, `GET /notifications/unread-count`, `POST /notifications/read` | `{ids:[...]}` atau `{all:true}` | NOTIF-1 |
| `POST /reports` | `{target_type: POST\|COMMENT\|USER, target_id, reason}` | MOD-1 |
| `GET /admin/reports?status=`, `POST /admin/reports/{id}` | antrean laporan; `{status}` | MOD-4 |
| `DELETE /admin/posts/{id}`, `DELETE /admin/comments/{id}` | hapus konten | MOD-4 |
| `POST /admin/users/{id}/suspend`, `.../unsuspend` | | MOD-4 |

## Privasi dan otorisasi

- Semua aturan "siapa boleh lihat apa" ada di satu file, [internal/api/authz.go](internal/api/authz.go) (matriks PRD §7). Query list dan cek per-resource memakai predikat SQL yang sama. Resource yang tidak boleh dilihat dijawab `404`, bukan `403`.
- Kucing, foto, dan sighting yang belum diposting hanya bisa diakses pemiliknya.
- Lokasi dikaburkan di server. `APPROX` menggeser titik 60–200 m secara deterministik (HMAC dari `LOCATION_SECRET` + id sighting), jadi refresh berulang tidak bisa dirata-ratakan. `HIDDEN` hanya mengirim label. Pemilik post mendapat `location` (asli) dan `shared_location` (yang dilihat orang lain) untuk pratinjau peringatan `EXACT`.
- Foto disimpan ulang dari piksel, jadi tidak ada metadata EXIF yang ikut ke B2.
- Rate limit (in-memory, cukup untuk satu server): auth 10/menit per IP, friend request 30/jam, explore 60/menit, komentar 20/menit, report 10/jam, upload 60/menit per user.

## Keputusan yang berbeda dari PRD

- PRD menyebut PWA. Karena klien sekarang React Native, auth memakai Bearer token (bukan cookie) dan web push (NOTIF-2) belum ada; untuk push nanti, tambahkan registrasi Expo push token.
- Jitter `APPROX` dihitung per **sighting**, bukan per post, supaya beberapa post dari sighting yang sama tidak bisa digabung untuk menebak titik asli.
- `loc_precision` default `EXACT` untuk post `PUBLIC` (PUB-2) dan `APPROX` untuk lainnya.
- Belum dibuat (P2): badge, leaderboard, statistik, "on this day", reminder, peta teman (LOC-8), invite link per user (yang ada hanya satu `INVITE_CODE` global), share link tanpa login.
