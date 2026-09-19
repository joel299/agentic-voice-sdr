# Redis + RabbitMQ Development Infrastructure

Esta documentação descreve a infraestrutura local de desenvolvimento para **Redis** e **RabbitMQ** do projeto **Agentic Voice SDR**, em conformidade com a **ADR-002**, o contrato formal aprovado na **GRU-57** e a tarefa **GRU-61**.

> [!NOTE]
> **Alinhamento com a GRU-57 (Approved & Done):**
> A infraestrutura de desenvolvimento encontra-se plenamente reconciliada com os contratos definitivos de baixa latência estabelecidos na **GRU-57**. A topologia de retries adota o modelo determinístico de filas de delay por estágio (`call.retry.30s`, `call.retry.120s`, `call.retry.600s`) com retorno automático para `voice.commands/call.dispatch`, reservando a DLQ `voice.dead` estritamente para falhas permanentes, poison pills e retries esgotados.

---

## 1. Princípios de Segurança e Governança

1. **Zero Credenciais Hardcoded:**
   - Nenhum container ou script possui senha fixa de fallback (como `devpassword` ou `guest`).
   - `deploy/dev/rabbitmq/rabbitmq.conf` não armazena `default_pass`.
   - `deploy/dev/rabbitmq/definitions.json` trata estritamente da topologia declarativa (exchanges, queues, bindings), sem declarar usuários ou hashes de senha.
2. **Injeção Estrita via Env/Secret:**
   - As credenciais devem ser fornecidas via variáveis de ambiente (`REDIS_PASSWORD`, `RABBITMQ_DEFAULT_USER`, `RABBITMQ_DEFAULT_PASS`).
   - Scripts (`smoke-test.sh`, `setup-rabbitmq-topology.sh`) validam a presença das variáveis e falham explicitamente se ausentes.
3. **Template Seguro (`.env.example`):**
   - Contém apenas placeholders demonstrativos (`replace_with_...`), sem segredos reais.

---

## 2. Decisões de Arquitetura (ADR-002 & GRU-57)

### 2.1. Redis (Cache Volátil e Hot State)
* **Função:** Camada rápida de estado efêmero de chamadas (`CallSession`), cache de tokens, rate-limiting e deduplicação.
* **Invariante Canônico:** **Não persiste dados canônicos.** A persistência oficial e canônica pertence exclusivamente ao PostgreSQL / Supabase.
* **Configuração:**
  * Imagem pinada: `redis:7.4-alpine`.
  * `maxmemory`: configurável (padrão: `256mb`).
  * `maxmemory-policy`: configurável (padrão: `allkeys-lru`).
  * Persistência em disco desativada (`--save "" --appendonly no`) para garantir que dados efêmeros não sejam tratados como storage persistente.
  * Autenticação obrigatória via variável de ambiente `REDIS_PASSWORD`.

### 2.2. RabbitMQ (Broker Operacional)
* **Função:** Mensageria operacional desacoplada para despacho de chamadas, enfileiramento assíncrono de jobs de tools, estágios de retry determinísticos e persistência de transcrições.
* **Invariante de Performance:** **Fora do caminho de áudio PCM.** Áudio linear PCM flui diretamente via Asterisk -> AudioSocket -> Go Voice Engine -> Gemini Live WSS com objetivo de latência conversacional < 800ms.
* **Configuração:**
  * Imagem pinada: `rabbitmq:3.13-management-alpine`.
  * Management UI / HTTP API ativa em porta separada (`15672`).
  * Topologia declarativa carregada na inicialização nativamente via `load_definitions = /etc/rabbitmq/definitions.json`.
  * Credenciais de acesso configuradas dinamicamente na inicialização do container a partir de variáveis de ambiente.

---

## 3. Topologia de Mensageria Declarativa

### 3.1. Exchanges
| Exchange | Tipo | Durável | Propósito |
|---|---|---|---|
| `voice.commands` | `topic` | Sim | Comandos de controle de chamada, estágios de retry e jobs de ferramentas. |
| `voice.events` | `topic` | Sim | Eventos de domínio emitidos pelo ciclo de vida da chamada. |
| `voice.dlx` | `topic` | Sim | Dead Letter Exchange para poison messages, payloads inválidos e retries esgotados. |

### 3.2. Filas de Trabalho e Dead Letter Queue (DLQ)
| Fila | Durável | Dead Letter Exchange | Dead Letter Routing Key | Propósito |
|---|---|---|---|---|
| `call.dispatch` | Sim | `voice.dlx` | `voice.dead` | Enfileiramento e despacho de chamadas a serem iniciadas. |
| `tool.jobs` | Sim | `voice.dlx` | `voice.dead` | Execução assíncrona de tools / integrações externas. |
| `transcript.persist`| Sim | `voice.dlx` | `voice.dead` | Persistência assíncrona de transcrições e memórias estruturadas. |
| `voice.dead` | Sim | *(nenhum - DLQ)* | *(nenhum)* | Fila canônica de descarte, análise de erros operacionais e alertas. |

### 3.3. Filas de Delay por Estágio (Retry Determinístico)
Estas filas **não possuem consumidores ativos**. Elas retêm mensagens durante o TTL fixo configurado e realizam dead-letter automático de volta para o fluxo de dispatch:

| Fila | Durável | TTL Fixo (`x-message-ttl`) | Dead Letter Exchange | Dead Letter Routing Key | Propósito |
|---|---|---|---|---|---|
| `call.retry.30s` | Sim | **30.000 ms** (30s) | `voice.commands` | `call.dispatch` | 1º estágio de backoff após falha transitória inicial. |
| `call.retry.120s`| Sim | **120.000 ms** (2m) | `voice.commands` | `call.dispatch` | 2º estágio de backoff após segunda falha transitória. |
| `call.retry.600s`| Sim | **600.000 ms** (10m)| `voice.commands` | `call.dispatch` | 3º estágio de backoff após terceira falha transitória. |

### 3.4. Bindings
* `voice.commands` -> `call.dispatch` (routing_key: `call.dispatch`)
* `voice.commands` -> `call.retry.30s` (routing_key: `call.retry.30s`)
* `voice.commands` -> `call.retry.120s` (routing_key: `call.retry.120s`)
* `voice.commands` -> `call.retry.600s` (routing_key: `call.retry.600s`)
* `voice.commands` -> `tool.jobs` (routing_key: `tool.jobs`)
* `voice.events` -> `transcript.persist` (routing_key: `transcript.persist`)
* `voice.dlx` -> `voice.dead` (routing_key: `voice.dead` e `#`)

---

## 4. Variáveis de Ambiente

| Variável | Obrigatória | Descrição |
|---|---|---|
| `REDIS_PASSWORD` | Sim | Senha do Redis (sem fallback no runtime) |
| `REDIS_PORT` | Não (padrão: `6379`) | Porta de rede para conexão ao Redis |
| `REDIS_MAXMEMORY` | Não (padrão: `256mb`) | Limite de memória RAM para o Redis |
| `REDIS_MAXMEMORY_POLICY` | Não (padrão: `allkeys-lru`) | Política de desalocação de chaves do Redis |
| `RABBITMQ_DEFAULT_USER` | Sim | Usuário administrador do RabbitMQ |
| `RABBITMQ_DEFAULT_PASS` | Sim | Senha do RabbitMQ (sem fallback no runtime) |
| `RABBITMQ_DEFAULT_VHOST` | Não (padrão: `/`) | Virtual host padrão |
| `RABBITMQ_PORT` | Não (padrão: `5672`) | Porta do protocolo AMQP 0-9-1 |
| `RABBITMQ_MANAGEMENT_PORT` | Não (padrão: `15672`) | Porta da interface web e HTTP API do RabbitMQ |

---

## 5. Como Executar e Validar

### 5.1. Configuração Inicial
Copie o template de variáveis e configure senhas locais seguras:
```bash
cp .env.example .env
# Edite o arquivo .env definindo valores seguros para REDIS_PASSWORD, RABBITMQ_DEFAULT_USER e RABBITMQ_DEFAULT_PASS
```

### 5.2. Executar o Smoke Test Automatizado
O script `scripts/infra/smoke-test.sh` valida a infraestrutura e executa o ciclo completo de testes (Redis PING, memória, auto-load de topologia, DLX de `voice.dead` e retorno funcional de 30s de `call.retry.30s` para `call.dispatch`):
```bash
chmod +x scripts/infra/smoke-test.sh
./scripts/infra/smoke-test.sh
```

Opção com encerramento automático da stack ao final:
```bash
./scripts/infra/smoke-test.sh --down
```

### 5.3. Subir e Parar Manualmente
```bash
# Subir
docker compose -f deploy/dev/docker-compose.yml up -d

# Parar
docker compose -f deploy/dev/docker-compose.yml down -v
```
