#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MIGRATION="${ROOT_DIR}/db/migrations/0004_agent_prompt_versions.sql"
for variable in PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE; do
  if [[ -z "${!variable:-}" ]]; then
    printf '[-] %s is required.\n' "$variable" >&2
    exit 1
  fi
done
command -v psql >/dev/null || { echo '[-] psql is required.' >&2; exit 127; }

TEST_SCHEMA="agent_prompt_test_${BASHPID}_$(date +%s%N)"
psql_base() { psql -X -v ON_ERROR_STOP=1 "$@"; }
psql_test() {
  psql_base -c "SET search_path TO \"${TEST_SCHEMA}\", public;" "$@"
}
cleanup() {
  psql_base -c "DROP SCHEMA IF EXISTS \"${TEST_SCHEMA}\" CASCADE;" >/dev/null 2>&1 || true
}
trap cleanup EXIT

psql_base -c "CREATE SCHEMA \"${TEST_SCHEMA}\";" >/dev/null
psql_test -f "${MIGRATION}" >/dev/null
psql_test <<'SQL'
CREATE FUNCTION assert_rejected(statement TEXT)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE rejected BOOLEAN := FALSE;
BEGIN
    BEGIN
        EXECUTE statement;
    EXCEPTION WHEN OTHERS THEN
        rejected := TRUE;
    END;
    IF NOT rejected THEN
        RAISE EXCEPTION 'statement unexpectedly succeeded: %', statement;
    END IF;
END;
$$;

DO $$
BEGIN
    IF to_regclass('agent_prompt_versions') IS NULL THEN
        RAISE EXCEPTION 'agent_prompt_versions table is missing';
    END IF;
    IF EXISTS (SELECT 1 FROM agent_prompt_versions) THEN
        RAISE EXCEPTION 'migration unexpectedly seeded an agent prompt';
    END IF;
    IF (SELECT count(*) FROM agent_prompt_versions WHERE is_active) <> 0 THEN
        RAISE EXCEPTION 'zero active prompt versions should be valid';
    END IF;
END $$;

INSERT INTO agent_prompt_versions (name, prompt)
VALUES ('synthetic v1', 'synthetic conversational prompt v1');
INSERT INTO agent_prompt_versions (name, prompt)
VALUES ('synthetic v2', 'synthetic conversational prompt v2');

DO $$
DECLARE v1 BIGINT; v2 BIGINT;
BEGIN
    SELECT version INTO v1 FROM agent_prompt_versions WHERE name = 'synthetic v1';
    SELECT version INTO v2 FROM agent_prompt_versions WHERE name = 'synthetic v2';
    IF v1 <= 0 OR v2 <= v1 THEN
        RAISE EXCEPTION 'generated version must be positive and strictly increasing';
    END IF;
END $$;

SELECT assert_rejected($q$INSERT INTO agent_prompt_versions(name,prompt) VALUES ('', 'x')$q$);
SELECT assert_rejected($q$INSERT INTO agent_prompt_versions(version,name,prompt) VALUES (999, 'synthetic explicit version', 'x')$q$);
SELECT assert_rejected($q$INSERT INTO agent_prompt_versions(name,prompt) VALUES (E' \t\n ', 'x')$q$);
SELECT assert_rejected(format(
    'INSERT INTO agent_prompt_versions(name,prompt) VALUES (%L, %L)',
    repeat('n', 121), 'x'));
SELECT assert_rejected($q$INSERT INTO agent_prompt_versions(name,prompt) VALUES ('synthetic', '')$q$);
SELECT assert_rejected($q$INSERT INTO agent_prompt_versions(name,prompt) VALUES ('synthetic', E' \t\n ')$q$);
SELECT assert_rejected(format(
    'INSERT INTO agent_prompt_versions(name,prompt) VALUES (%L, %L)',
    'synthetic too large', repeat('x', 32769)));
SELECT assert_rejected(format(
    'INSERT INTO agent_prompt_versions(name,prompt) VALUES (%L, %L)',
    'synthetic multibyte too large', repeat('é', 16385)));

INSERT INTO agent_prompt_versions(name, prompt)
VALUES ('synthetic max bytes', repeat('x', 32768));
INSERT INTO agent_prompt_versions(name, prompt)
VALUES ('synthetic max multibyte bytes', repeat('é', 16384));

DO $$
DECLARE v1 BIGINT;
BEGIN
    SELECT version INTO v1 FROM agent_prompt_versions WHERE name = 'synthetic v1';
    UPDATE agent_prompt_versions
       SET is_active = TRUE, activated_at = now()
     WHERE version = v1;
    IF (SELECT count(*) FROM agent_prompt_versions WHERE is_active) <> 1 THEN
        RAISE EXCEPTION 'v1 activation failed';
    END IF;
END $$;

SELECT assert_rejected($q$
    UPDATE agent_prompt_versions
       SET is_active = TRUE, activated_at = now()
     WHERE name = 'synthetic v2'
$q$);

BEGIN;
UPDATE agent_prompt_versions
   SET is_active = FALSE
 WHERE name = 'synthetic v1';
UPDATE agent_prompt_versions
   SET is_active = TRUE, activated_at = now()
 WHERE name = 'synthetic v2';
COMMIT;

DO $$
DECLARE v1 agent_prompt_versions%ROWTYPE;
BEGIN
    SELECT * INTO v1 FROM agent_prompt_versions WHERE name = 'synthetic v1';
    IF (SELECT count(*) FROM agent_prompt_versions WHERE is_active) <> 1 THEN
        RAISE EXCEPTION 'activation transaction must leave exactly one active version';
    END IF;
    IF (SELECT name FROM agent_prompt_versions WHERE is_active) <> 'synthetic v2' THEN
        RAISE EXCEPTION 'v2 should be active after transaction';
    END IF;
    IF v1.version IS NULL OR v1.prompt <> 'synthetic conversational prompt v1'
       OR v1.activated_at IS NULL OR v1.is_active THEN
        RAISE EXCEPTION 'historical v1 was not preserved with activation timestamp';
    END IF;
END $$;

SELECT assert_rejected($q$UPDATE agent_prompt_versions SET name = 'mutated' WHERE name = 'synthetic v1'$q$);
SELECT assert_rejected($q$UPDATE agent_prompt_versions SET prompt = 'mutated' WHERE name = 'synthetic v1'$q$);
SELECT assert_rejected($q$UPDATE agent_prompt_versions SET created_at = now() + interval '1 day' WHERE name = 'synthetic v1'$q$);
SELECT assert_rejected($q$UPDATE agent_prompt_versions SET version = version + 100 WHERE name = 'synthetic v1'$q$);
SELECT assert_rejected($q$UPDATE agent_prompt_versions SET activated_at = NULL WHERE name = 'synthetic v2'$q$);

UPDATE agent_prompt_versions
   SET activated_at = now() - interval '1 minute'
 WHERE name = 'synthetic v1';
DO $$
BEGIN
    IF (SELECT activated_at IS NULL OR is_active FROM agent_prompt_versions WHERE name = 'synthetic v1') THEN
        RAISE EXCEPTION 'inactive historical row must retain activated_at';
    END IF;
END $$;
SQL

printf '%s\n' \
  '[+] agent_prompt_versions table and no-seed/zero-active contract: PASS.' \
  '[+] Generated positive, monotonically increasing versions: PASS.' \
  '[+] Name and prompt validation, including 32768-byte boundary: PASS.' \
  '[+] Zero/one-active invariant and activation transaction: PASS.' \
  '[+] Historical retention and immutable content/metadata: PASS.'
