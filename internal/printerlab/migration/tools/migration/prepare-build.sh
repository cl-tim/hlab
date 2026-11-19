#!/usr/bin/env bash
set -euo pipefail
klipper=$1
katapult=$2
klipper_ref=$3
katapult_ref=$4

for tool in git make arm-none-eabi-gcc python3; do
    if ! command -v "$tool" >/dev/null; then
        printf 'Missing build tool: %s\nInstall in Ubuntu/WSL: sudo apt-get update && sudo apt-get install -y git make gcc-arm-none-eabi binutils-arm-none-eabi libnewlib-arm-none-eabi python3 python3-venv\n' "$tool" >&2
        exit 1
    fi
done

prepare_source() {
    local directory=$1 url=$2 commit=$3
    case "$directory" in /*) ;; *) printf 'Build source paths must be absolute: %s\n' "$directory" >&2; exit 1;; esac
    if [[ -L "$directory" ]]; then
        printf 'Refusing a source directory symlink: %s\n' "$directory" >&2
        exit 1
    fi
    if [[ -e "$directory" ]]; then
        [[ -d "$directory/.git" ]] || { printf 'Existing path is not a Git checkout: %s\n' "$directory" >&2; exit 1; }
        [[ "$(git -C "$directory" remote get-url origin)" == "$url" ]] || { printf 'Unexpected source remote: %s\n' "$directory" >&2; exit 1; }
        [[ -z "$(git -C "$directory" status --porcelain --untracked-files=normal)" ]] || { printf 'Source checkout has local changes: %s\n' "$directory" >&2; exit 1; }
        [[ "$(git -C "$directory" rev-parse HEAD)" == "$commit" ]] || { printf 'Source checkout differs from the pinned commit: %s\nUse a new source directory.\n' "$directory" >&2; exit 1; }
        return
    fi
    mkdir -p -- "$(dirname -- "$directory")"
    # A failed clone is retained for inspection, never deleted or reset.
    git clone --no-checkout -- "$url" "$directory"
    git -C "$directory" checkout --detach "$commit"
    [[ "$(git -C "$directory" rev-parse HEAD)" == "$commit" ]]
}

prepare_source "$klipper" https://github.com/Klipper3d/klipper.git "$klipper_ref"
prepare_source "$katapult" https://github.com/Arksine/katapult.git "$katapult_ref"
printf 'Firmware sources ready.\n'
