#!/usr/bin/env sh
set -eu

: "${BARESIP_SOURCE:?set BARESIP_SOURCE to the Baresip v1.1.0 source tree}"
: "${RE_INCLUDE_ROOT:?set RE_INCLUDE_ROOT to the directory containing re/ and rem/}"

src_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
out=${OUT:-"${TMPDIR:-/tmp}/gru151_media.so"}
cc=${CC:-cc}

"$cc" -std=c11 -Wall -Wextra -Werror -Wno-sign-compare \
	-DHAVE_INTTYPES_H -DHAVE_STDBOOL_H -fPIC -shared -pthread \
	-I"$BARESIP_SOURCE/include" \
	-I"$RE_INCLUDE_ROOT/re" -I"$RE_INCLUDE_ROOT/rem" \
	-o "$out" "$src_dir/module/baresip_module.c"
printf 'Built Baresip 1.1.0 media module: %s\n' "$out"
