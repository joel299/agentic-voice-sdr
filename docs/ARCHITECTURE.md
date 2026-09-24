# Architecture — Current State

## Boundary principal

```text
Fale Paco SIP -> Asterisk -> AudioSocket -> Go Voice Engine -> Gemini Live
```

Esse é o único caminho aprovado para o fluxo linear de áudio realtime. Redis, RabbitMQ, PostgreSQL, Supabase, n8n, Composio, WhatsApp e demais integrações não podem carregar raw PCM/audio frames.

## Componentes e estado real

| Componente | Papel | Estado no repositório |
|---|---|---|
| Go API | Entrypoints, health/config APIs e runtime interno | IMPLEMENTED |
| Fale Paco SIP | Trunk/provedor de telefonia | IN_PROGRESS / provider boundary |
| Asterisk | Gateway SIP/media para AudioSocket | IN_PROGRESS / integração externa |
| AudioSocket | Framing, stream e servidor TCP boundary | IMPLEMENTED |
| Gemini Live | Conversa natural/voz realtime | PLANNED para integração runtime |
| CallSession | Ciclo e estados da chamada | IMPLEMENTED |
| Tool Registry | Contratos, schemas, validação e registro de tools | IMPLEMENTED |
| PostgreSQL/Supabase | Fonte durável para domínio, transcript e memória | PLANNED como runtime persistente |
| Redis | Cache quente, locks, rate limit e idempotency fast-path | IN_PROGRESS; contrato/dev configurado |
| RabbitMQ | Dispatch, eventos, jobs e DLQ fora do PCM | IN_PROGRESS; contrato/dev configurado |
| Transactional Outbox | Atomicidade domínio + evento e relay | PLANNED como runtime |
| Composio | Boundary para ações externas autorizadas | PLANNED |
| WhatsApp | Fallback/continuidade e provider boundary | IN_PROGRESS; configuração/provider HTTP existem |
| OpenTelemetry | Traces e propagação de contexto | PLANNED |

A classificação acima separa código/configuração/contrato existentes de integração operacional completa. Não há implementação de Gemini Live, JEV ou produção neste pacote documental.

## Boundaries

1. **Telephony boundary:** SIP entra no Asterisk; Asterisk conversa com o servidor AudioSocket.
2. **Realtime boundary:** AudioSocket entrega frames ao Go Voice Engine; o engine mantém a sessão Gemini Live.
3. **Conversation boundary:** Gemini Live conduz conversa natural; domínio decide estados e regras.
4. **Tool boundary:** somente tools registradas e validadas podem acionar sistemas externos.
5. **Persistence boundary:** PostgreSQL/Supabase é canônico; Redis não é fonte de verdade.
6. **Async boundary:** Outbox publica eventos para RabbitMQ; consumers executam jobs, persistência e integrações fora do PCM.
7. **Fallback boundary:** WhatsApp é ação externa/continuidade, nunca transporte de áudio realtime.

## JEV — PLANNED / FUTURE DECISION LAYER

JEV é uma direção arquitetural aprovada para discussão futura, não uma funcionalidade implementada. Seu objetivo futuro é atuar como `Sales Decision Advisor`, classificar objeções, escolher estratégia e possivelmente fornecer um `Tool Guard`. Gemini Live permanece responsável pela conversa natural/voz. JEV não deve entrar no PCM path e nenhuma integração JEV é implementada nesta task.

## Concorrência, latência e privacidade

O MVP tem `concurrency = 1`. O objetivo documentado para conversational round-trip é `< 800ms`, sujeito a SIP, rede, buffers e inferência. O MVP não grava áudio. PCM, secrets e PII não mascarada não entram em logs, traces, filas ou persistência auxiliar.

## Deployment e ambiente

GRU-104 foi executada em `LOCAL` por Stark/Hermes como bootstrap histórico. A partir de GRU-105, GRU-101 e das validações realtime/runtime seguintes, o ambiente operacional de Stark/Hermes é a `Stark VPS`; essa VPS não deve ser identificada pelo alias `vultur-vps`, por IP ou por credenciais. Arquimedes/OpenCode e Neriel/OpenClaw usam `vultur-vps` via alias SSH quando autorizados por suas GRUs.

GRU-105 validou na Stark VPS Ubuntu 26.04, linux/amd64, Go nativo 1.27.1, CGO/GCC e PATH persistente, além de `go mod download`, testes, race detector, vet, build e `git diff --check`. Este documento não autoriza mudança de produção, deploy ou secrets; qualquer definição de produção, custo, segredo, autenticação ou migração destrutiva exige HUMAN_GATE.
