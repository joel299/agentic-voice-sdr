#!/usr/bin/env sh
# Requires Baresip 1.1.0 source headers and matching libre/librem headers/libs.
# Downloads/installs nothing and never loads an account or dials a provider.
set -eu
: "${BARESIP_SOURCE:?set BARESIP_SOURCE to Baresip v1.1.0}"
: "${RE_INCLUDE_ROOT:?set RE_INCLUDE_ROOT to directory containing re/ and rem/}"
src_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
artifacts=${C_TEST_OUTPUT_DIR:-$(mktemp -d)}
mkdir -p "$artifacts"
OUT="$artifacts/gru151_media.so" sh "$src_dir/build-module.sh"
jbuf_flag=
if ! printf '#include <re.h>\nint main(void){return JBUF_FIXED;}\n' | "${CC:-cc}" -x c -fsyntax-only -DHAVE_INTTYPES_H -DHAVE_STDBOOL_H -I"$RE_INCLUDE_ROOT/re" - 2>/dev/null; then
	jbuf_flag=-DGRU151_DECLARE_JBUF_TYPE
fi
"${CC:-cc}" $jbuf_flag -include "$src_dir/module/baresip_compat.h" ${C_TEST_FLAGS:-} -std=c11 -Wall -Wextra -Werror -Wno-sign-compare \
	-D_DEFAULT_SOURCE=1 -DHAVE_INTTYPES_H -DHAVE_STDBOOL_H -rdynamic -pthread \
	-I"$BARESIP_SOURCE/include" -I"$RE_INCLUDE_ROOT/re" -I"$RE_INCLUDE_ROOT/rem" \
	"$src_dir/module/tests/module_source_test.c" "$BARESIP_SOURCE/src/auframe.c" \
	${RE_LINK:- -lre -lrem} -ldl -o "$artifacts/module-source-test"
sh "$src_dir/module/test-parser.sh"
for count in 1 2 18 250; do
	"$artifacts/module-source-test" "$artifacts/gru151_media.so" "$count"
done
printf 'actual_c_module_test=PASS artifacts=%s\n' "$artifacts"
