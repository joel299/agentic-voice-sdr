#!/usr/bin/env sh
# Demonstrate RED on the reviewed production parser, not a simulated model.
set -eu
: "${BARESIP_SOURCE:?}"
: "${RE_INCLUDE_ROOT:?}"
: "${C_TEST_OUTPUT_DIR:?run test-module.sh into this directory first}"
src_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
old=$(mktemp -d)
trap 'rm -rf "$old"' EXIT HUP INT TERM
git -C "$src_dir" show a7580529866cdf3a07059c915d8f22806b8f816c:internal/telephony/baresipmedia/module/baresip_module.c > "$old/old.c"
# The installed libre2 already defines jbuf_type. Only remove the obsolete
# enum workaround, leaving the reviewed parser/source implementation intact.
if printf '#include <re.h>\nint main(void){return JBUF_FIXED;}\n' | "${CC:-cc}" -x c -fsyntax-only -DHAVE_INTTYPES_H -DHAVE_STDBOOL_H -I"$RE_INCLUDE_ROOT/re" - 2>/dev/null; then
	sed '/^enum jbuf_type /d' "$old/old.c" > "$old/fixed-header.c"
	mv "$old/fixed-header.c" "$old/old.c"
fi
"${CC:-cc}" -std=c11 -Wall -Wextra -Werror -Wno-sign-compare \
	-DHAVE_INTTYPES_H -DHAVE_STDBOOL_H -fPIC -shared -pthread \
	-I"$BARESIP_SOURCE/include" -I"$RE_INCLUDE_ROOT/re" -I"$RE_INCLUDE_ROOT/rem" \
	-o "$old/old.so" "$old/old.c"
set +e
result=$("$C_TEST_OUTPUT_DIR/module-source-test" "$old/old.so" 18)
status=$?
set -e
printf '%s\n' "$result"
[ "$status" -eq 1 ]
printf '%s\n' "$result" | grep -q 'actual_module_error_errno=90'
printf '%s\n' "$result" | grep -q 'actual_module_frames=0 expected=18 errors=1'
printf 'old_18_frame_failure_reproduced=yes\n'
