# PRD — Agentic Voice SDR

## Status

Foundation context pack. Requisitos classificados como `IMPLEMENTED`, `IN_PROGRESS`, `PLANNED` ou `TBD` em `docs/REQUIREMENTS.md`. Este documento não transforma contratos futuros em produto entregue.

## Visão e problema

Construir um SDR outbound por voz, API-first, capaz de trabalhar leads B2B frios com conversa natural, qualificação e avanço para reunião. O problema é coordenar telefonia, realtime, regras comerciais, tools, persistência e fallback sem colocar áudio bruto em filas ou sistemas de negócio.

## Usuários e alvo

- Operadores/gestores de campanhas outbound.
- Times comerciais que trabalham leads B2B frios.
- Leads contatados por chamada telefônica e, quando aplicável, WhatsApp.

Personas, segmentos prioritários, consentimento de contato e critérios de ICP ainda dependem de definição por produto (`TBD/HUMAN_GATE`).

## Fluxo outbound

```text
Lead ingestion -> Campaign dispatcher -> Command queue -> Outbound call
-> Natural conversation -> Qualification -> Scheduling or WhatsApp fallback
-> Transcript/memory -> Outcome/state update
```

A chamada segue o caminho realtime aprovado em `docs/ARCHITECTURE.md`. O MVP é outbound-only e limita a uma chamada concorrente global (`concurrency = 1`).

## Lead ingestion

Leads devem chegar por uma fonte autorizada e ser normalizados antes de entrar em campanha. A fonte canônica de persistência é PostgreSQL/Supabase quando esse runtime for implementado. O contrato de campos obrigatórios, consentimento e deduplicação é `TBD` fora do escopo desta foundation.

## Chamada e qualificação

- Conversa natural alvo em PT-BR via Gemini Live quando a integração runtime existir.
- Duração alvo: 2–3 minutos.
- O SDR identifica interesse, objeções e disponibilidade.
- O objetivo é avançar para reunião; não negociar preço nem informar valores.
- Tools externas somente por Tool Registry e contratos autorizados.

## Agendamento

Agendamento por tool autorizada é objetivo do produto, mas o provider/calendário e sua política de confirmação são `PLANNED/TBD`. Nenhum calendário ou integração Composio é implementado nesta task.

## WhatsApp fallback

Voicemail encerra a chamada imediatamente e pode acionar o fallback aprovado de WhatsApp. WhatsApp também pode manter continuidade quando a política comercial permitir. O payload, provider definitivo e consentimento são `TBD`; não há raw audio nesse fluxo.

## Memória, transcript e resultados

O produto deve persistir transcript textual, metadados e memória estruturada da conversa, além do resultado da chamada e do estado do lead. Não grava áudio no MVP. Retenção, base legal, acesso e exclusão de transcript/memória exigem decisão de privacidade (`HUMAN_GATE`).

## Regras SDR

- Leads são frios; respeitar políticas de contato e opt-out.
- Máximo de três tentativas por lead.
- Retry após uma hora, dentro do horário comercial configurado em `America/Sao_Paulo`.
- Voicemail: encerrar e acionar WhatsApp aprovado.
- Sem negociação ou divulgação de preço.
- Sem gravação de áudio no MVP.
- Uma chamada concorrente global no MVP.

## Escopo MVP

- API-first outbound orchestration.
- CallSession e políticas de retry/horário.
- SIP/Asterisk/AudioSocket/Go boundary.
- Conversa Gemini Live, quando a integração runtime for entregue.
- Tool Registry para ações externas.
- Persistência de outcome/transcript/memória.
- WhatsApp fallback.
- Redis/RabbitMQ/Outbox fora do caminho PCM.

## Fora de escopo nesta foundation

- Implementar GRU-101.
- Implementar JEV, Tool Dispatcher, Calendar/Composio runtime ou mudança de comportamento Gemini.
- Alterar AudioSocket/SIP/Asterisk.
- Produção, deploy em VPS, secrets ou migrações destrutivas.
- Frontend e gravação de áudio.

## Pricing policy

O SDR não negocia preço e não informa valores no MVP. Qualquer mudança requer decisão de produto.

## Privacidade e segurança

Não versionar API keys, tokens, passwords, cookies, credentials, connection strings privadas, raw audio ou PII desnecessária. Raw PCM permanece exclusivamente no realtime path; logs/traces não devem conter áudio bruto, secrets ou PII não mascarada.

## Critérios de sucesso

O produto será considerado pronto para o MVP quando uma campanha controlada puder: selecionar um lead elegível; realizar a chamada; manter conversa natural; identificar interesse; executar tools autorizadas; agendar ou acionar fallback; persistir transcript/memória/outcome; aplicar retry corretamente; e expor estado rastreável. Cada critério depende das respectivas GRUs e gates de segurança/privacidade.
