# Loop Engineering

Todo trabalho executável do Agentic Voice SDR segue exatamente:

```text
SPEC -> TEST -> RED -> IMPLEMENT -> GREEN -> REFACTOR -> REGRESSION -> EVIDENCE -> REVIEW
```

## Estágios

1. **SPEC:** ler a Linear GRU, mirror GitHub, contexto canônico, dependências e `ALLOWED_SCOPE`; declarar critérios de aceite.
2. **TEST:** identificar testes/checks aplicáveis e validar o estado inicial.
3. **RED:** executar uma validação que demonstre a ausência, falha ou lacuna a ser corrigida. Para documentação, usar checks mecânicos de arquivos, links, estados e consistência.
4. **IMPLEMENT:** fazer a menor mudança necessária, preservando conteúdo correto e sem expandir escopo.
5. **GREEN:** repetir os checks e provar que os critérios agora passam.
6. **REFACTOR:** remover duplicação/drift sem mudar o contrato.
7. **REGRESSION:** executar a suíte e validações relevantes; confirmar que conteúdo existente válido permanece.
8. **EVIDENCE:** registrar diff, comandos, resultados, CI, decisões, riscos, blockers e próximos passos, sem secrets.
9. **REVIEW:** publicar branch/PR, aguardar CI aplicável e entregar `Ready for Review` ao Anorak. A GRU fica `In Review`, não `Done`.

## Regras de parada

Nenhum agente deve parar em análise, entregar apenas instruções ou declarar sucesso sem evidência. Também não deve ignorar CI nem alterar arquivos fora do `ALLOWED_SCOPE`.

A execução só pode ser interrompida por:

- `HUMAN_GATE` explícito;
- permission blocker real;
- dependência externa indisponível;
- conflito de escopo que exija decisão humana.

## Documentação

Em documentação, `TEST/RED/GREEN` significa validar mecanicamente a estrutura, presença dos arquivos obrigatórios, links internos, referências, estados e ausência de secrets acidentais. Toda afirmação de implementação deve ser sustentada por código, configuração, teste ou contrato existente.

## Handoff mínimo

Informe GRU, ambiente, branch, commit/head, arquivos, escopo, validações locais, CI, PR, decisões, riscos, blockers e próximo responsável. Assine handoffs como `— Stark` quando Stark executar a ação.
