# Agentic Voice SDR — Agent Entrypoint

## Read first

Antes de qualquer GRU, leia nesta ordem:

1. `CLAUDE.md`
2. `AGENTS.md`
3. `docs/CONTEXT_INDEX.md`
4. `docs/PRD.md`
5. `docs/REQUIREMENTS.md`
6. `docs/ARCHITECTURE.md`
7. `docs/LOOP_ENGINEERING.md`
8. `docs/PROJECT.md`, `docs/ENGINEERING.md`, `docs/AGENT_PROTOCOL.md` e `docs/PROMPT_CACHE.md` quando forem relevantes
9. GitHub Issue #54, a Linear GRU atual e o mirror GitHub da GRU

## O produto

Agentic Voice SDR é um sistema API-first de prospecção outbound por voz para leads B2B frios. O MVP coordena campanhas, chamadas, conversa natural, qualificação, agendamento, fallback de WhatsApp e persistência de transcript/memória estruturada.

## Escopo operacional

Trabalhe somente no contrato e no `ALLOWED_SCOPE` da GRU. Não implemente GRU-101 junto desta task. Não transforme uma direção planejada em funcionalidade implementada. Ambiguidades de produto, arquitetura, segurança, produção, custo relevante ou migração destrutiva exigem `HUMAN_GATE`.

## Arquitetura realtime

O caminho aprovado é:

```text
Fale Paco SIP -> Asterisk -> AudioSocket -> Go Voice Engine -> Gemini Live
```

Redis, RabbitMQ, PostgreSQL, Supabase, n8n, Composio e integrações auxiliares **não podem transportar raw PCM/audio frames**. Persistência, eventos, memória e tools ficam fora do caminho linear de áudio.

## Loop Engineering

Toda GRU segue:

```text
SPEC -> TEST -> RED -> IMPLEMENT -> GREEN -> REFACTOR -> REGRESSION -> EVIDENCE -> REVIEW
```

A evidência deve conter diff, validações, CI, decisões, riscos e handoff. Não declare sucesso sem execução verificável.

## Memória e tracking

- Stark/Hermes deve usar o Shared Memory MCP antes, durante e depois da task.
- A GitHub Issue #54 é a memória operacional universal e deve permanecer aberta.
- Toda GRU exige Linear + GitHub Issue mirror; mudanças no repositório exigem PR com links bidirecionais.
- Nunca registre secrets, tokens, passwords, connection strings privadas, cookies, raw audio ou PII desnecessária.

## Branch, PR e estados

Parta da `origin/main`, confirme a branch da GRU antes de criá-la, faça commits rastreáveis, publique a branch e abra um único PR para a GRU. Aguarde CI aplicável e entregue em `Ready for Review`; a GRU deve ficar `In Review`, nunca `Done`, até a revisão de Anorak.

## Ambiente atual

- Stark / Hermes: GRU-104 foi executada em `LOCAL` como bootstrap histórico; GRU-105, GRU-101 e validações realtime/runtime seguintes usam a `Stark VPS`, salvo mudança explícita em uma GRU futura.
- Arquimedes / OpenCode: `vultur-vps` via alias SSH quando autorizado por uma GRU própria.
- Neriel / OpenClaw: `vultur-vps` via alias SSH quando autorizado por uma GRU própria.
- `vultur-vps` não é alias da Stark VPS. Não registrar IP, credenciais ou segredos.

## Ownership

- **Stark / Hermes:** Go runtime, APIs, realtime boundaries, Gemini Live boundary e serviços internos.
- **Neriel / OpenClaw:** domínio, state machines, comportamento conversacional e contratos de orquestração.
- **Arquimedes / OpenCode:** infraestrutura, providers, integrações externas, observabilidade e deploy/reprodutibilidade.
- **Anorak:** revisão final baseada no diff e nas evidências.

Para detalhes, use os documentos especializados do índice canônico.
