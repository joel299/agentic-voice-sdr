# Asterisk/PJSIP TLS gate

Harness reproduzível para validar o certificado hostname-only e a política de
identidade TLS usada pela GRU-83. Ele gera CA/certificados em diretório
temporário, nunca grava chaves no repositório e não usa tráfego SIP produtivo.

Pré-requisitos: `openssl` e `asterisk` 22.x no PATH. Execute:

```bash
tests/integration/asterisk_tls/run.sh
```

O teste valida, com `openssl s_client`, o mesmo par de propriedades exigido
pelo transporte PJSIP: destino de rede pinado (`127.0.0.1`), SNI/identidade
lógica `sip.provider.test`, certificado hostname-only e rejeição de hostname
incorreto. O bloco Asterisk é executado contra um fixture temporário quando
`ASTERISK_GRU83_FIXTURE` aponta para uma configuração completa; caso contrário
o harness informa que a etapa PJSIP deve ser executada no ambiente com o
fixture Asterisk habilitado e encerra com sucesso apenas para permitir CI sem
Asterisk.

Nenhum segredo, certificado ou chave é versionado.
