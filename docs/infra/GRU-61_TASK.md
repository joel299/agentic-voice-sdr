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

## Entregáveis Concluídos

1. **Docker Compose Declarativo (`deploy/dev/docker-compose.yml`):**
   - Redis pinado na versão `redis:7.4-alpine` com healthcheck nativo, parâmetros de memória (`maxmemory`, `allkeys-lru`), senha via env e persistência canônica desabilitada.
   - RabbitMQ pinado na versão `rabbitmq:3.13-management-alpine` com healthcheck (`rabbitmq-diagnostics`), interface de gerenciamento e carregamento automático de topologia.
   - Rede dedicada `voice-dev-net`.

2. **Topologia Declarativa RabbitMQ (`deploy/dev/rabbitmq/definitions.json` & `rabbitmq.conf`):**
   - Exchanges: `voice.commands`, `voice.events`, `voice.dlx` (todas topic/durable).
   - Queues: `call.dispatch`, `call.retry`, `tool.jobs`, `transcript.persist` (com DLX configurada para `voice.dlx` e routing key `voice.dead`).
   - DLQ: `voice.dead` durável.
   - Bindings declarados e automáticos.

3. **Automação e Validação (`scripts/infra/`):**
   - `setup-rabbitmq-topology.sh`: script idempotente via HTTP Management API para aplicação e verificação da topologia.
   - `smoke-test.sh`: suite completa de testes automatizados (healthchecks, Redis PING/config/CRUD, RabbitMQ node diagnostics, aliveness, exchanges, queues, DLX/DLQ publish-and-consume).

4. **Documentação e Configuração:**
   - `docs/infra/REDIS_RABBITMQ_DEV.md`: documentação técnica completa.
   - `.env.example`: variáveis de ambiente dev sem segredos expostos.

---

## Status de Verificação

- [x] Docker Compose válido (`docker compose config`).
- [x] Containers iniciam e atingem status `healthy`.
- [x] Redis responde `PONG` e valida política `allkeys-lru`.
- [x] RabbitMQ valida aliveness e topologia declarativa (3 exchanges, 5 queues, bindings, DLX).
- [x] Smoke test com publicação e consumo ponta a ponta validado com exit code 0.
- [x] Nenhuma credencial real commitada.
- [x] Escopo restrito a `deploy/dev/**`, `scripts/infra/**`, `docs/infra/**` e `.env.example`.
