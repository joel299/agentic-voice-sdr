# Canonical Context Index

Este é o índice canônico do contexto do Agentic Voice SDR. A autoridade é dividida por função: o repositório guarda código e documentação versionados; Linear guarda estado e dependências; GitHub Issues/PRs guardam o mirror e as evidências; Shared Memory guarda decisões e handoffs compartilhados.

## Mandatory pre-task reading

Todo agente deve consultar, antes de iniciar uma GRU:

- `CLAUDE.md`
- `AGENTS.md`
- `docs/CONTEXT_INDEX.md`
- `docs/PRD.md`
- `docs/REQUIREMENTS.md`
- `docs/ARCHITECTURE.md`
- `docs/LOOP_ENGINEERING.md`
- GitHub Issue #54: https://github.com/joel299/agentic-voice-sdr/issues/54
- Linear GRU atual
- GitHub Issue mirror atual

Depois, leia `docs/PROJECT.md`, `docs/ENGINEERING.md`, `docs/AGENT_PROTOCOL.md`, `docs/PROMPT_CACHE.md`, SDD, ADRs, TDD e os documentos da área alterada.

## Document authority

| Documento | Autoridade |
|---|---|
| `CLAUDE.md` | Entrada operacional e regras de leitura |
| `AGENTS.md` | Contrato de execução, escopo, tracking e CI |
| `docs/PRD.md` | Produto, MVP, regras comerciais e gates de produto |
| `docs/REQUIREMENTS.md` | Requisitos estáveis, IDs e estado de implementação |
| `docs/ARCHITECTURE.md` | Fronteiras técnicas e estado real da arquitetura |
| `docs/LOOP_ENGINEERING.md` | Processo obrigatório de execução e evidência |
| `docs/PROJECT.md` | Fluxos e roadmap já registrados |
| `docs/ENGINEERING.md` | Práticas de engenharia e layout |
| `docs/AGENT_PROTOCOL.md` | Protocolo de agentes, autorização e handoff |
| `docs/PROMPT_CACHE.md` | Contexto compacto para prompts operacionais |
| SDD/ADRs/TDD | Decisões e contratos técnicos especializados |
| GitHub Issue #54 | Memória operacional universal e fallback compartilhado |
| Linear | Estado, dependências e controle da GRU |
| Shared Memory MCP | Contexto/decisões/handoffs de Stark e Arquimedes |

## Estado documental

Os documentos distinguem explicitamente `IMPLEMENTED`, `IN_PROGRESS`, `PLANNED` e `TBD`. A existência de uma especificação, configuração ou teste não prova que uma integração de produção esteja implementada.

## Ambiente

- Stark/Hermes: GRU-104 foi executada em `LOCAL` como bootstrap histórico; GRU-105, GRU-101 e validações realtime/runtime seguintes usam a `Stark VPS`, salvo mudança explícita em uma GRU futura.
- Neriel/OpenClaw: `vultur-vps` via alias SSH.
- Arquimedes/OpenCode: `vultur-vps` via alias SSH.

A `Stark VPS` é um ambiente separado. Não assumir que ela usa o alias `vultur-vps`; não registrar IP, credenciais ou segredos. A matriz completa e as regras operacionais estão em `CLAUDE.md` e na Issue #54.

## Conflitos

Não resolva conflito factual por inferência. Registre a divergência, preserve o conteúdo válido e acione `HUMAN_GATE` quando a decisão for de produto/arquitetura ou estiver fora do contrato da GRU.
