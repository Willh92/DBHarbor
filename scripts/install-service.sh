#!/usr/bin/env sh
# ==============================================================================
# DBHarbor Cross-Platform Auto-Start Service Installer (Linux & macOS)
# Repository : https://github.com/Willh92/DBHarbor.git
# License    : Apache-2.0
#
# Supports:
#   - Linux systemd (System unit /etc/systemd/system/dbharbor.service
#                    or User unit ~/.config/systemd/user/dbharbor.service)
#   - Linux OpenRC  (/etc/init.d/dbharbor for Alpine / Gentoo)
#   - macOS launchd (System LaunchDaemon /Library/LaunchDaemons/io.dbharbor.server.plist
#                    or User LaunchAgent ~/Library/LaunchAgents/io.dbharbor.server.plist)
#   - Service execution role (--user), primary group (--group), supplementary groups
#     (--supplementary-groups), Unix domain socket group (--socket-group), and
#     data directory permission mode (--data-dir-mode).
# ==============================================================================

set -eu
umask 022

SERVICE_NAME="dbharbor"
MACOS_LABEL="io.dbharbor.server"

ACTION="install"
BIN_SRC=""
INSTALL_DIR=""
HOST_ADDR="0.0.0.0"
PORT="8787"
UNIX_SOCKET=""
UNIX_SOCKET_PERM="0660"
SOCKET_GROUP=""
DATA_DIR=""
DATA_DIR_MODE="0750"
SVC_USER=""
SVC_GROUP=""
SUPP_GROUPS=""
USER_MODE="0"
ADMIN_USER="${DBHARBOR_ADMIN_USER:-}"
ADMIN_PASS="${DBHARBOR_ADMIN_PASS:-}"
GITHUB_REPO="${DBHARBOR_REPO:-Willh92/DBHarbor}"
RELEASE_VERSION="${DBHARBOR_VERSION:-}"
RELEASE_CHANNEL="${DBHARBOR_CHANNEL:-release}"
GITHUB_MIRROR="${DBHARBOR_MIRROR:-}"
FORCE_DOWNLOAD="0"
SKIP_VERIFY="0"
RESOLVED_BIN_PATH=""
TMP_DOWNLOAD_DIR=""
BOOTSTRAP_ENV_FILE=""

cleanup_tmp() {
  if [ -n "$TMP_DOWNLOAD_DIR" ] && [ -d "$TMP_DOWNLOAD_DIR" ]; then
    rm -rf "$TMP_DOWNLOAD_DIR" 2>/dev/null || true
  fi
  if [ -n "$BOOTSTRAP_ENV_FILE" ] && [ -f "$BOOTSTRAP_ENV_FILE" ]; then
    rm -f "$BOOTSTRAP_ENV_FILE" 2>/dev/null || true
  fi
}
trap 'cleanup_tmp' EXIT INT TERM

log_info() {
  printf "\033[1;34m[DBHarbor]\033[0m %s\n" "$*"
}

log_ok() {
  printf "\033[1;32m[DBHarbor]\033[0m %s\n" "$*"
}

log_warn() {
  printf "\033[1;33m[DBHarbor]\033[0m %s\n" "$*" >&2
}

log_err() {
  printf "\033[1;31m[DBHarbor ERROR]\033[0m %s\n" "$*" >&2
}

usage() {
  cat <<'EOF'
DBHarbor Service Manager (Linux systemd / OpenRC & macOS launchd)

Usage:
  install-service.sh [command] [options]

Commands:
  install      Install binary (local or auto-downloaded from GitHub Releases), register service, and start (default)
  upgrade      Download latest/specified release binary from GitHub, verify SHA-256, and restart service (upgrade or update)
  uninstall    Stop and remove the auto-start service registration (preserves data directory)
  start        Start the DBHarbor service
  stop         Stop the DBHarbor service
  restart      Restart the DBHarbor service
  status       Display current service status
  logs         Tail service runtime logs

Options:
  --bin <path>                    Path to local DBHarbor binary (skips GitHub download)
  --install-dir <dir>             Target installation directory for binary (and ./data when --data-dir is omitted)
  --download                      Force downloading binary from GitHub Releases even if a local binary exists
  --version <tag>                 GitHub Release version to download (default: latest; e.g. v1.0.0 or preview)
  --channel <release|preview>     Release channel when --version is omitted (default: release)
  --mirror <url>                  GitHub download mirror prefix (e.g. https://ghfast.top)
  --repo <owner/repo>             GitHub repository for release downloads (default: Willh92/DBHarbor)
  --no-verify                     Skip SHA256SUMS.txt verification when downloading from GitHub
  --host <addr>                   Listen host address (default: 0.0.0.0)
  --port <port>                   Listen HTTP port (default: 8787)
  --unix-socket <path>            Listen on Unix domain socket instead of TCP (e.g. /run/dbharbor/dbharbor.sock)
  --unix-socket-perm <octal>      Octal permissions for Unix domain socket (default: 0660)
  --socket-group <group>          Group ownership applied to Unix domain socket (e.g. www-data or nginx)
  --data-dir <dir>                Persistent metadata & encryption key directory
  --data-dir-mode <octal>         Octal permission mask for --data-dir (default: 0750)
  --user <username>               Service execution user / role (Linux default: dbharbor; optional on macOS)
  --group <groupname>             Primary permission group for service & data dir (Linux default: same as --user)
  --supplementary-groups <g1,g2>  Comma-separated supplementary groups for systemd service (e.g. www-data,docker)
  --admin-user <username>         Initial admin username when bootstrapping a fresh data directory (default: admin)
  --admin-pass <password>         Initial admin password when bootstrapping a fresh data directory (min 8 chars)
  --user-mode                     Install as current non-root user service (systemd --user / LaunchAgents)
  -h, --help                      Show this help message
EOF
}

if [ "$#" -gt 0 ]; then
  case "$1" in
    install|upgrade|update|uninstall|start|stop|restart|status|logs)
      ACTION="$1"
      [ "$ACTION" = "update" ] && ACTION="upgrade"
      shift
      ;;
    -h|--help|help)
      usage
      exit 0
      ;;
  esac
fi

while [ "$#" -gt 0 ]; do
  case "$1" in
    --bin)
      BIN_SRC="$2"
      shift 2
      ;;
    --bin=*)
      BIN_SRC="${1#*=}"
      shift 1
      ;;
    --install-dir)
      INSTALL_DIR="$2"
      shift 2
      ;;
    --install-dir=*)
      INSTALL_DIR="${1#*=}"
      shift 1
      ;;
    --download)
      FORCE_DOWNLOAD="1"
      shift 1
      ;;
    --version)
      RELEASE_VERSION="$2"
      shift 2
      ;;
    --version=*)
      RELEASE_VERSION="${1#*=}"
      shift 1
      ;;
    --channel)
      RELEASE_CHANNEL="$2"
      shift 2
      ;;
    --channel=*)
      RELEASE_CHANNEL="${1#*=}"
      shift 1
      ;;
    --mirror)
      GITHUB_MIRROR="$2"
      shift 2
      ;;
    --mirror=*)
      GITHUB_MIRROR="${1#*=}"
      shift 1
      ;;
    --repo)
      GITHUB_REPO="$2"
      shift 2
      ;;
    --repo=*)
      GITHUB_REPO="${1#*=}"
      shift 1
      ;;
    --no-verify)
      SKIP_VERIFY="1"
      shift 1
      ;;
    --host)
      HOST_ADDR="$2"
      shift 2
      ;;
    --host=*)
      HOST_ADDR="${1#*=}"
      shift 1
      ;;
    --port)
      PORT="$2"
      shift 2
      ;;
    --port=*)
      PORT="${1#*=}"
      shift 1
      ;;
    --unix-socket)
      UNIX_SOCKET="$2"
      shift 2
      ;;
    --unix-socket=*)
      UNIX_SOCKET="${1#*=}"
      shift 1
      ;;
    --unix-socket-perm)
      UNIX_SOCKET_PERM="$2"
      shift 2
      ;;
    --unix-socket-perm=*)
      UNIX_SOCKET_PERM="${1#*=}"
      shift 1
      ;;
    --socket-group)
      SOCKET_GROUP="$2"
      shift 2
      ;;
    --socket-group=*)
      SOCKET_GROUP="${1#*=}"
      shift 1
      ;;
    --data-dir)
      DATA_DIR="$2"
      shift 2
      ;;
    --data-dir=*)
      DATA_DIR="${1#*=}"
      shift 1
      ;;
    --data-dir-mode)
      DATA_DIR_MODE="$2"
      shift 2
      ;;
    --data-dir-mode=*)
      DATA_DIR_MODE="${1#*=}"
      shift 1
      ;;
    --user)
      SVC_USER="$2"
      shift 2
      ;;
    --user=*)
      SVC_USER="${1#*=}"
      shift 1
      ;;
    --group)
      SVC_GROUP="$2"
      shift 2
      ;;
    --group=*)
      SVC_GROUP="${1#*=}"
      shift 1
      ;;
    --supplementary-groups)
      SUPP_GROUPS="$2"
      shift 2
      ;;
    --supplementary-groups=*)
      SUPP_GROUPS="${1#*=}"
      shift 1
      ;;
    --admin-user)
      ADMIN_USER="$2"
      shift 2
      ;;
    --admin-user=*)
      ADMIN_USER="${1#*=}"
      shift 1
      ;;
    --admin-pass)
      ADMIN_PASS="$2"
      shift 2
      ;;
    --admin-pass=*)
      ADMIN_PASS="${1#*=}"
      shift 1
      ;;
    --user-mode)
      USER_MODE="1"
      shift 1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      log_err "Unknown argument: $1"
      usage
      exit 1
      ;;
  esac
done

OS_RAW="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH_RAW="$(uname -m)"

case "$OS_RAW" in
  linux*)  TARGET_OS="linux" ;;
  darwin*) TARGET_OS="darwin" ;;
  *)
    log_err "Unsupported OS: $OS_RAW (use scripts/install-service.ps1 on Windows)"
    exit 1
    ;;
esac

case "$ARCH_RAW" in
  x86_64|amd64)  TARGET_ARCH="amd64" ;;
  aarch64|arm64) TARGET_ARCH="arm64" ;;
  *)
    log_err "Unsupported CPU architecture: $ARCH_RAW"
    exit 1
    ;;
esac

IS_ROOT="0"
if [ "$(id -u)" -eq 0 ]; then
  IS_ROOT="1"
fi

if [ "$TARGET_OS" = "darwin" ] && [ "$IS_ROOT" = "0" ] && [ "$USER_MODE" = "0" ]; then
  log_warn "Not running as root on macOS; switching to --user-mode (~/Library/LaunchAgents)."
  USER_MODE="1"
fi

if [ "$TARGET_OS" = "linux" ] && [ "$IS_ROOT" = "0" ] && [ "$USER_MODE" = "0" ]; then
  log_err "System-wide service management on Linux requires root (sudo), or pass --user-mode."
  exit 1
fi

if [ -f "$0" ]; then
  SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd || pwd)"
else
  SCRIPT_DIR="$(pwd)"
fi

normalize_dir_path() {
  RAW_P="$1"
  case "$RAW_P" in
    /) printf "/" ;;
    /*) printf "%s" "${RAW_P%/}" ;;
    *)
      if [ -d "$RAW_P" ]; then
        (CDPATH= cd -- "$RAW_P" && pwd)
      else
        CLEAN_REL="${RAW_P#./}"
        printf "%s/%s" "$(pwd)" "${CLEAN_REL%/}"
      fi
      ;;
  esac
}

is_standard_bin_dir() {
  case "$1" in
    /bin|/sbin|/usr/bin|/usr/sbin|/usr/local/bin|/usr/local/sbin|*/.local/bin|*/bin|*/sbin)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

copy_binary_safe() {
  SRC_BIN="$1"
  DST_BIN="$2"
  DST_DIR="$(dirname "$DST_BIN")"
  mkdir -p "$DST_DIR"
  chmod 0755 "$DST_DIR" 2>/dev/null || true
  SRC_ABS="$(CDPATH= cd -- "$(dirname "$SRC_BIN")" && pwd)/$(basename "$SRC_BIN")"
  DST_ABS="$(CDPATH= cd -- "$DST_DIR" && pwd)/$(basename "$DST_BIN")"
  if [ "$SRC_ABS" != "$DST_ABS" ]; then
    TMP_DST="${DST_BIN}.new.$$"
    cp -f "$SRC_BIN" "$TMP_DST"
    chmod 0755 "$TMP_DST"
    mv -f "$TMP_DST" "$DST_BIN"
  else
    chmod 0755 "$DST_BIN"
  fi
}

build_release_base_url() {
  REL_REF="$1"
  if [ "$REL_REF" = "latest" ]; then
    RAW_BASE="https://github.com/${GITHUB_REPO}/releases/latest/download"
  else
    RAW_BASE="https://github.com/${GITHUB_REPO}/releases/download/${REL_REF}"
  fi
  if [ -n "$GITHUB_MIRROR" ]; then
    printf "%s/%s" "${GITHUB_MIRROR%/}" "$RAW_BASE"
  else
    printf "%s" "$RAW_BASE"
  fi
}

fetch_url() {
  DL_URL="$1"
  DL_DEST="$2"
  DL_QUIET="$3"
  if command -v curl >/dev/null 2>&1; then
    if [ "$DL_QUIET" = "1" ]; then
      curl -fsSL --retry 3 --connect-timeout 15 -o "$DL_DEST" "$DL_URL"
    else
      curl -fL --progress-bar --retry 3 --connect-timeout 15 -o "$DL_DEST" "$DL_URL"
    fi
  elif command -v wget >/dev/null 2>&1; then
    if [ "$DL_QUIET" = "1" ]; then
      wget -q --tries=3 --timeout=15 -O "$DL_DEST" "$DL_URL"
    else
      wget --tries=3 --timeout=15 -O "$DL_DEST" "$DL_URL"
    fi
  else
    log_err "Neither 'curl' nor 'wget' is installed. Install curl/wget or pass a local binary via --bin <path>."
    exit 1
  fi
}

compute_sha256() {
  FILE_TO_HASH="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$FILE_TO_HASH" | awk '{print tolower($1)}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$FILE_TO_HASH" | awk '{print tolower($1)}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$FILE_TO_HASH" | awk '{print tolower($NF)}'
  else
    printf ""
  fi
}

download_release_binary() {
  ASSET_NAME="$1"
  PINNED="0"
  if [ -n "$RELEASE_VERSION" ]; then
    case "$RELEASE_VERSION" in
      latest)
        PRIMARY_TARGET="latest"
        ;;
      preview)
        PRIMARY_TARGET="preview"
        PINNED="1"
        ;;
      v*|V*)
        PRIMARY_TARGET="v${RELEASE_VERSION#[vV]}"
        PINNED="1"
        ;;
      *)
        PRIMARY_TARGET="v${RELEASE_VERSION}"
        PINNED="1"
        ;;
    esac
  else
    case "$RELEASE_CHANNEL" in
      preview)
        PRIMARY_TARGET="preview"
        PINNED="1"
        ;;
      release|"")
        PRIMARY_TARGET="latest"
        ;;
      *)
        log_err "Invalid --channel '${RELEASE_CHANNEL}' (expected 'release' or 'preview')"
        exit 1
        ;;
    esac
  fi

  TMP_DOWNLOAD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/dbharbor-dl.XXXXXX")"
  BASE_URL="$(build_release_base_url "$PRIMARY_TARGET")"
  BIN_URL="${BASE_URL}/${ASSET_NAME}"
  SUM_URL="${BASE_URL}/SHA256SUMS.txt"
  DL_BIN="${TMP_DOWNLOAD_DIR}/${ASSET_NAME}"
  DL_SUM="${TMP_DOWNLOAD_DIR}/SHA256SUMS.txt"

  log_info "Downloading ${ASSET_NAME} from GitHub Releases (${GITHUB_REPO} @ ${PRIMARY_TARGET})..."
  if ! fetch_url "$BIN_URL" "$DL_BIN" "0"; then
    if [ "$PRIMARY_TARGET" = "latest" ] && [ "$PINNED" = "0" ]; then
      log_warn "No stable 'latest' release found; falling back to rolling 'preview' release..."
      PRIMARY_TARGET="preview"
      BASE_URL="$(build_release_base_url "preview")"
      BIN_URL="${BASE_URL}/${ASSET_NAME}"
      SUM_URL="${BASE_URL}/SHA256SUMS.txt"
      if ! fetch_url "$BIN_URL" "$DL_BIN" "0"; then
        log_err "Failed to download ${ASSET_NAME} from ${BIN_URL}"
        exit 1
      fi
    else
      log_err "Failed to download ${ASSET_NAME} from ${BIN_URL}"
      exit 1
    fi
  fi

  if [ ! -s "$DL_BIN" ]; then
    log_err "Downloaded binary is empty: ${DL_BIN}"
    exit 1
  fi

  if [ "$SKIP_VERIFY" != "1" ]; then
    if ! fetch_url "$SUM_URL" "$DL_SUM" "1" || [ ! -s "$DL_SUM" ]; then
      log_err "Failed to fetch SHA256SUMS.txt from ${SUM_URL} (refusing to install unverified binary; pass --no-verify only when using a custom mirror without checksums)."
      exit 1
    fi
    EXPECTED_SHA="$(awk -v f="$ASSET_NAME" '$2 == f || $2 == ("*" f) { print tolower($1); exit }' "$DL_SUM")"
    if [ -z "$EXPECTED_SHA" ]; then
      log_err "Entry for ${ASSET_NAME} not found in SHA256SUMS.txt from ${SUM_URL} (refusing to install unverified binary; pass --no-verify only when using a custom mirror without checksums)."
      exit 1
    fi
    ACTUAL_SHA="$(compute_sha256 "$DL_BIN")"
    if [ -z "$ACTUAL_SHA" ]; then
      log_err "No SHA-256 utility (sha256sum, shasum, or openssl) found on host; install one or pass --no-verify."
      exit 1
    fi
    if [ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]; then
      log_err "SHA-256 mismatch for ${ASSET_NAME}! Expected ${EXPECTED_SHA}, got ${ACTUAL_SHA}"
      exit 1
    fi
    log_ok "Verified SHA-256 checksum (${ACTUAL_SHA})"
  else
    log_warn "Skipping SHA-256 checksum verification (--no-verify specified)."
  fi

  chmod 0755 "$DL_BIN"
  if [ "$TARGET_OS" = "darwin" ]; then
    xattr -d com.apple.quarantine "$DL_BIN" >/dev/null 2>&1 || true
  fi
  case "$ASSET_NAME" in
    "dbharbor-${TARGET_OS}-${TARGET_ARCH}")
      if ! "$DL_BIN" -version >/dev/null 2>&1; then
        log_err "Downloaded binary failed smoke check ('${DL_BIN} -version'); refusing to overwrite existing service binary."
        exit 1
      fi
      ;;
  esac
  RESOLVED_BIN_PATH="$DL_BIN"
}

resolve_source_binary() {
  if [ -n "$BIN_SRC" ]; then
    if [ ! -f "$BIN_SRC" ]; then
      log_err "Specified binary not found: $BIN_SRC"
      exit 1
    fi
    RESOLVED_BIN_PATH="$BIN_SRC"
    return
  fi

  CANDIDATE_NAME="dbharbor-${TARGET_OS}-${TARGET_ARCH}"
  if [ "$FORCE_DOWNLOAD" = "1" ] || [ -n "$RELEASE_VERSION" ] || [ "$ACTION" = "upgrade" ]; then
    download_release_binary "$CANDIDATE_NAME"
    return
  fi

  for cand in \
    "${SCRIPT_DIR}/${CANDIDATE_NAME}" \
    "${SCRIPT_DIR}/dbharbor" \
    "${SCRIPT_DIR}/../dist/${CANDIDATE_NAME}" \
    "./${CANDIDATE_NAME}" \
    "./dist/${CANDIDATE_NAME}"
  do
    if [ -n "$cand" ] && [ -f "$cand" ]; then
      log_info "Using local binary: ${cand}"
      RESOLVED_BIN_PATH="$cand"
      return
    fi
  done

  log_info "No local binary (${CANDIDATE_NAME}) found; auto-downloading from GitHub Releases..."
  download_release_binary "$CANDIDATE_NAME"
}

extract_systemd_exec_bin() {
  U_FILE="$1"
  if [ -f "$U_FILE" ]; then
    awk '
      /^ExecStart=/ {
        line = substr($0, 11)
        sub(/^[ \t@+-]+/, "", line)
        if (substr(line, 1, 1) == "\"") {
          out = ""
          i = 2
          len = length(line)
          while (i <= len) {
            ch = substr(line, i, 1)
            if (ch == "\\" && i < len) {
              out = out substr(line, i + 1, 1)
              i += 2
              continue
            }
            if (ch == "\"") {
              break
            }
            out = out ch
            i++
          }
          print out
          exit
        } else if (substr(line, 1, 1) == "\047") {
          line = substr(line, 2)
          sub(/\047.*$/, "", line)
          print line
          exit
        } else {
          sub(/[ \t].*$/, "", line)
          print line
          exit
        }
      }
    ' "$U_FILE"
  fi
}

extract_macos_plist_bin() {
  P_FILE="$1"
  if [ ! -f "$P_FILE" ]; then
    return 0
  fi
  PLIST_XML=""
  if command -v plutil >/dev/null 2>&1; then
    PLIST_XML="$(plutil -convert xml1 -o - "$P_FILE" 2>/dev/null || true)"
  fi
  if [ -z "$PLIST_XML" ]; then
    PLIST_XML="$(cat "$P_FILE" 2>/dev/null || true)"
  fi
  printf "%s\n" "$PLIST_XML" | awk '
    {
      line = $0
      if (!in_prog && match(line, /<key>[ \t]*ProgramArguments[ \t]*<\/key>/)) {
        in_prog = 1
        line = substr(line, RSTART + RLENGTH)
      }
      if (in_prog && !in_arr && match(line, /<array>/)) {
        in_arr = 1
        line = substr(line, RSTART + RLENGTH)
      }
      if (in_arr && match(line, /<string>[^<]+<\/string>/)) {
        val = substr(line, RSTART + 8, RLENGTH - 17)
        gsub(/&lt;/, "<", val)
        gsub(/&gt;/, ">", val)
        gsub(/&quot;/, "\"", val)
        gsub(/&apos;/, "\047", val)
        gsub(/&amp;/, "\\&", val)
        print val
        exit
      }
      if (in_arr && /<\/array>/) {
        exit
      }
    }
  '
}

validate_admin_bootstrap() {
  NL='
'
  CR="$(printf '\r')"
  if [ -n "$ADMIN_USER" ] && [ -z "$ADMIN_PASS" ]; then
    log_err "Invalid parameters: --admin-pass (at least 8 characters) is required when --admin-user is specified."
    exit 1
  fi
  if [ -n "$ADMIN_USER" ]; then
    case "$ADMIN_USER" in
      *"$NL"*|*"$CR"*)
        log_err "Invalid --admin-user: control characters are not allowed."
        exit 1
        ;;
    esac
    if ! printf "%s\n" "$ADMIN_USER" | grep -Eq '^[a-zA-Z0-9._@-]{2,64}$'; then
      log_err "Invalid --admin-user '${ADMIN_USER}': must be 2-64 characters using letters, numbers, '.', '_', '-', or '@'."
      exit 1
    fi
  fi
  if [ -n "$ADMIN_PASS" ]; then
    case "$ADMIN_PASS" in
      *"$NL"*|*"$CR"*)
        log_err "Invalid --admin-pass: newline characters are not allowed."
        exit 1
        ;;
    esac
    if [ "${#ADMIN_PASS}" -lt 8 ]; then
      log_err "Invalid --admin-pass: initial admin password must be at least 8 characters."
      exit 1
    fi
  fi
}

systemd_escape_dq() {
  printf "%s" "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

systemd_escape_env_sq() {
  printf "%s" "$1" | sed "s/\\\\/\\\\\\\\/g; s/'/\\\\'/g"
}

xml_escape() {
  printf "%s" "$1" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g'
}

format_systemd_arg() {
  case "$1" in
    *[[:space:]\"\\]*) printf '"%s"' "$(systemd_escape_dq "$1")" ;;
    *) printf '%s' "$1" ;;
  esac
}

sh_escape_sq() {
  printf "%s" "$1" | sed "s/'/'\\\\''/g"
}

prepare_systemd_bootstrap_env() {
  ENV_PATH="$1"
  OWNER_SPEC="$2"
  rm -f "$ENV_PATH" 2>/dev/null || true
  if [ -z "$ADMIN_USER" ] && [ -z "$ADMIN_PASS" ]; then
    return 1
  fi
  if [ -s "${DATA_DIR}/dbharbor.db" ]; then
    log_warn "Existing database detected at ${DATA_DIR}/dbharbor.db; --admin-user / --admin-pass only applies when initializing a fresh data directory."
    return 1
  fi
  BOOTSTRAP_ENV_FILE="$ENV_PATH"
  OLD_UMASK="$(umask)"
  umask 077
  {
    [ -n "$ADMIN_USER" ] && printf "DBHARBOR_ADMIN_USER='%s'\n" "$(systemd_escape_env_sq "$ADMIN_USER")"
    [ -n "$ADMIN_PASS" ] && printf "DBHARBOR_ADMIN_PASS='%s'\n" "$(systemd_escape_env_sq "$ADMIN_PASS")"
  } > "$ENV_PATH"
  umask "$OLD_UMASK"
  chmod 0600 "$ENV_PATH"
  if [ -n "$OWNER_SPEC" ]; then
    chown "$OWNER_SPEC" "$ENV_PATH" 2>/dev/null || true
  fi
  return 0
}

prepare_openrc_bootstrap_env() {
  ENV_PATH="$1"
  OWNER_SPEC="$2"
  rm -f "$ENV_PATH" 2>/dev/null || true
  if [ -z "$ADMIN_USER" ] && [ -z "$ADMIN_PASS" ]; then
    return 1
  fi
  if [ -s "${DATA_DIR}/dbharbor.db" ]; then
    log_warn "Existing database detected at ${DATA_DIR}/dbharbor.db; --admin-user / --admin-pass only applies when initializing a fresh data directory."
    return 1
  fi
  BOOTSTRAP_ENV_FILE="$ENV_PATH"
  OLD_UMASK="$(umask)"
  umask 077
  {
    [ -n "$ADMIN_USER" ] && printf "export DBHARBOR_ADMIN_USER='%s'\n" "$(sh_escape_sq "$ADMIN_USER")"
    [ -n "$ADMIN_PASS" ] && printf "export DBHARBOR_ADMIN_PASS='%s'\n" "$(sh_escape_sq "$ADMIN_PASS")"
  } > "$ENV_PATH"
  umask "$OLD_UMASK"
  chmod 0600 "$ENV_PATH"
  if [ -n "$OWNER_SPEC" ]; then
    chown "$OWNER_SPEC" "$ENV_PATH" 2>/dev/null || true
  fi
  return 0
}

finalize_bootstrap_env() {
  ENV_PATH="$1"
  [ ! -f "$ENV_PATH" ] && return 0
  WAIT_I=0
  while [ "$WAIT_I" -lt 10 ]; do
    if [ -s "${DATA_DIR}/dbharbor.db" ]; then
      break
    fi
    sleep 1
    WAIT_I=$((WAIT_I + 1))
  done
  rm -f "$ENV_PATH" 2>/dev/null || true
  BOOTSTRAP_ENV_FILE=""
  if [ -s "${DATA_DIR}/dbharbor.db" ]; then
    log_ok "Initialized administrator account (${ADMIN_USER:-admin}) and wiped temporary bootstrap credentials."
  else
    log_warn "Removed temporary bootstrap file before ${DATA_DIR}/dbharbor.db appeared; inspect service logs if login fails."
  fi
}

bootstrap_direct_admin_store() {
  BIN_EXE="$1"
  TARGET_DATA="$2"
  if [ -z "$ADMIN_USER" ] && [ -z "$ADMIN_PASS" ]; then
    return 0
  fi
  if [ -z "$ADMIN_PASS" ]; then
    log_err "Invalid parameters: --admin-pass (at least 8 characters) is required when initializing an administrator account."
    exit 1
  fi
  if [ -s "${TARGET_DATA}/dbharbor.db" ]; then
    log_warn "Existing database detected at ${TARGET_DATA}/dbharbor.db; --admin-user / --admin-pass only applies when initializing a fresh data directory."
    return 0
  fi
  EFF_USER="${ADMIN_USER:-admin}"
  EFF_PASS="$ADMIN_PASS"
  if "$BIN_EXE" -h 2>&1 | grep -q "init-only"; then
    DBHARBOR_ADMIN_USER="$EFF_USER" DBHARBOR_ADMIN_PASS="$EFF_PASS" "$BIN_EXE" -data-dir "$TARGET_DATA" -init-only >/dev/null
  else
    DBHARBOR_ADMIN_USER="$EFF_USER" DBHARBOR_ADMIN_PASS="$EFF_PASS" "$BIN_EXE" -host 127.0.0.1 -port 0 -data-dir "$TARGET_DATA" >/dev/null 2>&1 &
    BOOT_PID=$!
    WAIT_I=0
    while [ "$WAIT_I" -lt 10 ]; do
      if [ -s "${TARGET_DATA}/dbharbor.db" ]; then
        break
      fi
      sleep 1
      WAIT_I=$((WAIT_I + 1))
    done
    kill "$BOOT_PID" 2>/dev/null || true
    wait "$BOOT_PID" 2>/dev/null || true
  fi
  log_ok "Initialized administrator account (${EFF_USER}) in ${TARGET_DATA}"
}

validate_admin_bootstrap

ensure_linux_role_and_group() {
  TARGET_U="$1"
  TARGET_G="$2"

  if ! grep -q "^${TARGET_G}:" /etc/group 2>/dev/null; then
    log_info "Creating system permission group: ${TARGET_G}"
    if command -v groupadd >/dev/null 2>&1; then
      groupadd --system "$TARGET_G" || groupadd "$TARGET_G"
    elif command -v addgroup >/dev/null 2>&1; then
      addgroup -S "$TARGET_G" || addgroup "$TARGET_G"
    fi
  fi

  if ! id -u "$TARGET_U" >/dev/null 2>&1; then
    log_info "Creating dedicated service account: ${TARGET_U} (group: ${TARGET_G})"
    if command -v useradd >/dev/null 2>&1; then
      useradd --system --gid "$TARGET_G" --no-create-home --shell /usr/sbin/nologin "$TARGET_U" \
        || useradd -r -g "$TARGET_G" -s /bin/false "$TARGET_U"
    elif command -v adduser >/dev/null 2>&1; then
      adduser -S -D -H -G "$TARGET_G" -s /sbin/nologin "$TARGET_U"
    fi
  fi
}

# ==============================================================================
# Linux Implementation (systemd & OpenRC)
# ==============================================================================
run_linux() {
  [ -z "$SVC_USER" ] && SVC_USER="dbharbor"
  [ -z "$SVC_GROUP" ] && SVC_GROUP="$SVC_USER"

  HAS_SYSTEMD="0"
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    HAS_SYSTEMD="1"
  fi

  LISTEN_ARGS="-host ${HOST_ADDR} -port ${PORT}"
  LISTEN_DISPLAY="http://${HOST_ADDR}:${PORT}"
  PRIVATE_TMP_VAL="true"
  if [ -n "$UNIX_SOCKET" ]; then
    SOCK_GRP="${SOCKET_GROUP:-$SVC_GROUP}"
    LISTEN_ARGS="-unix-socket ${UNIX_SOCKET} -unix-socket-perm ${UNIX_SOCKET_PERM}"
    if [ -n "$SOCK_GRP" ] && [ "$USER_MODE" = "0" ]; then
      LISTEN_ARGS="${LISTEN_ARGS} -unix-socket-group ${SOCK_GRP}"
    fi
    LISTEN_DISPLAY="unix:${UNIX_SOCKET} (perm ${UNIX_SOCKET_PERM}, group ${SOCK_GRP})"
    case "$UNIX_SOCKET" in
      /tmp/*|/var/tmp/*) PRIVATE_TMP_VAL="false" ;;
    esac
  fi

  if [ "$USER_MODE" = "1" ]; then
    if [ -n "$INSTALL_DIR" ]; then
      INSTALL_DIR="$(normalize_dir_path "$INSTALL_DIR")"
      INSTALL_BIN="${INSTALL_DIR}/dbharbor"
      if [ -z "$DATA_DIR" ]; then
        if is_standard_bin_dir "$INSTALL_DIR"; then
          DATA_DIR="${XDG_DATA_HOME:-${HOME}/.local/share}/dbharbor"
        else
          DATA_DIR="${INSTALL_DIR}/data"
        fi
      fi
    else
      INSTALL_BIN="${HOME}/.local/bin/dbharbor"
      [ -z "$DATA_DIR" ] && DATA_DIR="${XDG_DATA_HOME:-${HOME}/.local/share}/dbharbor"
    fi
    DATA_DIR="$(normalize_dir_path "$DATA_DIR")"
    UNIT_DIR="${HOME}/.config/systemd/user"
    UNIT_FILE="${UNIT_DIR}/${SERVICE_NAME}.service"

    case "$ACTION" in
      install)
        resolve_source_binary
        mkdir -p "$DATA_DIR" "$UNIT_DIR"
        [ -n "$UNIX_SOCKET" ] && mkdir -p "$(dirname "$UNIX_SOCKET")"
        chmod "$DATA_DIR_MODE" "$DATA_DIR" 2>/dev/null || true
        copy_binary_safe "$RESOLVED_BIN_PATH" "$INSTALL_BIN"

        INIT_ENV_FILE="${DATA_DIR}/.dbharbor-init.env"
        HAS_BOOT_ENV="0"
        if prepare_systemd_bootstrap_env "$INIT_ENV_FILE" ""; then
          HAS_BOOT_ENV="1"
        fi

        cat > "$UNIT_FILE" <<EOF
[Unit]
Description=DBHarbor Multi-Database Web Workbench (User Service)
Documentation=https://github.com/Willh92/DBHarbor
After=network-online.target

[Service]
Type=simple
EnvironmentFile=-$(format_systemd_arg "$INIT_ENV_FILE")
ExecStart=$(format_systemd_arg "$INSTALL_BIN") ${LISTEN_ARGS} -data-dir $(format_systemd_arg "$DATA_DIR")
WorkingDirectory=$(format_systemd_arg "$DATA_DIR")
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=default.target
EOF
        systemctl --user daemon-reload
        systemctl --user enable "${SERVICE_NAME}.service"
        systemctl --user restart "${SERVICE_NAME}.service"
        [ "$HAS_BOOT_ENV" = "1" ] && finalize_bootstrap_env "$INIT_ENV_FILE"
        log_ok "Installed & started user systemd service: ${SERVICE_NAME}.service"
        log_info "Binary   : ${INSTALL_BIN}"
        log_info "Listen   : ${LISTEN_DISPLAY}"
        log_info "Data Dir : ${DATA_DIR}"
        log_info "Tip: Run 'sudo loginctl enable-linger $(whoami)' to keep user service running before login."
        ;;
      upgrade)
        if [ -z "$INSTALL_DIR" ] && [ -f "$UNIT_FILE" ]; then
          EXISTING_BIN="$(extract_systemd_exec_bin "$UNIT_FILE")"
          [ -n "$EXISTING_BIN" ] && INSTALL_BIN="$EXISTING_BIN"
        fi
        resolve_source_binary
        copy_binary_safe "$RESOLVED_BIN_PATH" "$INSTALL_BIN"
        if [ -f "$UNIT_FILE" ]; then
          systemctl --user daemon-reload
          systemctl --user restart "${SERVICE_NAME}.service"
          log_ok "Upgraded & restarted user service (${INSTALL_BIN}): $("$INSTALL_BIN" -version 2>/dev/null || printf "OK")"
        else
          log_ok "Upgraded binary at ${INSTALL_BIN} (service unit not yet registered; run 'install' to register)."
        fi
        ;;
      uninstall)
        systemctl --user disable --now "${SERVICE_NAME}.service" >/dev/null 2>&1 || true
        rm -f "$UNIT_FILE"
        systemctl --user daemon-reload || true
        log_ok "Uninstalled user service ${SERVICE_NAME} (data preserved at ${DATA_DIR})."
        ;;
      start|stop|restart|status)
        systemctl --user "$ACTION" "${SERVICE_NAME}.service"
        ;;
      logs)
        journalctl --user -u "${SERVICE_NAME}.service" -f -n 100
        ;;
    esac
    return
  fi

  # System-wide Linux mode (root)
  if [ -n "$INSTALL_DIR" ]; then
    INSTALL_DIR="$(normalize_dir_path "$INSTALL_DIR")"
    INSTALL_BIN="${INSTALL_DIR}/dbharbor"
    if [ -z "$DATA_DIR" ]; then
      if is_standard_bin_dir "$INSTALL_DIR"; then
        DATA_DIR="/var/lib/dbharbor"
      else
        DATA_DIR="${INSTALL_DIR}/data"
      fi
    fi
  else
    INSTALL_BIN="/usr/local/bin/dbharbor"
    [ -z "$DATA_DIR" ] && DATA_DIR="/var/lib/dbharbor"
  fi
  DATA_DIR="$(normalize_dir_path "$DATA_DIR")"

  if [ "$HAS_SYSTEMD" = "1" ]; then
    UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
    case "$ACTION" in
      install)
        resolve_source_binary
        ensure_linux_role_and_group "$SVC_USER" "$SVC_GROUP"

        mkdir -p "$DATA_DIR"
        if [ -n "$UNIX_SOCKET" ]; then
          SOCK_PARENT="$(dirname "$UNIX_SOCKET")"
          mkdir -p "$SOCK_PARENT"
          chown "${SVC_USER}:${SOCKET_GROUP:-$SVC_GROUP}" "$SOCK_PARENT" 2>/dev/null || true
          chmod 0755 "$SOCK_PARENT" 2>/dev/null || true
        fi
        copy_binary_safe "$RESOLVED_BIN_PATH" "$INSTALL_BIN"
        chown -R "${SVC_USER}:${SVC_GROUP}" "$DATA_DIR"
        chmod "$DATA_DIR_MODE" "$DATA_DIR"

        INIT_ENV_FILE="${DATA_DIR}/.dbharbor-init.env"
        HAS_BOOT_ENV="0"
        if prepare_systemd_bootstrap_env "$INIT_ENV_FILE" "${SVC_USER}:${SVC_GROUP}"; then
          HAS_BOOT_ENV="1"
        fi

        SUPP_LINE=""
        if [ -n "$SUPP_GROUPS" ]; then
          # systemd SupplementaryGroups accepts space-separated group names
          SUPP_SPACED="$(printf "%s" "$SUPP_GROUPS" | tr ',' ' ')"
          SUPP_LINE="SupplementaryGroups=${SUPP_SPACED}"
        elif [ -n "$SOCKET_GROUP" ] && [ "$SOCKET_GROUP" != "$SVC_GROUP" ]; then
          SUPP_LINE="SupplementaryGroups=${SOCKET_GROUP}"
        fi

        cat > "$UNIT_FILE" <<EOF
[Unit]
Description=DBHarbor Cross-Platform Multi-Database Web Workbench
Documentation=https://github.com/Willh92/DBHarbor
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SVC_USER}
Group=${SVC_GROUP}
${SUPP_LINE}
RuntimeDirectory=${SERVICE_NAME}
RuntimeDirectoryMode=0755
WorkingDirectory=$(format_systemd_arg "$DATA_DIR")
EnvironmentFile=-$(format_systemd_arg "$INIT_ENV_FILE")
ExecStart=$(format_systemd_arg "$INSTALL_BIN") ${LISTEN_ARGS} -data-dir $(format_systemd_arg "$DATA_DIR")
Restart=on-failure
RestartSec=5
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=${PRIVATE_TMP_VAL}

[Install]
WantedBy=multi-user.target
EOF
        chmod 0644 "$UNIT_FILE"
        systemctl daemon-reload
        systemctl enable "${SERVICE_NAME}.service"
        systemctl restart "${SERVICE_NAME}.service"
        [ "$HAS_BOOT_ENV" = "1" ] && finalize_bootstrap_env "$INIT_ENV_FILE"
        log_ok "DBHarbor systemd service installed and started!"
        log_info "Binary   : ${INSTALL_BIN}"
        log_info "Role     : User=${SVC_USER}, Group=${SVC_GROUP}${SUPP_GROUPS:+ (Supplementary=${SUPP_GROUPS})}"
        log_info "Data Dir : ${DATA_DIR} (mode ${DATA_DIR_MODE})"
        log_info "Listen   : ${LISTEN_DISPLAY}"
        ;;
      upgrade)
        if [ -z "$INSTALL_DIR" ] && [ -f "$UNIT_FILE" ]; then
          EXISTING_BIN="$(extract_systemd_exec_bin "$UNIT_FILE")"
          [ -n "$EXISTING_BIN" ] && INSTALL_BIN="$EXISTING_BIN"
        fi
        resolve_source_binary
        copy_binary_safe "$RESOLVED_BIN_PATH" "$INSTALL_BIN"
        if [ -f "$UNIT_FILE" ]; then
          systemctl daemon-reload
          systemctl restart "${SERVICE_NAME}.service"
          log_ok "Upgraded & restarted ${SERVICE_NAME}.service (${INSTALL_BIN}): $("$INSTALL_BIN" -version 2>/dev/null || printf "OK")"
        else
          log_ok "Upgraded binary at ${INSTALL_BIN} (service unit not yet registered; run 'install' to register)."
        fi
        ;;
      uninstall)
        systemctl disable --now "${SERVICE_NAME}.service" >/dev/null 2>&1 || true
        rm -f "$UNIT_FILE"
        systemctl daemon-reload
        log_ok "Removed systemd unit ${UNIT_FILE} (data preserved in ${DATA_DIR})."
        ;;
      start|stop|restart|status)
        systemctl "$ACTION" "${SERVICE_NAME}.service"
        ;;
      logs)
        journalctl -u "${SERVICE_NAME}.service" -f -n 100
        ;;
    esac
  elif command -v rc-service >/dev/null 2>&1; then
    INIT_SCRIPT="/etc/init.d/${SERVICE_NAME}"
    LOG_FILE="/var/log/dbharbor.log"
    case "$ACTION" in
      install)
        resolve_source_binary
        ensure_linux_role_and_group "$SVC_USER" "$SVC_GROUP"
        mkdir -p "$DATA_DIR"
        copy_binary_safe "$RESOLVED_BIN_PATH" "$INSTALL_BIN"
        chown -R "${SVC_USER}:${SVC_GROUP}" "$DATA_DIR"
        chmod "$DATA_DIR_MODE" "$DATA_DIR"
        touch "$LOG_FILE"
        chown "${SVC_USER}:${SVC_GROUP}" "$LOG_FILE" 2>/dev/null || true

        INIT_ENV_FILE="${DATA_DIR}/.dbharbor-init.env"
        HAS_BOOT_ENV="0"
        if prepare_openrc_bootstrap_env "$INIT_ENV_FILE" "${SVC_USER}:${SVC_GROUP}"; then
          HAS_BOOT_ENV="1"
        fi

        cat > "$INIT_SCRIPT" <<EOF
#!/sbin/openrc-run
name="DBHarbor"
description="DBHarbor Multi-Database Web Workbench"
command='$(sh_escape_sq "$INSTALL_BIN")'
command_args="${LISTEN_ARGS} -data-dir '$(sh_escape_sq "$DATA_DIR")'"
command_user='$(sh_escape_sq "${SVC_USER}:${SVC_GROUP}")'
command_background=true
pidfile='/run/$(sh_escape_sq "$SERVICE_NAME").pid'
output_log='$(sh_escape_sq "$LOG_FILE")'
error_log='$(sh_escape_sq "$LOG_FILE")'

if [ -f '$(sh_escape_sq "$INIT_ENV_FILE")' ]; then
  . '$(sh_escape_sq "$INIT_ENV_FILE")'
fi

depend() {
  need net
}
EOF
        chmod 0755 "$INIT_SCRIPT"
        rc-update add "$SERVICE_NAME" default
        rc-service "$SERVICE_NAME" restart || rc-service "$SERVICE_NAME" start
        [ "$HAS_BOOT_ENV" = "1" ] && finalize_bootstrap_env "$INIT_ENV_FILE"
        log_ok "DBHarbor OpenRC service installed and started!"
        log_info "Role     : ${SVC_USER}:${SVC_GROUP}"
        log_info "Listen   : ${LISTEN_DISPLAY}"
        ;;
      upgrade)
        if [ -z "$INSTALL_DIR" ] && [ -f "$INIT_SCRIPT" ]; then
          EXISTING_BIN="$(sed -n 's/^command="\([^"]*\)".*/\1/p' "$INIT_SCRIPT" | head -n 1)"
          [ -n "$EXISTING_BIN" ] && INSTALL_BIN="$EXISTING_BIN"
        fi
        resolve_source_binary
        copy_binary_safe "$RESOLVED_BIN_PATH" "$INSTALL_BIN"
        if [ -f "$INIT_SCRIPT" ]; then
          rc-service "$SERVICE_NAME" restart || rc-service "$SERVICE_NAME" start
          log_ok "Upgraded & restarted OpenRC service (${INSTALL_BIN}): $("$INSTALL_BIN" -version 2>/dev/null || printf "OK")"
        else
          log_ok "Upgraded binary at ${INSTALL_BIN}: $("$INSTALL_BIN" -version 2>/dev/null || printf "OK")"
        fi
        ;;
      uninstall)
        rc-service "$SERVICE_NAME" stop >/dev/null 2>&1 || true
        rc-update del "$SERVICE_NAME" default >/dev/null 2>&1 || true
        rm -f "$INIT_SCRIPT"
        log_ok "Removed OpenRC service ${SERVICE_NAME}."
        ;;
      start|stop|restart|status)
        rc-service "$SERVICE_NAME" "$ACTION"
        ;;
      logs)
        tail -n 100 -f "$LOG_FILE"
        ;;
    esac
  else
    log_err "Neither systemd nor OpenRC detected on this Linux host."
    exit 1
  fi
}

# ==============================================================================
# macOS Implementation (launchd LaunchDaemons & LaunchAgents)
# ==============================================================================
run_macos() {
  if [ "$USER_MODE" = "1" ]; then
    if [ -n "$INSTALL_DIR" ]; then
      INSTALL_DIR="$(normalize_dir_path "$INSTALL_DIR")"
      INSTALL_BIN="${INSTALL_DIR}/dbharbor"
      if [ -z "$DATA_DIR" ]; then
        if is_standard_bin_dir "$INSTALL_DIR"; then
          DATA_DIR="${HOME}/Library/Application Support/DBHarbor/data"
        else
          DATA_DIR="${INSTALL_DIR}/data"
        fi
      fi
    else
      INSTALL_BIN="${HOME}/.local/bin/dbharbor"
      [ -z "$DATA_DIR" ] && DATA_DIR="${HOME}/Library/Application Support/DBHarbor/data"
    fi
    LOG_DIR="${HOME}/Library/Logs/DBHarbor"
    PLIST_DIR="${HOME}/Library/LaunchAgents"
    DOMAIN_TARGET="gui/$(id -u)"
  else
    if [ -n "$INSTALL_DIR" ]; then
      INSTALL_DIR="$(normalize_dir_path "$INSTALL_DIR")"
      INSTALL_BIN="${INSTALL_DIR}/dbharbor"
      if [ -z "$DATA_DIR" ]; then
        if is_standard_bin_dir "$INSTALL_DIR"; then
          DATA_DIR="/Library/Application Support/DBHarbor/data"
        else
          DATA_DIR="${INSTALL_DIR}/data"
        fi
      fi
    else
      INSTALL_BIN="/usr/local/bin/dbharbor"
      [ -z "$DATA_DIR" ] && DATA_DIR="/Library/Application Support/DBHarbor/data"
    fi
    LOG_DIR="/var/log/dbharbor"
    PLIST_DIR="/Library/LaunchDaemons"
    DOMAIN_TARGET="system"
  fi
  DATA_DIR="$(normalize_dir_path "$DATA_DIR")"

  PLIST_FILE="${PLIST_DIR}/${MACOS_LABEL}.plist"
  OUT_LOG="${LOG_DIR}/dbharbor.log"
  ERR_LOG="${LOG_DIR}/dbharbor.err.log"

  case "$ACTION" in
    install)
      resolve_source_binary
      mkdir -p "$DATA_DIR" "$LOG_DIR" "$PLIST_DIR"
      copy_binary_safe "$RESOLVED_BIN_PATH" "$INSTALL_BIN"
      xattr -d com.apple.quarantine "$INSTALL_BIN" >/dev/null 2>&1 || true
      bootstrap_direct_admin_store "$INSTALL_BIN" "$DATA_DIR"
      chmod "$DATA_DIR_MODE" "$DATA_DIR" 2>/dev/null || true

      ROLE_PLIST_KEYS=""
      if [ "$USER_MODE" = "0" ]; then
        if [ -n "$SVC_USER" ]; then
          ROLE_PLIST_KEYS="  <key>UserName</key>
  <string>$(xml_escape "$SVC_USER")</string>"
          chown -R "$SVC_USER" "$DATA_DIR" "$LOG_DIR" 2>/dev/null || true
        fi
        if [ -n "$SVC_GROUP" ]; then
          ROLE_PLIST_KEYS="${ROLE_PLIST_KEYS}
  <key>GroupName</key>
  <string>$(xml_escape "$SVC_GROUP")</string>"
          chgrp -R "$SVC_GROUP" "$DATA_DIR" "$LOG_DIR" 2>/dev/null || true
        fi
      fi

      [ -n "$UNIX_SOCKET" ] && mkdir -p "$(dirname "$UNIX_SOCKET")"
      if [ -n "$UNIX_SOCKET" ]; then
        LISTEN_PLIST_ARGS="    <string>-unix-socket</string>
    <string>$(xml_escape "$UNIX_SOCKET")</string>
    <string>-unix-socket-perm</string>
    <string>$(xml_escape "$UNIX_SOCKET_PERM")</string>"
        if [ -n "$SOCKET_GROUP" ]; then
          LISTEN_PLIST_ARGS="${LISTEN_PLIST_ARGS}
    <string>-unix-socket-group</string>
    <string>$(xml_escape "$SOCKET_GROUP")</string>"
        fi
        MAC_LISTEN_DISPLAY="unix:${UNIX_SOCKET} (perm ${UNIX_SOCKET_PERM})"
      else
        LISTEN_PLIST_ARGS="    <string>-host</string>
    <string>$(xml_escape "$HOST_ADDR")</string>
    <string>-port</string>
    <string>$(xml_escape "$PORT")</string>"
        MAC_LISTEN_DISPLAY="http://${HOST_ADDR}:${PORT}"
      fi

      cat > "$PLIST_FILE" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$(xml_escape "$MACOS_LABEL")</string>
${ROLE_PLIST_KEYS}
  <key>ProgramArguments</key>
  <array>
    <string>$(xml_escape "$INSTALL_BIN")</string>
${LISTEN_PLIST_ARGS}
    <string>-data-dir</string>
    <string>$(xml_escape "$DATA_DIR")</string>
  </array>
  <key>WorkingDirectory</key>
  <string>$(xml_escape "$DATA_DIR")</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>SoftResourceLimits</key>
  <dict>
    <key>NumberOfFiles</key>
    <integer>65535</integer>
  </dict>
  <key>StandardOutPath</key>
  <string>$(xml_escape "$OUT_LOG")</string>
  <key>StandardErrorPath</key>
  <string>$(xml_escape "$ERR_LOG")</string>
</dict>
</plist>
EOF
      chmod 0644 "$PLIST_FILE"

      launchctl bootout "${DOMAIN_TARGET}/${MACOS_LABEL}" >/dev/null 2>&1 || launchctl unload "$PLIST_FILE" >/dev/null 2>&1 || true
      if ! launchctl bootstrap "$DOMAIN_TARGET" "$PLIST_FILE" >/dev/null 2>&1; then
        launchctl load -w "$PLIST_FILE"
      fi
      launchctl kickstart -k "${DOMAIN_TARGET}/${MACOS_LABEL}" >/dev/null 2>&1 || true

      log_ok "DBHarbor macOS launchd service (${MACOS_LABEL}) installed & started!"
      log_info "Plist    : ${PLIST_FILE}"
      log_info "Binary   : ${INSTALL_BIN}"
      log_info "Data Dir : ${DATA_DIR}"
      log_info "Logs     : ${OUT_LOG}"
      log_info "Listen   : ${MAC_LISTEN_DISPLAY}"
      ;;
    upgrade)
      if [ -z "$INSTALL_DIR" ] && [ -f "$PLIST_FILE" ]; then
        EXISTING_BIN="$(extract_macos_plist_bin "$PLIST_FILE")"
        [ -n "$EXISTING_BIN" ] && INSTALL_BIN="$EXISTING_BIN"
      fi
      resolve_source_binary
      copy_binary_safe "$RESOLVED_BIN_PATH" "$INSTALL_BIN"
      xattr -d com.apple.quarantine "$INSTALL_BIN" >/dev/null 2>&1 || true
      if [ -f "$PLIST_FILE" ]; then
        launchctl kickstart -k "${DOMAIN_TARGET}/${MACOS_LABEL}" >/dev/null 2>&1 || true
        log_ok "Upgraded & restarted ${MACOS_LABEL} (${INSTALL_BIN}): $("$INSTALL_BIN" -version 2>/dev/null || printf "OK")"
      else
        log_ok "Upgraded binary at ${INSTALL_BIN}: $("$INSTALL_BIN" -version 2>/dev/null || printf "OK")"
      fi
      ;;
    uninstall)
      launchctl bootout "${DOMAIN_TARGET}/${MACOS_LABEL}" >/dev/null 2>&1 || launchctl unload -w "$PLIST_FILE" >/dev/null 2>&1 || true
      rm -f "$PLIST_FILE"
      log_ok "Uninstalled macOS launchd service ${MACOS_LABEL} (data preserved in ${DATA_DIR})."
      ;;
    start)
      launchctl kickstart "${DOMAIN_TARGET}/${MACOS_LABEL}" >/dev/null 2>&1 || launchctl start "$MACOS_LABEL"
      log_ok "Started ${MACOS_LABEL}"
      ;;
    stop)
      launchctl kill SIGTERM "${DOMAIN_TARGET}/${MACOS_LABEL}" >/dev/null 2>&1 || launchctl stop "$MACOS_LABEL"
      log_ok "Stopped ${MACOS_LABEL}"
      ;;
    restart)
      launchctl kickstart -k "${DOMAIN_TARGET}/${MACOS_LABEL}"
      log_ok "Restarted ${MACOS_LABEL}"
      ;;
    status)
      launchctl print "${DOMAIN_TARGET}/${MACOS_LABEL}" || launchctl list | grep "$MACOS_LABEL" || log_warn "Service ${MACOS_LABEL} is not loaded."
      ;;
    logs)
      touch "$OUT_LOG" "$ERR_LOG"
      tail -n 100 -f "$OUT_LOG" "$ERR_LOG"
      ;;
  esac
}

if [ "$TARGET_OS" = "linux" ]; then
  run_linux
else
  run_macos
fi
