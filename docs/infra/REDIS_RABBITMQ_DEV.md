# Redis + RabbitMQ Development Infrastructure

Esta documentação descreve a infraestrutura local de desenvolvimento para **Redis** e **RabbitMQ** do projeto **Agentic Voice SDR**, em conformidade com a **ADR-002**, a tarefa **GRU-61** e as diretrizes de governança de infraestrutura.

> [!IMPORTANT]
> **Dependência e Bloqueio:**
> A tarefa **GRU-61** possui dependência direta da **GRU-57** (`[FOUNDATION] Define Redis + RabbitMQ low-latency infrastructure`). Enquanto a GRU-57 estiver aberta, contratos formais de retry/backoff e outros tópicos de fundação permanecem sob especificação. Por este motivo, a fila `call.retry` encontra-se provisionada na topologia declarativa, mas políticas ativas de retry/backoff **não** são consideradas implementadas nesta etapa.

---

## 1. Princípios de Segurança e Governança

1. **Zero Credenciais Hardcoded:**
   - Nenhum container ou script possui senha fixa de fallback (como `devpassword` ou `guest`).
   - `deploy/dev/rabbitmq/rabbitmq.conf` não armazena `default_pass`.
   - `deploy/dev/rabbitmq/definitions.json` trata estritamente da topologia (exchanges, queues, bindings), sem declarar usuários ou hashes de senha.
2. **Injeção Estrita via Env/Secret:**
   - As credenciais devem ser fornecidas via variáveis de ambiente (`REDIS_PASSWORD`, `RABBITMQ_DEFAULT_USER`, `RABBITMQ_DEFAULT_PASS`).
   - Scripts (`smoke-test.sh`, `setup-rabbitmq-topology.sh`) validam a presença das variáveis e falham explicitamente se ausentes.
3. **Template Seguro (`.env.example`):**
   - Contém apenas placeholders demonstrativos (`replace_with_...`), sem segredos reais.

---

## 2. Decisões de Arquitetura (ADR-002)

### 2.1. Redis (Cache Volátil e Hot State)
* **Função:** Camada rápida de estado efêmero de chamadas (`CallSession`), cache de tokens e rate-limiting.
* **Invariante Canônico:** **Não persiste dados canônicos.** A persistência oficial e canônica pertence exclusivamente ao PostgreSQL / Supabase.
* **Configuração:**
  * Imagem pinada: `redis:7.4-alpine`.
  * `maxmemory`: configurável (padrão: `256mb`).
  * `maxmemory-policy`: configurável (padrão: `allkeys-lru`).
  * Persistência em disco desativada (`--save "" --appendonly no`) para garantir que dados efêmeros não sejam tratados como storage persistente.
  * Autenticação obrigatória via variável de ambiente `REDIS_PASSWORD`.

### 2.2. RabbitMQ (Broker Operacional)
* **Função:** Mensageria operacional desacoplada para despacho de chamadas, enfileiramento assíncrono de jobs de tools e persistência assíncrona de transcrições.
* **Invariante de Performance:** **Fora do caminho de áudio PCM.** Áudio bruto (PCM) flui diretamente via Asterisk -> AudioSocket -> Go Voice Engine -> Gemini Live.
* **Configuração:**
  * Imagem pinada: `rabbitmq:3.13-management-alpine`.
  * Management UI / HTTP API ativa em porta separada (`15672`).
  * Topologia declarativa carregada na inicialização via `definitions.json`.
  * Credenciais de acesso configuradas pelo entrypoint a partir de variáveis de ambiente.

---

## 3. Topologia de Mensageria Declarativa

### 3.1. Exchanges
| Exchange | Tipo | Durável | Propósito |
|---|---|---|---|
| `voice.commands` | `topic` | Sim | Comandos de controle de chamada e jobs de ferramentas. |
| `voice.events` | `topic` | Sim | Eventos de domínio emitidos pelo ciclo da chamada. |
| `voice.dlx` | `topic` | Sim | Dead Letter Exchange para mensagens rejeitadas ou falhas. |

### 3.2. Queues e Dead Letter Queues (DLQ)
| Fila | Durável | Dead Letter Exchange | Dead Letter Routing Key | Propósito |
|---|---|---|---|---|
| `call.dispatch` | Sim | `voice.dlx` | `voice.dead` | Enfileiramento de chamadas a serem iniciadas. |
| `call.retry` | Sim | `voice.dlx` | `voice.dead` | Fila provisionada para retries (política sob GRU-57). |
| `tool.jobs` | Sim | `voice.dlx` | `voice.dead` | Execução assíncrona de tools / integrações. |
| `transcript.persist`| Sim | `voice.dlx` | `voice.dead` | Persistência em background de transcrições. |
| `voice.dead` | Sim | *(nenhum - DLQ)* | *(nenhum)* | Fila de mensagens mortas / análise de erros. |

### 3.3. Bindings
* `voice.commands` -> `call.dispatch` (routing_key: `call.dispatch`)
* `voice.commands` -> `call.retry` (routing_key: `call.retry`)
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
O script `scripts/infra/smoke-test.sh` exige as variáveis e executa a validação ponta a ponta:
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
