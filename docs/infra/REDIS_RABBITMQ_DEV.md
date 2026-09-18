# Redis + RabbitMQ Development Infrastructure

Esta documentação descreve a infraestrutura local de desenvolvimento para **Redis** e **RabbitMQ** do projeto **Agentic Voice SDR**, em total conformidade com a **ADR-002** e a tarefa **GRU-61**.

---

## 1. Arquitetura e Decisões de Design (ADR-002)

### 1.1. Redis (Cache Volátil e Hot State)
* **Função:** Camada rápida de estado efêmero de chamadas (`CallSession`), cache de tokens e rate-limiting.
* **Invariante:** **Não persiste dados canônicos.** A persistência oficial e canônica pertence exclusivamente ao PostgreSQL / Supabase.
* **Configuração:**
  * Imagem pinada: `redis:7.4-alpine`.
  * `maxmemory`: configurável (padrão: `256mb`).
  * `maxmemory-policy`: configurável (padrão: `allkeys-lru`).
  * Persistência em disco desativada (`--save "" --appendonly no`) para garantir que dados efêmeros não sejam tratados como storage persistente.
  * Autenticação obrigatória via variável de ambiente (`REDIS_PASSWORD`).

### 1.2. RabbitMQ (Broker Operacional)
* **Função:** Mensageria operacional desacoplada para despacho de chamadas, retries com backoff, execução assíncrona de tools e persistência assíncrona de transcrições.
* **Invariante:** **Fora do caminho de áudio PCM.** Áudio bruto (PCM) flui diretamente via Asterisk -> AudioSocket -> Go Voice Engine -> Gemini Live.
* **Configuração:**
  * Imagem pinada: `rabbitmq:3.13-management-alpine`.
  * Management UI / HTTP API ativa em porta separada (`15672`).
  * Topologia declarativa carregada na inicialização via `definitions.json`.

---

## 2. Topologia de Mensageria Declarativa

### 2.1. Exchanges
| Exchange | Tipo | Durável | Propósito |
|---|---|---|---|
| `voice.commands` | `topic` | Sim | Comandos de controle de chamada e jobs de ferramentas. |
| `voice.events` | `topic` | Sim | Eventos de domínio emitidos pelo ciclo da chamada. |
| `voice.dlx` | `topic` | Sim | Dead Letter Exchange para mensagens rejeitadas ou falhas. |

### 2.2. Queues e Dead Letter Queues (DLQ)
| Fila | Durável | Dead Letter Exchange | Dead Letter Routing Key | Propósito |
|---|---|---|---|---|
| `call.dispatch` | Sim | `voice.dlx` | `voice.dead` | Enfileiramento de chamadas a serem iniciadas. |
| `call.retry` | Sim | `voice.dlx` | `voice.dead` | Retentativas de discagem agendadas. |
| `tool.jobs` | Sim | `voice.dlx` | `voice.dead` | Execução assíncrona de tools / integrações. |
| `transcript.persist`| Sim | `voice.dlx` | `voice.dead` | Persistência em background de transcrições. |
| `voice.dead` | Sim | *(nenhum - DLQ)* | *(nenhum)* | Fila de mensagens mortas / análise de erros. |

### 2.3. Bindings
* `voice.commands` -> `call.dispatch` (routing_key: `call.dispatch`)
* `voice.commands` -> `call.retry` (routing_key: `call.retry`)
* `voice.commands` -> `tool.jobs` (routing_key: `tool.jobs`)
* `voice.events` -> `transcript.persist` (routing_key: `transcript.persist`)
* `voice.dlx` -> `voice.dead` (routing_key: `voice.dead` e `#`)

---

## 3. Variáveis de Ambiente (`.env.example`)

| Variável | Valor Padrão | Descrição |
|---|---|---|
| `REDIS_PORT` | `6379` | Porta de rede para conexão ao Redis |
| `REDIS_PASSWORD` | `devpassword` | Senha de autenticação do Redis |
| `REDIS_MAXMEMORY` | `256mb` | Limite de memória RAM para o Redis |
| `REDIS_MAXMEMORY_POLICY`| `allkeys-lru` | Política de desalocação de chaves do Redis |
| `RABBITMQ_PORT` | `5672` | Porta do protocolo AMQP 0-9-1 |
| `RABBITMQ_MANAGEMENT_PORT`| `15672` | Porta da interface web e HTTP API do RabbitMQ |
| `RABBITMQ_DEFAULT_USER` | `guest` | Usuário administrador de desenvolvimento |
| `RABBITMQ_DEFAULT_PASS` | `guest` | Senha de desenvolvimento |
| `RABBITMQ_DEFAULT_VHOST`| `/` | Virtual host padrão |

---

## 4. Como Executar e Validar

### 4.1. Subir a Stack de Dev
```bash
docker compose -f deploy/dev/docker-compose.yml up -d
```

### 4.2. Executar o Smoke Test Automatizado
O script `scripts/infra/smoke-test.sh` executa a validação ponta a ponta:
```bash
chmod +x scripts/infra/smoke-test.sh
./scripts/infra/smoke-test.sh
```

Opção com encerramento automático da stack ao final:
```bash
./scripts/infra/smoke-test.sh --down
```

### 4.3. Reaplicar Topologia Declarativa Standalone (Opcional)
Se o RabbitMQ já estiver rodando e você quiser revalidar ou reaplicar a topologia declarativa via HTTP API:
```bash
chmod +x scripts/infra/setup-rabbitmq-topology.sh
./scripts/infra/setup-rabbitmq-topology.sh
```

### 4.4. Parar a Stack
```bash
docker compose -f deploy/dev/docker-compose.yml down -v
```
