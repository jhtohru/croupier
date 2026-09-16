# croupier

Serviço de processamento de carteiras (wallets) e apostas (wagering) para provedores de jogos (iGaming), desenvolvido como solução do [backend-challenge-go](https://github.com/junglegaming/backend-challenge-go).

> Em desenvolvimento. Progresso e escopo detalhado em [TODO.md](TODO.md).

## Pré-requisitos

- Docker + Docker Compose (v2, `docker compose`) — é assim que Postgres, LocalStack, Keycloak e a própria aplicação sobem
- Go (versão travada em [go.mod](go.mod), hoje `1.26.0`) — só necessário pra rodar testes ou `go run` fora de container
- `python3` — usado só nos exemplos deste README pra extrair `access_token` do JSON de resposta do Keycloak (`curl | python3 -c '...'`); não é dependência do projeto em si

## Variáveis de ambiente

Todas em [.env.example](.env.example), com valores padrão seguros pra rodar local (nenhum é segredo real). Copie pra `.env` e ajuste se quiser:
```sh
cp .env.example .env
```
`docker compose` já lê `.env` automaticamente. As variáveis cobrem Postgres, LocalStack/SQS, Keycloak e a própria aplicação (`cmd/croupier`, Fase 10) — a maioria tem default embutido no código (ver `cmd/croupier/config.go`) e só precisa ser setada se você quiser um valor diferente.

## Subindo o ambiente local (Docker Compose)

```sh
docker compose up -d --build
```

Sobe, nessa ordem de dependência (via `depends_on` com `condition: service_healthy`), Postgres, LocalStack (filas provisionadas sozinhas, ver "Inicialização das filas") e Keycloak (realm provisionado sozinho, ver "Autenticação"), e só então a própria aplicação (`app`) — que aplica as migrations automaticamente antes de aceitar tráfego (ver `cmd/croupier/migrate.go`; os comandos manuais na seção "Migrations" abaixo continuam funcionando, mas não são mais um passo obrigatório).

Conferir que tudo subiu saudável:
```sh
docker compose ps
```

Ver logs da aplicação (inclusive o log de cada `fx.Hook` de start/stop, útil pra entender a ordem de inicialização):
```sh
docker compose logs -f app
```

**Nota sobre rede**: o serviço `app` roda com `network_mode: host` (ver o comentário no `docker-compose.yml`) — Keycloak em modo dev resolve o `iss` de cada token dinamicamente a partir do header `Host` da requisição, então a aplicação precisa enxergar Postgres/LocalStack/Keycloak pelos mesmos nomes (`localhost:<porta>`) que você usa nos exemplos de `curl` deste README, não pelos nomes internos da rede do compose (`postgres`, `localstack`, `keycloak`) — senão o token que você obtém via `localhost:8080` não bate com o emissor que a aplicação espera.

## Migrations

Migrations ficam em `internal/postgres/migrations` (pares `.up.sql`/`.down.sql`, formato [golang-migrate](https://github.com/golang-migrate/migrate)). Não precisa instalar nada localmente — os comandos abaixo rodam a própria imagem oficial do `migrate` conectada à rede do `docker-compose.yml`.

Suba o Postgres primeiro:
```sh
docker compose up -d postgres
```

Aplicar todas as migrations pendentes:
```sh
docker run --rm --network croupier_default \
  -v "$(pwd)/internal/postgres/migrations:/migrations" \
  migrate/migrate:v4.17.1 \
  -path=/migrations -database "postgres://croupier:croupier@postgres:5432/croupier?sslmode=disable" \
  up
```

Reverter tudo:
```sh
docker run --rm --network croupier_default \
  -v "$(pwd)/internal/postgres/migrations:/migrations" \
  migrate/migrate:v4.17.1 \
  -path=/migrations -database "postgres://croupier:croupier@postgres:5432/croupier?sslmode=disable" \
  down -all
```

Reverter só a última:
```sh
docker run --rm --network croupier_default \
  -v "$(pwd)/internal/postgres/migrations:/migrations" \
  migrate/migrate:v4.17.1 \
  -path=/migrations -database "postgres://croupier:croupier@postgres:5432/croupier?sslmode=disable" \
  down 1
```

Ajuste usuário/senha/porta se você alterou os valores padrão do `.env.example`.

## Inicialização das filas (SQS / LocalStack)

Automática — não precisa rodar nada manualmente. Suba o LocalStack:
```sh
docker compose up -d localstack
```

`deploy/localstack/init-queues.sh` roda sozinho dentro do container (hook `ready.d` do LocalStack) assim que o serviço SQS sobe, e cria:
- `wager-transactions.fifo` — fila de entrada (submissões de wager transaction via SQS, envelope `{messageId, type, occurredAt, data}` — `data` carrega os mesmos campos do corpo de `POST /wagering/transactions` mais `idempotencyKey`; ver ARCHITECTURE.md → "Mensageria (SQS)"), com `RedrivePolicy` (`maxReceiveCount=5`) apontando pra...
- `wager-transactions-dlq.fifo` — dead-letter queue
- `wallet-events.fifo` — fila de saída (eventos de domínio publicados pelo outbox worker)

O healthcheck do serviço só reporta "healthy" depois que o script termina, então `docker compose up -d --wait localstack` (ou simplesmente esperar o `docker compose ps` mostrar `healthy`) garante que as filas já existem antes de qualquer coisa tentar usá-las. Conferir manualmente:
```sh
docker exec <container-do-localstack> awslocal sqs list-queues
```

## Autenticação (IdP / Keycloak)

Automática — não precisa configurar nada manualmente no admin console. Suba o Keycloak:
```sh
docker compose up -d keycloak
```

`deploy/keycloak/realm-export.json` é importado sozinho no start do container (`--import-realm`) e provisiona o realm `croupier` com três identidades de teste (`client_credentials`, service-to-service — sem fluxo de usuário/senha):

| Client            | Secret                     | Papel                                                              |
|-------------------|----------------------------|---------------------------------------------------------------------|
| `provider-a`      | `provider-a-secret`        | Provider — token carrega `providerId: "provider-a"`                |
| `provider-b`      | `provider-b-secret`        | Provider — token carrega `providerId: "provider-b"`                 |
| `internal-service`| `internal-service-secret`  | Uso interno — token carrega a role de realm `internal-service`      |

Obter um token (válido por 5 minutos):
```sh
curl -s -X POST http://localhost:8080/realms/croupier/protocol/openid-connect/token \
  -d 'grant_type=client_credentials' -d 'client_id=provider-a' -d 'client_secret=provider-a-secret' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])'
```

Rotas de wallet (`/wallets/*`) exigem um token com a role `internal-service`; rotas de wagering exigem qualquer token válido, com `providerId` extraído do token (nunca de corpo/path informado pelo cliente) — ver ARCHITECTURE.md → "Autenticação e Autorização".

## Rodando a aplicação

Via Docker Compose (recomendado — ver "Subindo o ambiente local" acima):
```sh
docker compose up -d --build app
```

Ou direto no host, com a infra (`postgres`, `localstack`, `keycloak`) já no ar via compose:
```sh
go run ./cmd/croupier
```

Em ambos os casos a aplicação: aplica as migrations pendentes, sobe o servidor HTTP (`APP_PORT`, padrão `8081`), e inicia os workers de fundo (consumer SQS de `wager-transactions.fifo`, resolvedor de `PENDING_REFERENCE`, publicador de outbox, poller de profundidade da DLQ) — tudo isso é o `fx.App` em `cmd/croupier/main.go`, ver ARCHITECTURE.md → "Composição (Uber Fx) e ciclo de vida". `Ctrl+C` (ou `docker compose stop app`) dispara shutdown gracioso: para de aceitar conexão nova, dá um tempo (`SHUTDOWN_TIMEOUT`, padrão 15s) pro que já estava em andamento terminar, então encerra.

Logs saem em JSON no stdout (`docker compose logs app`), com `correlationId` em toda linha de requisição HTTP/mensagem SQS. Métricas Prometheus ficam em `GET /metrics` (sem autenticação, como `/health/*`) — ver ARCHITECTURE.md → "Observabilidade" para a lista completa e o porquê de cada uma.

## Exemplos de chamadas

Os exemplos abaixo assumem a aplicação rodando em `localhost:8081` (padrão de `APP_PORT`, ver seção acima). Todas as rotas exigem um token — ver "Autenticação (IdP / Keycloak)" acima pra obter um.

```sh
INTERNAL_TOKEN=$(curl -s -X POST http://localhost:8080/realms/croupier/protocol/openid-connect/token \
  -d 'grant_type=client_credentials' -d 'client_id=internal-service' -d 'client_secret=internal-service-secret' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
PROVIDER_TOKEN=$(curl -s -X POST http://localhost:8080/realms/croupier/protocol/openid-connect/token \
  -d 'grant_type=client_credentials' -d 'client_id=provider-a' -d 'client_secret=provider-a-secret' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
```

Criar uma wallet com saldo inicial (rota de wallet — exige `$INTERNAL_TOKEN`):
```sh
curl -s -X POST localhost:8081/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" -H 'Content-Type: application/json' \
  -d '{"playerId":"11111111-1111-1111-1111-111111111111","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

Consultar uma wallet:
```sh
curl -s localhost:8081/wallets/<walletId> -H "Authorization: Bearer $INTERNAL_TOKEN"
```

Submeter uma aposta (rota de wagering — exige `$PROVIDER_TOKEN`; `providerId` vem do token, não vai no corpo). O header `Idempotency-Key` é **obrigatório** (`400` se ausente) e precisa bater com `providerId:externalTransactionId` — ver ARCHITECTURE.md → "API HTTP (internal/httpapi)". Resposta: `{transactionId, status, balance, idempotentReplay}`:
```sh
curl -s -X POST localhost:8081/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:ext-1' \
  -d '{
    "externalTransactionId": "ext-1",
    "playerId": "11111111-1111-1111-1111-111111111111", "walletId": "<walletId>",
    "roundId": "round-1", "gameId": "game-1",
    "kind": "BET", "money": {"amount":"30.00","currency":"BRL"}
  }'
```

Consultar uma transação por id interno (só `$INTERNAL_TOKEN`) ou por `(providerId, externalTransactionId)` (o provider só acessa as próprias — path `providerId` precisa bater com o do token, exceto pra `$INTERNAL_TOKEN`, que acessa qualquer uma):
```sh
curl -s localhost:8081/wagering/transactions/<transactionId> -H "Authorization: Bearer $INTERNAL_TOKEN"
curl -s localhost:8081/providers/provider-a/wagering/transactions/ext-1 -H "Authorization: Bearer $PROVIDER_TOKEN"
```

Ledger paginado por cursor e reconciliação (rotas de wallet — `$INTERNAL_TOKEN`):
```sh
curl -s "localhost:8081/wallets/<walletId>/ledger?limit=20" -H "Authorization: Bearer $INTERNAL_TOKEN"
curl -s -X POST localhost:8081/wallets/<walletId>/reconciliation -H "Authorization: Bearer $INTERNAL_TOKEN"
```

Healthchecks:
```sh
curl -s localhost:8081/health/live
curl -s localhost:8081/health/ready   # 503 se Postgres estiver inacessível
```

## Rodando os testes

Testes unitários (sem infraestrutura nenhuma — repositórios fake/in-memory) e `go vet`:
```sh
go test ./...
go test -race ./...
go vet ./...
```

Testes de integração (`//go:build integration`) exigem Postgres real — mas **não precisam de nenhuma configuração manual de banco**: `internal/postgres`, `internal/httpapi`, `internal/sqs` e `cmd/croupier` cada um tem seu próprio `TestMain` (`internal/testdb`) que dropa/recria e migra sozinho um banco dedicado (`croupier_test_postgres`, `croupier_test_httpapi`, ...) toda vez que rodam — nunca o banco `croupier` que o serviço `app` usa. Pode rodar com o `app` no ar sem medo: eles não competem pela mesma tabela nem pelo mesmo banco, estruturalmente. Só usa `POSTGRES_HOST`/`PORT`/`USER`/`PASSWORD` (as mesmas variáveis de sempre — ver `.env.example`), nenhuma `TEST_DATABASE_URL` pra configurar:
```sh
docker compose up -d postgres
go test -tags integration -race -count=1 ./internal/postgres/...
```

Testes de integração de `internal/sqs` exigem Postgres **e** LocalStack (as filas são provisionadas sozinhas — ver "Inicialização das filas" acima):
```sh
docker compose up -d postgres localstack
SQS_ENDPOINT="http://localhost:4566" \
  go test -tags integration -race -count=1 ./internal/sqs/...
```

Testes de integração de `internal/auth` exigem Keycloak real (realm provisionado sozinho — ver "Autenticação" acima); `internal/httpapi` e `cmd/croupier` exigem Postgres **e** Keycloak juntos, já que o fluxo completo passa pelos dois (`cmd/croupier` exige LocalStack também, já que testa HTTP e SQS juntos):
```sh
docker compose up -d postgres keycloak localstack
KEYCLOAK_ISSUER_URL="http://localhost:8080/realms/croupier" \
  go test -tags integration -race -count=1 ./internal/auth/... ./internal/httpapi/...
KEYCLOAK_ISSUER_URL="http://localhost:8080/realms/croupier" SQS_ENDPOINT="http://localhost:4566" \
  go test -tags integration -race -count=1 ./cmd/croupier/...
```
