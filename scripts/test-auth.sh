#!/usr/bin/env bash
# Uji end-to-end modul autentikasi terhadap API yang sedang berjalan.
#
# Script ini:
#   1. membuat akun uji baru lewat cmd/provision (nama unik per eksekusi),
#   2. memanggil API dengan curl (cookie jar per sesi),
#   3. memeriksa status, isi respons, atribut cookie, penguncian akun,
#      pencabutan sesi, CSRF, dan (bila psql tersedia) baris audit log,
#   4. menonaktifkan akun uji lewat cmd/provision,
#   5. mencetak ringkasan dan keluar dengan kode 1 bila ada yang gagal.
#
# Prasyarat: curl, go (atau PROVISION_BIN), database + migrasi, .env terisi.
# API: bila BASE_URL belum menjawab /api/health, script menjalankan API sendiri
# (API_BIN atau `go run ./cmd/api`) dengan RATE_LIMIT_AUTH_PER_MINUTE besar agar
# batas laju tidak menghalangi uji, lalu menghentikannya saat selesai.
#
# Variabel (semua opsional):
#   BASE_URL            default http://localhost:8080
#   ORIGIN              default http://localhost:3000 (harus sama dengan APP_ORIGIN)
#   LOGIN_MAX_ATTEMPTS  default 5 (harus sama dengan konfigurasi API)
#   API_BIN / PROVISION_BIN   biner siap pakai (menghindari `go run`)
#   KEEP_API=1          jangan hentikan API yang dijalankan script
#   VERBOSE=1           cetak isi respons setiap request
#
# Contoh:  make test-e2e     atau     bash scripts/test-auth.sh
set -uo pipefail

cd "$(dirname "$0")/.."

BASE_URL="${BASE_URL:-http://localhost:8080}"
ORIGIN="${ORIGIN:-http://localhost:3000}"
LOGIN_MAX_ATTEMPTS="${LOGIN_MAX_ATTEMPTS:-5}"
STAMP="$(date +%s)$RANDOM"
TMP="$(mktemp -d)"
API_PID=""
PASS=0
FAIL=0
FAILED_NAMES=()

TEMP_PW="Sementara-${STAMP}"     # password awal (wajib diganti)
NEW_PW="Sandi-Baru-${STAMP}"     # password sesudah diganti
SATGAS_USER="e2e_satgas_${STAMP}"
SATGAS_MAIL="${SATGAS_USER}@e2e.test"
LOCK_USER="e2e_lock_${STAMP}"
LOCK_MAIL="${LOCK_USER}@e2e.test"
PEJABAT_USER="e2e_pejabat_${STAMP}"
PEJABAT_MAIL="${PEJABAT_USER}@e2e.test"

if [ -t 1 ]; then G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; B=$'\033[1m'; N=$'\033[0m'; else G=""; R=""; Y=""; B=""; N=""; fi

cleanup() {
  for u in "$SATGAS_USER" "$LOCK_USER" "$PEJABAT_USER"; do
    PROVISION deactivate "$u" >/dev/null 2>&1 || true
  done
  if [ -n "$API_PID" ] && [ "${KEEP_API:-0}" != "1" ]; then
    kill "$API_PID" >/dev/null 2>&1 || true
    wait "$API_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

PROVISION() {
  if [ -n "${PROVISION_BIN:-}" ]; then "$PROVISION_BIN" "$@"; else go run ./cmd/provision "$@"; fi
}

# ---------------------------------------------------------------- helper uji
STATUS=""; BODY=""; HEADERS=""
# req METHOD PATH [BODY] [JAR] [ORIGIN]  -> mengisi STATUS, BODY, HEADERS
req() {
  local method="$1" path="$2" body="${3:-}" jar="${4:-}" origin="${5-$ORIGIN}"
  local args=(-s -m 20 -X "$method" -o "$TMP/body" -D "$TMP/headers" -w '%{http_code}')
  [ -n "$origin" ] && args+=(-H "Origin: $origin")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data "$body")
  [ -n "$jar" ] && args+=(-b "$jar" -c "$jar")
  STATUS="$(curl "${args[@]}" "$BASE_URL$path" 2>/dev/null || echo 000)"
  BODY="$(cat "$TMP/body" 2>/dev/null || true)"
  HEADERS="$(cat "$TMP/headers" 2>/dev/null || true)"
  [ "${VERBOSE:-0}" = "1" ] && printf '    %s %s -> %s %s\n' "$method" "$path" "$STATUS" "$BODY"
  if [ "$STATUS" = "429" ]; then
    printf '%s  ! 429 dari rate limit. Jalankan API dengan RATE_LIMIT_AUTH_PER_MINUTE=1000 untuk uji ini.%s\n' "$Y" "$N"
  fi
}

ok()   { PASS=$((PASS+1)); printf '  %s✓%s %s\n' "$G" "$N" "$1"; }
bad()  { FAIL=$((FAIL+1)); FAILED_NAMES+=("$1"); printf '  %s✗%s %s\n' "$R" "$N" "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }

# expect_status NAMA DIHARAPKAN
expect_status() {
  if [ "$STATUS" = "$2" ]; then ok "$1 (HTTP $STATUS)"; else bad "$1" "HTTP $STATUS, diharapkan $2. Body: $BODY"; fi
}
# expect_contains NAMA TEKS  (di body)
expect_body_has() {
  if printf '%s' "$BODY" | grep -q -- "$2"; then ok "$1"; else bad "$1" "body tidak memuat '$2': $BODY"; fi
}
expect_body_lacks() {
  if printf '%s' "$BODY" | grep -qi -- "$2"; then bad "$1" "body tidak boleh memuat '$2': $BODY"; else ok "$1"; fi
}
expect_header_has() {
  if printf '%s' "$HEADERS" | grep -qi -- "$2"; then ok "$1"; else bad "$1" "header tidak memuat '$2'"; fi
}
json_str() { printf '%s' "$BODY" | grep -o "\"$1\":\"[^\"]*\"" | head -1 | cut -d'"' -f4; }
login_body() { printf '{"identifier":"%s","password":"%s"}' "$1" "$2"; }
section() { printf '\n%s%s%s\n' "$B" "$1" "$N"; }

# ------------------------------------------------------------ siapkan lingkungan
section "Persiapan"
if ! curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE_URL/api/health" | grep -q '^200$'; then
  port="${BASE_URL##*:}"; port="${port%%/*}"
  echo "  API belum menjawab di $BASE_URL; menjalankan API sendiri di :$port ..."
  if [ -n "${API_BIN:-}" ]; then
    HTTP_ADDR=":$port" RATE_LIMIT_AUTH_PER_MINUTE=1000 "$API_BIN" >"$TMP/api.log" 2>&1 &
  else
    HTTP_ADDR=":$port" RATE_LIMIT_AUTH_PER_MINUTE=1000 go run ./cmd/api >"$TMP/api.log" 2>&1 &
  fi
  API_PID=$!
  for _ in $(seq 1 40); do
    curl -s -m 2 -o /dev/null -w '%{http_code}' "$BASE_URL/api/health" 2>/dev/null | grep -q '^200$' && break
    sleep 0.5
  done
fi
if ! curl -s -m 3 -o /dev/null -w '%{http_code}' "$BASE_URL/api/health" | grep -q '^200$'; then
  echo "${R}API tidak siap di $BASE_URL${N}"; [ -f "$TMP/api.log" ] && tail -20 "$TMP/api.log"; exit 2
fi
ok "API siap di $BASE_URL"

PROVISION_PASSWORD="$TEMP_PW" PROVISION_BIN="${PROVISION_BIN:-}" bash -c '
  set -e
  run() { if [ -n "$PROVISION_BIN" ]; then "$PROVISION_BIN" "$@"; else go run ./cmd/provision "$@"; fi; }
  run create -name "E2E Satgas" -username "'"$SATGAS_USER"'" -email "'"$SATGAS_MAIL"'" -role SATGAS
  run create -name "E2E Lock" -username "'"$LOCK_USER"'" -email "'"$LOCK_MAIL"'" -role SATGAS
  run create -name "E2E Pejabat" -username "'"$PEJABAT_USER"'" -email "'"$PEJABAT_MAIL"'" -role PEJABAT
' >"$TMP/provision.out" 2>&1 \
  && ok "akun uji dibuat lewat cmd/provision" \
  || { cat "$TMP/provision.out"; echo "${R}gagal membuat akun uji${N}"; exit 2; }

JAR_A="$TMP/jar_a"; JAR_B="$TMP/jar_b"; JAR_P="$TMP/jar_p"

# ------------------------------------------------------------------- 1. tanpa sesi
section "1. Tanpa sesi"
req GET /api/auth/me
expect_status "GET /api/auth/me tanpa cookie ditolak" 401
req PUT /api/auth/password '{"current_password":"x","new_password":"y"}'
expect_status "PUT /api/auth/password tanpa cookie ditolak" 401
req POST /api/auth/login '{}'
expect_status "login dengan isian kosong ditolak" 422
expect_body_has "422 menyebut field identifier dan password" '"identifier"'
req POST /api/auth/login '{"identifier":"a","password":"b","admin":true}'
expect_status "field tak dikenal pada login ditolak" 422

# ------------------------------------------------------------------- 2. login gagal
section "2. Login gagal"
req POST /api/auth/login "$(login_body "$SATGAS_USER" 'Salah-Sandi-1')"
expect_status "password salah -> 401" 401
MSG_WRONG="$(json_str message)"
req POST /api/auth/login "$(login_body "tidak_ada_${STAMP}" 'Salah-Sandi-1')"
expect_status "akun tidak ada -> 401" 401
MSG_UNKNOWN="$(json_str message)"
if [ -n "$MSG_WRONG" ] && [ "$MSG_WRONG" = "$MSG_UNKNOWN" ]; then ok "pesan galat sama untuk akun tidak ada dan password salah"; else bad "pesan galat sama untuk akun tidak ada dan password salah" "'$MSG_WRONG' vs '$MSG_UNKNOWN'"; fi
if printf '%s' "$HEADERS" | grep -qi 'set-cookie: ppkpt_session'; then bad "login gagal tidak boleh memasang cookie sesi"; else ok "login gagal tidak memasang cookie sesi"; fi

# ------------------------------------------------------------------- 3. login berhasil
section "3. Login berhasil"
req POST /api/auth/login "$(login_body "$SATGAS_USER" "$TEMP_PW")" "$JAR_A"
expect_status "login dengan username" 200
expect_body_has "role SATGAS" '"role":"SATGAS"'
expect_body_has "must_change_password true untuk akun baru" '"must_change_password":true'
expect_body_lacks "respons tidak memuat password/hash" 'password_hash'
expect_header_has "cookie ppkpt_session dipasang" 'set-cookie: ppkpt_session='
expect_header_has "cookie HttpOnly" 'HttpOnly'
expect_header_has "cookie SameSite=Lax" 'SameSite=Lax'
req POST /api/auth/login "$(login_body "$(printf '%s' "$SATGAS_MAIL" | tr '[:lower:]' '[:upper:]')" "$TEMP_PW")" "$JAR_B"
expect_status "login dengan email (huruf besar)" 200

req GET /api/auth/me "" "$JAR_A"
expect_status "GET /api/auth/me dengan sesi" 200
expect_body_has "me memuat username" "\"username\":\"$SATGAS_USER\""

# ------------------------------------------------------------------- 4. ganti password
section "4. Ganti password"
req PUT /api/auth/password "{\"current_password\":\"Salah-Sandi-1\",\"new_password\":\"$NEW_PW\"}" "$JAR_B"
expect_status "password saat ini salah -> 422" 422
expect_body_has "422 menyebut current_password" 'current_password'
req PUT /api/auth/password "{\"current_password\":\"$TEMP_PW\",\"new_password\":\"pendek1\"}" "$JAR_B"
expect_status "password baru lemah -> 422" 422
expect_body_has "422 menyebut new_password" 'new_password'
req PUT /api/auth/password "{\"current_password\":\"$TEMP_PW\",\"new_password\":\"$TEMP_PW\"}" "$JAR_B"
expect_status "password baru sama dengan lama -> 422" 422
req PUT /api/auth/password "{\"current_password\":\"$TEMP_PW\",\"new_password\":\"$NEW_PW\"}" "$JAR_B"
expect_status "ganti password berhasil" 204
req GET /api/auth/me "" "$JAR_B"
expect_status "sesi yang dipakai mengganti password tetap berlaku" 200
expect_body_has "must_change_password menjadi false" '"must_change_password":false'
req GET /api/auth/me "" "$JAR_A"
expect_status "sesi LAIN dicabut setelah ganti password" 401
req POST /api/auth/login "$(login_body "$SATGAS_USER" "$TEMP_PW")"
expect_status "password lama tidak berlaku lagi" 401
req POST /api/auth/login "$(login_body "$SATGAS_USER" "$NEW_PW")" "$JAR_A"
expect_status "login dengan password baru" 200

# ------------------------------------------------------------------- 5. logout
section "5. Logout"
req POST /api/auth/logout "" "$JAR_A"
expect_status "logout" 204
expect_header_has "cookie dibersihkan saat logout" 'Max-Age=0'
req GET /api/auth/me "" "$JAR_A"
expect_status "sesi tidak berlaku setelah logout" 401
req POST /api/auth/logout ""
expect_status "logout tanpa sesi tetap 204 (idempoten)" 204

# ------------------------------------------------------------------- 6. penguncian
section "6. Penguncian akun ($LOGIN_MAX_ATTEMPTS gagal)"
for i in $(seq 1 $((LOGIN_MAX_ATTEMPTS - 1))); do
  req POST /api/auth/login "$(login_body "$LOCK_USER" 'Salah-Sandi-1')"
  expect_status "gagal ke-$i -> 401" 401
done
req POST /api/auth/login "$(login_body "$LOCK_USER" 'Salah-Sandi-1')"
expect_status "gagal ke-$LOGIN_MAX_ATTEMPTS mengunci akun -> 423" 423
expect_body_has "423 memuat locked_until" 'locked_until'
req POST /api/auth/login "$(login_body "$LOCK_USER" "$TEMP_PW")"
expect_status "password BENAR tetap ditolak selama terkunci" 423

# ------------------------------------------------------------------- 7. CSRF
section "7. CSRF"
req POST /api/auth/login "$(login_body "$SATGAS_USER" "$NEW_PW")" "" "https://jahat.example"
expect_status "Origin asing ditolak" 403

# ------------------------------------------------------------------- 8. role pejabat
section "8. Role Pejabat"
req POST /api/auth/login "$(login_body "$PEJABAT_USER" "$TEMP_PW")" "$JAR_P"
expect_status "login pejabat" 200
expect_body_has "role PEJABAT" '"role":"PEJABAT"'

# ------------------------------------------------------------------- 9. nonaktifkan akun
section "9. Penonaktifan akun (cmd/provision)"
req GET /api/auth/me "" "$JAR_P"
expect_status "sesi pejabat aktif sebelum dinonaktifkan" 200
if PROVISION deactivate "$PEJABAT_USER" >"$TMP/deact.out" 2>&1; then ok "provision deactivate berhasil"; else bad "provision deactivate" "$(cat "$TMP/deact.out")"; fi
req GET /api/auth/me "" "$JAR_P"
expect_status "sesi akun nonaktif langsung tidak berlaku" 401
req POST /api/auth/login "$(login_body "$PEJABAT_USER" "$TEMP_PW")"
expect_status "akun nonaktif tidak bisa login" 401

# ------------------------------------------------------------------- 10. audit (opsional)
section "10. Audit log"
DBURL="${DATABASE_URL:-}"
if [ -z "$DBURL" ] && [ -f .env ]; then DBURL="$(grep -E '^DATABASE_URL=' .env | head -1 | cut -d= -f2-)"; fi
if command -v psql >/dev/null 2>&1 && [ -n "$DBURL" ]; then
  audit_count() {
    psql "$DBURL" -At -c "SELECT count(*) FROM audit_logs a JOIN users u ON u.id = a.user_id WHERE u.username = '$1' AND a.action = '$2'" 2>/dev/null
  }
  check_audit() { # username action minimum
    local n; n="$(audit_count "$1" "$2")"
    if [ -n "$n" ] && [ "$n" -ge "$3" ] 2>/dev/null; then ok "audit $2 untuk $1 (>= $3, ada $n)"; else bad "audit $2 untuk $1" "ditemukan '${n:-error}', minimal $3"; fi
  }
  check_audit "$SATGAS_USER" LOGIN_SUCCESS 3
  check_audit "$SATGAS_USER" LOGIN_FAILED 1
  check_audit "$SATGAS_USER" PASSWORD_CHANGED 1
  check_audit "$SATGAS_USER" LOGOUT 1
  check_audit "$LOCK_USER" ACCOUNT_LOCKED 1
  check_audit "$PEJABAT_USER" LOGIN_SUCCESS 1
else
  echo "  (dilewati: psql atau DATABASE_URL tidak tersedia)"
fi

# ---------------------------------------------------------------------- ringkasan
printf '\n%sHasil: %s lulus, %s gagal%s\n' "$B" "$PASS" "$FAIL" "$N"
if [ "$FAIL" -gt 0 ]; then
  printf '%sGagal:%s\n' "$R" "$N"; printf '  - %s\n' "${FAILED_NAMES[@]}"
  exit 1
fi
