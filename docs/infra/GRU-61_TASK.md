# GRU-61 — Arquimedes

Linear: https://linear.app/grupoalcate-ia/issue/GRU-61/microtaskantigravity-bootstrap-redis-rabbitmq-dev-infrastructure

Executor: Arquimedes (Antigravity), historical record from the former agent persona; current Arquimedes execution identity is OpenCode.
Reviewer: Anorak

Bootstrap only the Redis + RabbitMQ development infrastructure described in GRU-61.

Mandatory:
- follow ADR-002 and GRU-57 (Approved & Done);
- no business-domain changes;
- no real credentials;
- automatic declarative topology loading on RabbitMQ startup;
- provide reproducible smoke-test evidence before Ready for Review.

---

## Status da Fundação (GRU-57)

> [!NOTE]
> **Fundação Concluída:**
> A **GRU-57** foi formalmente aprovada e concluída (`Done`). A infraestrutura desta tarefa (GRU-61) foi totalmente reconciliada com o contrato canônico, implementando a topologia de retry por estágios (`call.retry.30s`, `call.retry.120s`, `call.retry.600s`) com retorno automático para `voice.commands/call.dispatch`.

---

## Entregáveis Concluídos e Reconciliados

1. **Docker Compose Declarativo (`deploy/dev/docker-compose.yml`):**
   - Redis pinado na versão `redis:7.4-alpine` com healthcheck nativo, parâmetros de memória (`maxmemory`, `allkeys-lru`), sem persistência canônica em disco e **sem senha de fallback hardcoded** (exige `REDIS_PASSWORD` via env).
   - RabbitMQ pinado na versão `rabbitmq:3.13-management-alpine` com healthcheck (`rabbitmq-diagnostics`), interface de gerenciamento e **carregamento automático de topologia na inicialização** via `/etc/rabbitmq/definitions.json` configurado em `rabbitmq.conf`, com usuário administrador provisionado dinamicamente via variáveis de ambiente.
   - Rede dedicada `voice-dev-net`.

2. **Topologia Declarativa RabbitMQ Reconciliada (`deploy/dev/rabbitmq/definitions.json` & `rabbitmq.conf`):**
   - `definitions.json`: estritamente topologia (exchanges, queues, bindings), sem guardar usuários ou hashes de senha.
   - `rabbitmq.conf`: configura `load_definitions = /etc/rabbitmq/definitions.json` sem credenciais fixas.
   - Exchanges: `voice.commands`, `voice.events`, `voice.dlx` (todas topic/durable).
   - Work Queues: `call.dispatch`, `tool.jobs`, `transcript.persist` (com DLX configurada para `voice.dlx` e routing key `voice.dead`).
   - Stage-Based Delay Queues: `call.retry.30s` (TTL 30s), `call.retry.120s` (TTL 120s) e `call.retry.600s` (TTL 600s), todas com DLX configurada para `voice.commands` e routing key `call.dispatch`.
   - DLQ: `voice.dead` durável.
   - Bindings declarativos: `voice.commands -> tool.jobs (tool.job.#)`, `voice.events -> transcript.persist (call.transcript.#)`, retry queues e DLQ.


3. **Automação e Validação (`scripts/infra/`):**
   - `setup-rabbitmq-topology.sh`: script opcional/idempotente de recovery e reaplicação manual atualizado com as filas de delay por estágio.
   - `smoke-test.sh`: suite completa de testes automatizados com flag `--down` e validação estrita de variáveis de ambiente. **Valida a existência prévia da topologia carregada no startup (sem importação manual), valida os TTLs fixos das filas de delay e comprova o retorno funcional após expiração de TTL de `call.retry.30s` para `call.dispatch` com exit code 0**.

4. **Documentação e Configuração:**
   - `docs/infra/REDIS_RABBITMQ_DEV.md`: documentação técnica completa e detalhada alinhada à GRU-57.
   - `.env.example`: variáveis de ambiente dev contendo apenas placeholders seguros (`replace_with_...`).

---

## Status de Verificação

- [x] Docker Compose válido (`docker compose config`).
- [x] Containers iniciam e atingem status `healthy`.
- [x] RabbitMQ carrega automaticamente `definitions.json` no startup normal.
- [x] Redis responde `PONG` e valida política `allkeys-lru` utilizando credenciais de ambiente.
- [x] RabbitMQ valida aliveness e topologia declarativa pré-existente (3 exchanges, 7 queues, bindings, DLX).
- [x] Fila antiga `call.retry` removida; três filas stage-based (`call.retry.30s`, `call.retry.120s`, `call.retry.600s`) ativas com TTLs corretos.
- [x] Retorno dead-letter de 30s da fila de retry para `call.dispatch` validado de ponta a ponta no smoke test.
- [x] Smoke test sem importação manual prévia com exit code 0 confirmado na VPS.
- [x] Nenhuma credencial fixa no runtime, definitions ou rabbitmq.conf.
- [x] Escopo restrito a `deploy/dev/**`, `scripts/infra/**`, `docs/infra/**` e `.env.example`.
- [x] Reconciliação formal com a GRU-57 concluída.
