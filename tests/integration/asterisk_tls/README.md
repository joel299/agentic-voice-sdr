# Asterisk/PJSIP TLS gate

Harness REAL autossuficiente para validar o transporte PJSIP com Asterisk real.
O modo normal apenas faz skip; o modo REAL gera CA, chave privada, certificado
com `SAN=DNS:sip.provider.test`, `asterisk.conf`, `pjsip.conf`, resolver,
hosts, logs e runtime em diretório temporário. Nenhuma chave ou credencial é
versionada.

Pré-requisitos: `openssl`, `python3`, `unshare` e Asterisk `22.5.2` no PATH.

Modo normal:

```bash
tests/integration/asterisk_tls/run.sh
```

Modo REAL:

```bash
ASTERISK_GRU83_MODE=real tests/integration/asterisk_tls/run.sh
```

O modo REAL usa Asterisk/PJSIP como cliente TLS, sem `openssl s_client` como
prova final. Valida hostname correto, hostname incorreto com o mesmo
certificado (falha de identidade esperada), `verify_server=yes`, reload real
do resolver e PJSIP, hot update do pin A (`127.0.0.1`) para B
(`127.0.0.2`) na mesma porta, PID invariável e cleanup completo. O log bruto
sanitizado é preservado em `/tmp/gru83-last-asterisk.log` e a saída CLI em
`/tmp/gru83-last-cli.log`.
Falha de startup, reload, hostname positivo, hostname negativo, pin B ou PID
faz o harness terminar com código diferente de zero; não há `|| true` nos
passos críticos.
