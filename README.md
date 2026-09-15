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

_(a preencher na Fase 9 — provisionamento automático, identidades de teste, fluxo autenticado de exemplo)_

## Rodando a aplicação

_(a preencher na Fase 10)_

## Exemplos de chamadas

A aplicação em si (`cmd/croupier`) ainda não existe (Fase 10) — os exemplos abaixo assumem um `*httpapi.Server` (ver `internal/httpapi`) servindo em `localhost:8080`, o que hoje só acontece dentro dos testes de integração (`internal/httpapi/integration_test.go`). Sem autenticação ainda (Fase 9): `providerId` nas rotas de wagering vem direto do corpo/path informado, não de uma identidade verificada.

Criar uma wallet com saldo inicial:
```sh
curl -s -X POST localhost:8080/wallets \
  -H 'Content-Type: application/json' \
  -d '{"playerId":"11111111-1111-1111-1111-111111111111","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

Consultar uma wallet:
```sh
curl -s localhost:8080/wallets/<walletId>
```

Submeter uma aposta (o header `Idempotency-Key`, quando enviado, precisa bater com `providerId:externalTransactionId` do corpo — ver TODO.md, Fase 5):
```sh
curl -s -X POST localhost:8080/wagering/transactions \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:ext-1' \
  -d '{
    "providerId": "provider-a", "externalTransactionId": "ext-1",
    "playerId": "11111111-1111-1111-1111-111111111111", "walletId": "<walletId>",
    "roundId": "round-1", "gameId": "game-1",
    "kind": "BET", "amount": {"amount":"30.00","currency":"BRL"}
  }'
```

Consultar uma transação por id interno ou por `(providerId, externalTransactionId)`:
```sh
curl -s localhost:8080/wagering/transactions/<transactionId>
curl -s localhost:8080/providers/provider-a/wagering/transactions/ext-1
```

Ledger paginado por cursor e reconciliação:
```sh
curl -s "localhost:8080/wallets/<walletId>/ledger?limit=20"
curl -s -X POST localhost:8080/wallets/<walletId>/reconciliation
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

Testes de integração de `internal/postgres` e `internal/httpapi` (`//go:build integration`) exigem Postgres real com as migrations aplicadas — ver "Migrations" acima:
```sh
docker compose up -d postgres
# aplicar as migrations (comandos na seção "Migrations")
TEST_DATABASE_URL="postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable" \
  go test -tags integration -race -count=1 ./internal/postgres/... ./internal/httpapi/...
```

Testes de integração de `internal/sqs` exigem Postgres **e** LocalStack (as filas são provisionadas sozinhas — ver "Inicialização das filas" acima):
```sh
docker compose up -d postgres localstack
TEST_DATABASE_URL="postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable" \
SQS_ENDPOINT="http://localhost:4566" \
  go test -tags integration -race -count=1 ./internal/sqs/...
```
