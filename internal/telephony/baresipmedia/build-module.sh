#!/usr/bin/env sh
set -eu

: "${BARESIP_SOURCE:?set BARESIP_SOURCE to the Baresip v1.1.0 source tree}"
: "${RE_INCLUDE_ROOT:?set RE_INCLUDE_ROOT to the directory containing re/ and rem/}"

src_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
out=${OUT:-"${TMPDIR:-/tmp}/gru151_media.so"}
cc=${CC:-cc}

# libre releases differ: declare the missing enum only when the public
# dependency headers do not supply it (no Baresip/runtime upgrade required).
jbuf_flag=
if ! printf '#include <re.h>\nint main(void) { return JBUF_FIXED; }\n' | "$cc" -x c -fsyntax-only -DHAVE_INTTYPES_H -DHAVE_STDBOOL_H -I"$RE_INCLUDE_ROOT/re" - 2>/dev/null; then
	jbuf_flag=-DGRU151_DECLARE_JBUF_TYPE
fi
"$cc" $jbuf_flag ${C_TEST_FLAGS:-} -std=c11 -Wall -Wextra -Werror -Wno-sign-compare \
	-DHAVE_INTTYPES_H -DHAVE_STDBOOL_H -fPIC -shared -pthread \
	-I"$BARESIP_SOURCE/include" \
	-I"$RE_INCLUDE_ROOT/re" -I"$RE_INCLUDE_ROOT/rem" \
	-o "$out" "$src_dir/module/baresip_module.c" "$src_dir/module/stream_source.c"
printf 'Built Baresip 1.1.0 media module: %s\n' "$out"
