# Requirements — Agentic Voice SDR

Estados: `IMPLEMENTED` = materializado e verificável no repositório; `IN_PROGRESS` = parcialmente materializado; `PLANNED` = direção aprovada ainda não entregue; `TBD` = depende de decisão humana ou especificação.

## Funcionais

| ID | Requisito | Estado |
|---|---|---|
| REQ-VOICE-001 | Operar chamadas outbound para leads frios | IN_PROGRESS |
| REQ-VOICE-002 | Manter conversa natural em PT-BR por Gemini Live | PLANNED |
| REQ-CALL-001 | Limitar o MVP a uma chamada concorrente | PLANNED |
| REQ-CALL-002 | Alvo de duração de 2–3 minutos | PLANNED |
| REQ-CALL-003 | Permitir até três tentativas com política de horário comercial | IMPLEMENTED |
| REQ-CALL-004 | Encerrar voicemail e acionar fallback aprovado de WhatsApp | PLANNED |
| REQ-CALL-005 | Não negociar nem divulgar preço no MVP | PLANNED |
| REQ-TOOL-001 | Validar ações externas por Tool Registry | IMPLEMENTED |
| REQ-TOOL-002 | Agendar reunião por tool autorizada | PLANNED |
| REQ-MEM-001 | Persistir transcript e memória estruturada sem áudio bruto | PLANNED |
| REQ-MEM-002 | Tratar PostgreSQL/Supabase como fonte durável de verdade | PLANNED |

## Realtime e desempenho

| ID | Requisito | Estado |
|---|---|---|
| REQ-RT-001 | Usar Fale Paco SIP -> Asterisk -> AudioSocket -> Go Voice Engine -> Gemini Live | IN_PROGRESS |
| REQ-RT-002 | Manter Redis, RabbitMQ, PostgreSQL, Supabase, n8n e auxiliares fora do caminho de PCM | IMPLEMENTED (contrato) |
| REQ-RT-003 | Objetivo de conversational round-trip inferior a 800 ms | PLANNED |
| REQ-RT-004 | Não adicionar gravação de áudio ao MVP | IMPLEMENTED (restrição) |

## Resiliência e persistência

| ID | Requisito | Estado |
|---|---|---|
| REQ-RES-001 | Usar retry determinístico e bounded | IMPLEMENTED (política/contrato) |
| REQ-RES-002 | Usar Transactional Outbox para eventos críticos | PLANNED |
| REQ-RES-003 | Usar Redis apenas como cache/lock/rate-limit/idempotency fast-path | IMPLEMENTED (contrato) |
| REQ-RES-004 | Usar RabbitMQ para comandos/eventos/jobs fora do PCM | IN_PROGRESS |

## Segurança e privacidade

| ID | Requisito | Estado |
|---|---|---|
| REQ-SEC-001 | Nunca versionar secrets ou credenciais | IMPLEMENTED (política) |
| REQ-SEC-002 | Excluir PCM, secrets e PII não mascarada de logs/traces | IMPLEMENTED (contrato) |
| REQ-SEC-003 | Definir retenção, base legal e direitos de transcript/memória | TBD / HUMAN_GATE |

## Observabilidade e operação

| ID | Requisito | Estado |
|---|---|---|
| REQ-OBS-001 | Propagar W3C Trace Context em boundaries assíncronos | PLANNED |
| REQ-OBS-002 | Expor estado traceável de chamada/campanha | IN_PROGRESS |
| REQ-OBS-003 | Validar CI e evidência antes de Review | IMPLEMENTED (governança) |

## Infraestrutura e integrações

| ID | Requisito | Estado |
|---|---|---|
| REQ-INFRA-001 | Disponibilizar stack dev Redis/RabbitMQ reproduzível | IMPLEMENTED (dev) |
| REQ-INFRA-002 | Integrar Asterisk/SIP/AudioSocket em runtime real | IN_PROGRESS |
| REQ-INFRA-003 | Integrar Composio e WhatsApp provider em produção | PLANNED |
| REQ-INFRA-004 | Definir ambiente e deploy de produção | TBD / HUMAN_GATE |

JEV não é requisito implementado desta versão; sua classificação está em `docs/ARCHITECTURE.md`.
