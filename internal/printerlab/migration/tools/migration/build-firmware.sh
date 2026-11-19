#!/usr/bin/env bash
# Compilation only. Go validates images and publishes the artifact manifest.
set -euo pipefail
root=$1 klipper=$2 katapult=$3 output=$4 klipper_ref=$5 katapult_ref=$6
for command in git make arm-none-eabi-gcc; do command -v "$command" >/dev/null; done
for project in klipper katapult; do
    directory=${!project}
    expected_name=${project}_ref
    expected=${!expected_name}
    test -e "$directory/.git" || { echo "Not a Git checkout: $directory" >&2; exit 1; }
    test "$(git -C "$directory" rev-parse HEAD)" = "$expected" || { echo "$project HEAD differs from pinned commit" >&2; exit 1; }
    test -z "$(git -C "$directory" status --porcelain --untracked-files=normal)" || { echo "Refusing dirty $project checkout" >&2; exit 1; }
done
saved=$(mktemp -d)
restore() {
    for project in klipper katapult; do
        directory=${!project}
        if test -f "$saved/$project.config"; then
            cp "$saved/$project.config" "$directory/.config"
        else
            rm -f -- "$directory/.config"
        fi
    done
    rm -rf -- "$saved"
}
for project in klipper katapult; do
    directory=${!project}
    if test -e "$directory/.config"; then cp "$directory/.config" "$saved/$project.config"; fi
done
trap restore EXIT
mkdir -p "$output"
build() {
    fragment=$1 directory=$2 binary=$3 target=$4
    cp "$root/firmware-configs/$fragment" "$directory/.config"
    make -C "$directory" olddefconfig
    make -C "$directory" clean
    make -C "$directory" -j"$(getconf _NPROCESSORS_ONLN)"
    cp "$directory/out/$binary" "$output/$target"
}
build klipper/plus4-mainboard.config "$klipper" klipper.bin plus4-mainboard-klipper.bin
build klipper/plus4-toolhead.config "$klipper" klipper.bin plus4-toolhead-klipper.bin
build katapult/plus4-toolhead.config "$katapult" katapult.bin plus4-toolhead-katapult.bin
arm-none-eabi-gcc --version | head -n 1
