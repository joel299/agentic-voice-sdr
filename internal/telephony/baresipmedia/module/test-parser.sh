#!/usr/bin/env sh
set -eu
src_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT HUP INT TERM
"${CC:-cc}" -std=c11 -Wall -Wextra -Werror -pthread \
	${C_TEST_FLAGS:-} -Wl,--wrap=recv -o "$test_dir/test-parser" \
	"$src_dir/tests/stream_source_test.c" "$src_dir/stream_source.c"
"$test_dir/test-parser"
