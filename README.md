# croupier

Serviço de processamento de carteiras (wallets) e apostas (wagering) para provedores de jogos (iGaming), desenvolvido como solução do [backend-challenge-go](https://github.com/junglegaming/backend-challenge-go).

> Em desenvolvimento. Progresso e escopo detalhado em [TODO.md](TODO.md).

## Pré-requisitos

_(a preencher na Fase 10)_

## Variáveis de ambiente

_(a preencher na Fase 10 — ver `.env.example`)_

## Subindo o ambiente local (Docker Compose)

_(a preencher na Fase 10)_

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
- `wager-transactions.fifo` — fila de entrada (submissões de wager transaction via SQS, mesmo formato do corpo de `POST /wagering/transactions`), com `RedrivePolicy` (`maxReceiveCount=5`) apontando pra...
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

_(a preencher na Fase 10)_

## Exemplos de chamadas

A aplicação em si (`cmd/croupier`) ainda não existe (Fase 10) — os exemplos abaixo assumem um `*httpapi.Server` (ver `internal/httpapi`) servindo em `localhost:8080`, o que hoje só acontece dentro dos testes de integração (`internal/httpapi/integration_test.go`). Todas as rotas abaixo exigem um token — ver "Autenticação (IdP / Keycloak)" acima pra obter um.

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
curl -s -X POST localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" -H 'Content-Type: application/json' \
  -d '{"playerId":"11111111-1111-1111-1111-111111111111","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

Consultar uma wallet:
```sh
curl -s localhost:8080/wallets/<walletId> -H "Authorization: Bearer $INTERNAL_TOKEN"
```

Submeter uma aposta (rota de wagering — exige `$PROVIDER_TOKEN`; `providerId` vem do token, não vai no corpo. O header `Idempotency-Key`, quando enviado, precisa bater com `providerId:externalTransactionId` — ver TODO.md, Fase 5):
```sh
curl -s -X POST localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:ext-1' \
  -d '{
    "externalTransactionId": "ext-1",
    "playerId": "11111111-1111-1111-1111-111111111111", "walletId": "<walletId>",
    "roundId": "round-1", "gameId": "game-1",
    "kind": "BET", "amount": {"amount":"30.00","currency":"BRL"}
  }'
```

Consultar uma transação por id interno (só `$INTERNAL_TOKEN`) ou por `(providerId, externalTransactionId)` (o provider só acessa as próprias — path `providerId` precisa bater com o do token, exceto pra `$INTERNAL_TOKEN`, que acessa qualquer uma):
```sh
curl -s localhost:8080/wagering/transactions/<transactionId> -H "Authorization: Bearer $INTERNAL_TOKEN"
curl -s localhost:8080/providers/provider-a/wagering/transactions/ext-1 -H "Authorization: Bearer $PROVIDER_TOKEN"
```

Ledger paginado por cursor e reconciliação (rotas de wallet — `$INTERNAL_TOKEN`):
```sh
curl -s "localhost:8080/wallets/<walletId>/ledger?limit=20" -H "Authorization: Bearer $INTERNAL_TOKEN"
curl -s -X POST localhost:8080/wallets/<walletId>/reconciliation -H "Authorization: Bearer $INTERNAL_TOKEN"
```

Healthchecks:
```sh
curl -s localhost:8080/health/live
curl -s localhost:8080/health/ready   # 503 se Postgres estiver inacessível
```

## Rodando os testes

_(a preencher por completo na Fase 13)_

```sh
go test ./...
go test -race ./...
go vet ./...
```

Testes de integração de `internal/postgres` (`//go:build integration`) exigem Postgres real com as migrations aplicadas — ver "Migrations" acima:
```sh
docker compose up -d postgres
# aplicar as migrations (comandos na seção "Migrations")
TEST_DATABASE_URL="postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable" \
  go test -tags integration -race -count=1 ./internal/postgres/...
```

Testes de integração de `internal/sqs` exigem Postgres **e** LocalStack (as filas são provisionadas sozinhas — ver "Inicialização das filas" acima):
```sh
docker compose up -d postgres localstack
TEST_DATABASE_URL="postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable" \
SQS_ENDPOINT="http://localhost:4566" \
  go test -tags integration -race -count=1 ./internal/sqs/...
```

Testes de integração de `internal/auth` exigem Keycloak real (realm provisionado sozinho — ver "Autenticação" acima); `internal/httpapi` exige Postgres **e** Keycloak juntos, já que o fluxo completo passa pelos dois:
```sh
docker compose up -d postgres keycloak
TEST_DATABASE_URL="postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable" \
KEYCLOAK_ISSUER_URL="http://localhost:8080/realms/croupier" \
  go test -tags integration -race -count=1 ./internal/auth/... ./internal/httpapi/...
```
