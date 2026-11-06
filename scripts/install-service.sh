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

SERVICE_NAME="dbharbor"
MACOS_LABEL="io.dbharbor.server"

ACTION="install"
BIN_SRC=""
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
  install      Install binary, register auto-start service, and start immediately (default)
  uninstall    Stop and remove the auto-start service registration (preserves data directory)
  start        Start the DBHarbor service
  stop         Stop the DBHarbor service
  restart      Restart the DBHarbor service
  status       Display current service status
  logs         Tail service runtime logs

Options:
  --bin <path>                    Path to compiled DBHarbor binary (auto-detected if omitted)
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
  --user-mode                     Install as current non-root user service (systemd --user / LaunchAgents)
  -h, --help                      Show this help message
EOF
}

if [ "$#" -gt 0 ]; then
  case "$1" in
    install|uninstall|start|stop|restart|status|logs)
      ACTION="$1"
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
    --host)
      HOST_ADDR="$2"
      shift 2
      ;;
    --port)
      PORT="$2"
      shift 2
      ;;
    --unix-socket)
      UNIX_SOCKET="$2"
      shift 2
      ;;
    --unix-socket-perm)
      UNIX_SOCKET_PERM="$2"
      shift 2
      ;;
    --socket-group)
      SOCKET_GROUP="$2"
      shift 2
      ;;
    --data-dir)
      DATA_DIR="$2"
      shift 2
      ;;
    --data-dir-mode)
      DATA_DIR_MODE="$2"
      shift 2
      ;;
    --user)
      SVC_USER="$2"
      shift 2
      ;;
    --group)
      SVC_GROUP="$2"
      shift 2
      ;;
    --supplementary-groups)
      SUPP_GROUPS="$2"
      shift 2
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

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

detect_binary() {
  if [ -n "$BIN_SRC" ]; then
    if [ ! -f "$BIN_SRC" ]; then
      log_err "Specified binary not found: $BIN_SRC"
      exit 1
    fi
    printf "%s" "$BIN_SRC"
    return
  fi

  CANDIDATE_NAME="dbharbor-${TARGET_OS}-${TARGET_ARCH}"
  for cand in \
    "${SCRIPT_DIR}/${CANDIDATE_NAME}" \
    "${SCRIPT_DIR}/dbharbor" \
    "${SCRIPT_DIR}/../dist/${CANDIDATE_NAME}" \
    "./${CANDIDATE_NAME}" \
    "./dist/${CANDIDATE_NAME}" \
    "/usr/local/bin/dbharbor"
  do
    if [ -f "$cand" ]; then
      printf "%s" "$cand"
      return
    fi
  done

  log_err "Could not auto-detect binary (${CANDIDATE_NAME}). Build first with 'npm run build' or pass --bin <path>."
  exit 1
}

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
    INSTALL_BIN="${HOME}/.local/bin/dbharbor"
    [ -z "$DATA_DIR" ] && DATA_DIR="${XDG_DATA_HOME:-${HOME}/.local/share}/dbharbor"
    UNIT_DIR="${HOME}/.config/systemd/user"
    UNIT_FILE="${UNIT_DIR}/${SERVICE_NAME}.service"

    case "$ACTION" in
      install)
        SRC="$(detect_binary)"
        mkdir -p "$(dirname "$INSTALL_BIN")" "$DATA_DIR" "$UNIT_DIR"
        [ -n "$UNIX_SOCKET" ] && mkdir -p "$(dirname "$UNIX_SOCKET")"
        chmod "$DATA_DIR_MODE" "$DATA_DIR" 2>/dev/null || true
        cp -f "$SRC" "$INSTALL_BIN"
        chmod 0755 "$INSTALL_BIN"

        cat > "$UNIT_FILE" <<EOF
[Unit]
Description=DBHarbor Multi-Database Web Workbench (User Service)
Documentation=https://github.com/Willh92/DBHarbor
After=network-online.target

[Service]
Type=simple
ExecStart=${INSTALL_BIN} ${LISTEN_ARGS} -data-dir ${DATA_DIR}
WorkingDirectory=${DATA_DIR}
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=default.target
EOF
        systemctl --user daemon-reload
        systemctl --user enable --now "${SERVICE_NAME}.service"
        log_ok "Installed & started user systemd service: ${SERVICE_NAME}.service"
        log_info "Listen   : ${LISTEN_DISPLAY}"
        log_info "Data Dir : ${DATA_DIR}"
        log_info "Tip: Run 'sudo loginctl enable-linger $(whoami)' to keep user service running before login."
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
  INSTALL_BIN="/usr/local/bin/dbharbor"
  [ -z "$DATA_DIR" ] && DATA_DIR="/var/lib/dbharbor"

  if [ "$HAS_SYSTEMD" = "1" ]; then
    UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
    case "$ACTION" in
      install)
        SRC="$(detect_binary)"
        ensure_linux_role_and_group "$SVC_USER" "$SVC_GROUP"

        mkdir -p /usr/local/bin "$DATA_DIR"
        if [ -n "$UNIX_SOCKET" ]; then
          SOCK_PARENT="$(dirname "$UNIX_SOCKET")"
          mkdir -p "$SOCK_PARENT"
          chown "${SVC_USER}:${SOCKET_GROUP:-$SVC_GROUP}" "$SOCK_PARENT" 2>/dev/null || true
          chmod 0755 "$SOCK_PARENT" 2>/dev/null || true
        fi
        if [ "$(cd "$(dirname "$SRC")" && pwd)/$(basename "$SRC")" != "$INSTALL_BIN" ]; then
          cp -f "$SRC" "$INSTALL_BIN"
        fi
        chmod 0755 "$INSTALL_BIN"
        chown -R "${SVC_USER}:${SVC_GROUP}" "$DATA_DIR"
        chmod "$DATA_DIR_MODE" "$DATA_DIR"

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
WorkingDirectory=${DATA_DIR}
ExecStart=${INSTALL_BIN} ${LISTEN_ARGS} -data-dir ${DATA_DIR}
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
        systemctl enable --now "${SERVICE_NAME}.service"
        log_ok "DBHarbor systemd service installed and enabled at boot!"
        log_info "Binary   : ${INSTALL_BIN}"
        log_info "Role     : User=${SVC_USER}, Group=${SVC_GROUP}${SUPP_GROUPS:+ (Supplementary=${SUPP_GROUPS})}"
        log_info "Data Dir : ${DATA_DIR} (mode ${DATA_DIR_MODE})"
        log_info "Listen   : ${LISTEN_DISPLAY}"
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
        SRC="$(detect_binary)"
        ensure_linux_role_and_group "$SVC_USER" "$SVC_GROUP"
        mkdir -p /usr/local/bin "$DATA_DIR"
        cp -f "$SRC" "$INSTALL_BIN"
        chmod 0755 "$INSTALL_BIN"
        chown -R "${SVC_USER}:${SVC_GROUP}" "$DATA_DIR"
        chmod "$DATA_DIR_MODE" "$DATA_DIR"
        touch "$LOG_FILE"
        chown "${SVC_USER}:${SVC_GROUP}" "$LOG_FILE" 2>/dev/null || true

        cat > "$INIT_SCRIPT" <<EOF
#!/sbin/openrc-run
name="DBHarbor"
description="DBHarbor Multi-Database Web Workbench"
command="${INSTALL_BIN}"
command_args="${LISTEN_ARGS} -data-dir ${DATA_DIR}"
command_user="${SVC_USER}:${SVC_GROUP}"
command_background=true
pidfile="/run/${SERVICE_NAME}.pid"
output_log="${LOG_FILE}"
error_log="${LOG_FILE}"

depend() {
  need net
}
EOF
        chmod 0755 "$INIT_SCRIPT"
        rc-update add "$SERVICE_NAME" default
        rc-service "$SERVICE_NAME" restart || rc-service "$SERVICE_NAME" start
        log_ok "DBHarbor OpenRC service installed and started!"
        log_info "Role     : ${SVC_USER}:${SVC_GROUP}"
        log_info "Listen   : ${LISTEN_DISPLAY}"
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
    INSTALL_BIN="${HOME}/.local/bin/dbharbor"
    [ -z "$DATA_DIR" ] && DATA_DIR="${HOME}/Library/Application Support/DBHarbor/data"
    LOG_DIR="${HOME}/Library/Logs/DBHarbor"
    PLIST_DIR="${HOME}/Library/LaunchAgents"
    DOMAIN_TARGET="gui/$(id -u)"
  else
    INSTALL_BIN="/usr/local/bin/dbharbor"
    [ -z "$DATA_DIR" ] && DATA_DIR="/Library/Application Support/DBHarbor/data"
    LOG_DIR="/var/log/dbharbor"
    PLIST_DIR="/Library/LaunchDaemons"
    DOMAIN_TARGET="system"
  fi

  PLIST_FILE="${PLIST_DIR}/${MACOS_LABEL}.plist"
  OUT_LOG="${LOG_DIR}/dbharbor.log"
  ERR_LOG="${LOG_DIR}/dbharbor.err.log"

  case "$ACTION" in
    install)
      SRC="$(detect_binary)"
      mkdir -p "$(dirname "$INSTALL_BIN")" "$DATA_DIR" "$LOG_DIR" "$PLIST_DIR"
      if [ "$(cd "$(dirname "$SRC")" && pwd)/$(basename "$SRC")" != "$INSTALL_BIN" ]; then
        cp -f "$SRC" "$INSTALL_BIN"
      fi
      chmod 0755 "$INSTALL_BIN"
      chmod "$DATA_DIR_MODE" "$DATA_DIR" 2>/dev/null || true
      xattr -d com.apple.quarantine "$INSTALL_BIN" >/dev/null 2>&1 || true

      ROLE_PLIST_KEYS=""
      if [ "$USER_MODE" = "0" ]; then
        if [ -n "$SVC_USER" ]; then
          ROLE_PLIST_KEYS="  <key>UserName</key>
  <string>${SVC_USER}</string>"
          chown -R "$SVC_USER" "$DATA_DIR" "$LOG_DIR" 2>/dev/null || true
        fi
        if [ -n "$SVC_GROUP" ]; then
          ROLE_PLIST_KEYS="${ROLE_PLIST_KEYS}
  <key>GroupName</key>
  <string>${SVC_GROUP}</string>"
          chgrp -R "$SVC_GROUP" "$DATA_DIR" "$LOG_DIR" 2>/dev/null || true
        fi
      fi

      [ -n "$UNIX_SOCKET" ] && mkdir -p "$(dirname "$UNIX_SOCKET")"
      if [ -n "$UNIX_SOCKET" ]; then
        LISTEN_PLIST_ARGS="    <string>-unix-socket</string>
    <string>${UNIX_SOCKET}</string>
    <string>-unix-socket-perm</string>
    <string>${UNIX_SOCKET_PERM}</string>"
        if [ -n "$SOCKET_GROUP" ]; then
          LISTEN_PLIST_ARGS="${LISTEN_PLIST_ARGS}
    <string>-unix-socket-group</string>
    <string>${SOCKET_GROUP}</string>"
        fi
        MAC_LISTEN_DISPLAY="unix:${UNIX_SOCKET} (perm ${UNIX_SOCKET_PERM})"
      else
        LISTEN_PLIST_ARGS="    <string>-host</string>
    <string>${HOST_ADDR}</string>
    <string>-port</string>
    <string>${PORT}</string>"
        MAC_LISTEN_DISPLAY="http://${HOST_ADDR}:${PORT}"
      fi

      cat > "$PLIST_FILE" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${MACOS_LABEL}</string>
${ROLE_PLIST_KEYS}
  <key>ProgramArguments</key>
  <array>
    <string>${INSTALL_BIN}</string>
${LISTEN_PLIST_ARGS}
    <string>-data-dir</string>
    <string>${DATA_DIR}</string>
  </array>
  <key>WorkingDirectory</key>
  <string>${DATA_DIR}</string>
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
  <string>${OUT_LOG}</string>
  <key>StandardErrorPath</key>
  <string>${ERR_LOG}</string>
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
