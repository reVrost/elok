#!/usr/bin/env bash
set -euo pipefail

ui_stamp_file="tmp/.air-ui.mtime"

stat_mtime() {
  local file="$1"
  if stat -f "%m" "$file" >/dev/null 2>&1; then
    stat -f "%m" "$file"
    return
  fi
  stat -c "%Y" "$file"
}

latest_ui_mtime() {
  if [[ ! -d "ui" ]]; then
    return
  fi

  local latest=0
  local file
  while IFS= read -r -d '' file; do
    local mtime
    mtime="$(stat_mtime "$file")"
    if [[ -n "$mtime" ]] && ((mtime > latest)); then
      latest="$mtime"
    fi
  done < <(
    find ui \
      \( -path "ui/node_modules" -o -path "ui/.svelte-kit" -o -path "ui/dist" -o -path "ui/.vite" \) -prune \
      -o -type f -print0
  )

  if ((latest > 0)); then
    printf '%s\n' "$latest"
  fi
}

mkdir -p tmp

latest_ui=""
previous_ui=""
ui_changed=0
ui_dist_missing=0

latest_ui="$(latest_ui_mtime || true)"
if [[ -f "$ui_stamp_file" ]]; then
  previous_ui="$(<"$ui_stamp_file")"
fi

if [[ ! -f "ui/dist/index.html" ]]; then
  ui_dist_missing=1
fi

if [[ -n "$latest_ui" ]]; then
  if [[ -z "$previous_ui" ]]; then
    printf '%s\n' "$latest_ui" >"$ui_stamp_file"
  elif ((latest_ui > previous_ui)); then
    ui_changed=1
  fi
fi

if ((ui_changed == 1 || ui_dist_missing == 1)); then
  if [[ ! -d "ui/node_modules" ]]; then
    echo "[air-build] ui/node_modules missing; running make ui-install"
    make ui-install
  fi
  if ((ui_dist_missing == 1)); then
    echo "[air-build] UI dist missing; running make ui"
  else
    echo "[air-build] UI changed; running make ui"
  fi
  make ui
  if [[ -n "$latest_ui" ]]; then
    printf '%s\n' "$latest_ui" >"$ui_stamp_file"
  fi
else
  echo "[air-build] UI unchanged; skipping make ui"
fi

echo "[air-build] running make build"
make build
