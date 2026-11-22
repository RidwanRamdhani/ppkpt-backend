#!/usr/bin/env bash
# Membuat akun uji baru lewat cmd/provision dan menulis file environment
# Postman yang sudah terisi (username, password sementara, URL).
#
#   bash scripts/prepare-postman.sh [file-keluaran]
#
# Jalankan ulang setiap kali ingin mengulang koleksi dari awal: akun uji
# berubah status (password diganti, terkunci) sehingga perlu akun baru.
# Variabel opsional: BASE_URL, ORIGIN, PROVISION_BIN.
set -euo pipefail
cd "$(dirname "$0")/.."

OUT="${1:-postman/ppkpt-local.generated.postman_environment.json}"
BASE_URL="${BASE_URL:-http://localhost:8080}"
ORIGIN="${ORIGIN:-http://localhost:3000}"
STAMP="$(date +%s)$RANDOM"
TEMP_PW="Sementara-${STAMP}"
NEW_PW="Sandi-Baru-${STAMP}"
USER_FLOW="pm_satgas_${STAMP}"
USER_LOCK="pm_lock_${STAMP}"

run() { if [ -n "${PROVISION_BIN:-}" ]; then "$PROVISION_BIN" "$@"; else go run ./cmd/provision "$@"; fi; }

export PROVISION_PASSWORD="$TEMP_PW"
run create -name "Postman Satgas" -username "$USER_FLOW" -email "${USER_FLOW}@postman.test" -role SATGAS >/dev/null
run create -name "Postman Lock"   -username "$USER_LOCK" -email "${USER_LOCK}@postman.test" -role SATGAS >/dev/null

val() { printf '    {"key": "%s", "value": "%s", "type": "default", "enabled": true},\n' "$1" "$2"; }
{
  echo '{'
  echo '  "id": "ppkpt-local-env",'
  echo '  "name": "PPKPT Local (generated)",'
  echo '  "values": ['
  val base_url "$BASE_URL"
  val origin "$ORIGIN"
  val username "$USER_FLOW"
  val temp_password "$TEMP_PW"
  val new_password "$NEW_PW"
  val wrong_password "Salah-Sandi-1"
  val lock_username "$USER_LOCK"
  printf '    {"key": "max_attempts", "value": "5", "type": "default", "enabled": true}\n'
  echo '  ],'
  echo '  "_postman_variable_scope": "environment"'
  echo '}'
} > "$OUT"

echo "Akun uji dibuat: $USER_FLOW (alur) dan $USER_LOCK (penguncian)"
echo "Environment ditulis ke: $OUT"
echo "Import file itu ke Postman, pilih sebagai environment aktif, lalu jalankan koleksi."
echo "Ingat: jalankan API dengan RATE_LIMIT_AUTH_PER_MINUTE=1000 agar batas laju tidak menghalangi."
