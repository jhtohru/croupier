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

_(a preencher na Fase 8)_

## Autenticação (IdP / Keycloak)

_(a preencher na Fase 9 — provisionamento automático, identidades de teste, fluxo autenticado de exemplo)_

## Rodando a aplicação

_(a preencher na Fase 10)_

## Exemplos de chamadas

_(a preencher na Fase 7)_

## Rodando os testes

_(a preencher na Fase 13)_

```sh
go test ./...
go test -race ./...
go vet ./...
```
