# GRU-61 — Antigravity

Linear: https://linear.app/grupoalcate-ia/issue/GRU-61/microtaskantigravity-bootstrap-redis-rabbitmq-dev-infrastructure

Owner: agent:antigravity

Bootstrap only the Redis + RabbitMQ development infrastructure described in GRU-61.

Mandatory:
- follow ADR-002 and GRU-57;
- no business-domain changes;
- no real credentials;
- provide reproducible smoke-test evidence before Ready for Review.

---

## Dependência de Fundação

> [!WARNING]
> **Status de Bloqueio:**
> A tarefa **GRU-61** permanece vinculada e bloqueada pela **GRU-57** (`[FOUNDATION] Define Redis + RabbitMQ low-latency infrastructure`). A GRU-61 **não pode** transicionar para `Done` enquanto a dependência GRU-57 estiver aberta. Políticas de retry/backoff dependem dos contratos da GRU-57 e **não** são declaradas como implementadas aqui (apenas a fila declarativa `call.retry` encontra-se provisionada na topologia).

---

## Entregáveis Concluídos

1. **Docker Compose Declarativo (`deploy/dev/docker-compose.yml`):**
   - Redis pinado na versão `redis:7.4-alpine` com healthcheck nativo, parâmetros de memória (`maxmemory`, `allkeys-lru`), sem persistência canônica em disco e **sem senha de fallback hardcoded** (exige `REDIS_PASSWORD` via env).
   - RabbitMQ pinado na versão `rabbitmq:3.13-management-alpine` com healthcheck (`rabbitmq-diagnostics`), interface de gerenciamento e carregamento automático de topologia, **sem senhas salvas em arquivos de configuração**.
   - Rede dedicada `voice-dev-net`.

2. **Topologia Declarativa RabbitMQ (`deploy/dev/rabbitmq/definitions.json` & `rabbitmq.conf`):**
   - `definitions.json`: estritamente topologia (exchanges, queues, bindings), sem guardar usuários ou hashes de senha.
   - `rabbitmq.conf`: sem credenciais salvas.
   - Exchanges: `voice.commands`, `voice.events`, `voice.dlx` (todas topic/durable).
   - Queues: `call.dispatch`, `call.retry`, `tool.jobs`, `transcript.persist` (com DLX configurada para `voice.dlx` e routing key `voice.dead`).
   - DLQ: `voice.dead` durável.

3. **Automação e Validação (`scripts/infra/`):**
   - `setup-rabbitmq-topology.sh`: script idempotente via HTTP Management API com validação estrita de credenciais em env.
   - `smoke-test.sh`: suite completa de testes automatizados com flag `--down` e validação estrita de variáveis de ambiente.

4. **Documentação e Configuração:**
   - `docs/infra/REDIS_RABBITMQ_DEV.md`: documentação técnica completa e detalhada.
   - `.env.example`: variáveis de ambiente dev contendo apenas placeholders seguros (`replace_with_...`).

---

## Status de Verificação

- [x] Docker Compose válido (`docker compose config`).
- [x] Containers iniciam e atingem status `healthy`.
- [x] Redis responde `PONG` e valida política `allkeys-lru` utilizando credenciais de ambiente.
- [x] RabbitMQ valida aliveness e topologia declarativa (3 exchanges, 5 queues, bindings, DLX) utilizando credenciais de ambiente.
- [x] Smoke test com publicação e consumo ponta a ponta validado com exit code 0.
- [x] Nenhuma credencial fixa no runtime, definitions ou rabbitmq.conf.
- [x] Escopo restrito a `deploy/dev/**`, `scripts/infra/**`, `docs/infra/**` e `.env.example`.
- [x] Bloqueio pela GRU-57 documentado e respeitado.
