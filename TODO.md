# TODO — croupier

Legenda:
- `[!]` crítico / desqualificante se faltar
- `[~]` importante, mas negociável sob pressão de tempo
- `[o]` diferencial opcional (cortar primeiro se faltar tempo)
- `[doc]` tarefa de documentação incremental (preencher a seção correspondente de README.md/ARCHITECTURE.md ao concluir a fase — não deixar tudo para o final)

Prazo: entrega segunda-feira. Priorize tudo marcado `[!]` antes de qualquer `[o]`.

---

## Fase 0 — Setup do projeto
- [ ] `[!]` Estrutura de pacotes por aggregate/conceito (não por camada arquitetural) — convenção definida abaixo; cada diretório é criado naturalmente ao escrever o primeiro arquivo da fase correspondente (não há pacote vazio pré-criado em Go):
  ```
  internal/
    money/     // Money (já existe)
    wallet/    // Wallet, WalletLedgerEntry, Repository interface, casos de uso (Service)
    wager/     // WagerTransaction, Repository interface, casos de uso (Service)
    inbox/
    outbox/
    postgres/  // implementa wallet.Repository, wager.Repository, inbox/outbox repos (pgx)
    sqs/       // consumer/producer
    auth/      // validação OIDC/Keycloak, extração de providerId
    httpapi/   // handlers HTTP (evita colisão de nome com net/http)
  cmd/
    croupier/  // main.go, wiring com Fx
  ```
  Interfaces de repositório (`WalletRepository`, `WagerRepository`, `OutboxRepository`, `TxManager`) ficam em `internal/app` — quem as consome de fato é o caso de uso, não o próprio agregado (correção feita na Fase 5: a nota original dizia `wallet.Repository` dentro de `internal/wallet`, mas isso só fazia sentido antes de existir a camada `internal/app`). Nunca em `internal/postgres` — mantém o domínio e o caso de uso livres de dependência de infraestrutura; só o pacote `postgres` (Fase 6) sabe que existe um banco.
- [x] `[~]` `.gitignore` (`.env`) — sem `Makefile`/scripts auxiliares ainda, opcional
- [x] `[!]` Iniciar `README.md` e `ARCHITECTURE.md` com esqueleto de seções — feito (ver [README.md](README.md) e [ARCHITECTURE.md](ARCHITECTURE.md))

## Fase 1 — internal/money
- [x] `[!]` `Money` value object: `currency` (ISO 4217) + `amount` (int64 em unidade mínima, escala fixa 2 casas)
- [x] `[!]` `New(currency, amount string)`: parse com regex, rejeita NaN/Infinity/notação científica/escala excedente/zeros à esquerda — bug do `ParseInt` corrigido (ponto removido antes de parsear)
- [x] `[!]` Money é simétrico quanto ao sinal (regex aceita `-` opcional) — Money não rejeita negativo; "amount não pode ser negativo" é regra de `WagerTransaction` por tipo, não de Money (ver Fase 3)
- [x] `[!]` `Zero(currency)`, comparação (`Equal`, `LessThan` etc.), sinal (`IsNegative`), serialização JSON (`MarshalJSON`/`UnmarshalJSON`, formato `{"amount": "...", "currency": "..."}`)
- [x] `[!]` `Add`, `Subtract`, `Negate` com checagem de overflow — implementado (checagem por comparação de sinal em `Add`, `Subtract` via `Negate`+`Add`, `Negate` corrigido para checar `MinInt64`)
- [x] `[!]` `FromMinorUnits(currency string, amount int64) (Money, error)`: reconstrói Money a partir de dado já confiável (ex.: linha do Postgres escaneada via pgx), sem parsing de string — necessário porque os campos de Money são privados e a coluna no banco será `BIGINT`, não uma string formatada (consumido a partir da Fase 6)
- [x] `[!]` Testes unitários: parsing válido/inválido, aritmética, limites (overflow), mismatch de moeda, escala — `go test -race -count=1 ./...` passando
- [ ] `[o]` Refatorar `money_test.go` pro padrão de testes de tabela adotado a partir da Fase 2 em diante (baixa prioridade — só depois de fechar os itens `[!]`)
- [ ] `[o]` Introduzir `const brl = "BRL"` (não exportado) em `money_test.go` pra substituir as ~122 ocorrências do literal `"BRL"` — reduz repetição e elimina risco de typo silencioso na moeda; não colocar em `money.go` (pacote é agnóstico de moeda de propósito)
- [x] `[doc]` ARCHITECTURE.md → "Representação de dinheiro (Money)"

## Fase 2 — internal/wallet (modelo)
- [x] `[!]` `Wallet` aggregate: id, `(playerId, currency)`, balance (`Money`), version (inicia em 1), timestamps — currency vem de `balance.Currency()`, sem campo redundante
- [x] `[!]` Invariante: débito não pode deixar saldo negativo — `Debit` retorna `ErrInsufficientBalance`; `New` retorna `ErrNegativeInitialBalance`
- [x] `[!]` Version incrementa **somente** quando o saldo muda — implementado em `Credit`/`Debit` (só incrementa após mutação de saldo confirmada)
- [x] `[!]` Testes unitários: invariantes de saldo, incremento de versão, casos de borda (débito exato do saldo, saldo insuficiente, overflow) — `go test -race -count=1 ./...` passando
- [x] `[doc]` ARCHITECTURE.md → "Wallet e invariantes de saldo"

## Fase 3 — internal/wager (modelo) + WalletLedgerEntry
- [x] `[!]` `WagerTransaction`: tipos OPENING/BET/WIN/LOSS/REFUND/ROLLBACK; estados PENDING → PROCESSED/REJECTED/FAILED (e PENDING_REFERENCE) — `Kind`/`TxStatus`, `IsTerminal()`, e os 4 métodos `Mark*` com transições corretas
- [x] `[!]` Campos: ids interno/externo, providerId, idempotency key, hash do payload, walletId, playerId, roundId, gameId, amount, referências, failure code — `IdempotencyKey()` e `PayloadHash()` como métodos computados, não campos armazenados
- [x] `[!]` Regra: OPENING só é criada internamente — `NewTransaction` rejeita `KindOpening` estruturalmente (cai no `default: ErrInvalidKind`), então `app.WagerSubmitter.Submit` já recusa automaticamente qualquer submissão externa com esse `Kind`, sem checagem extra necessária; `NewOpeningTransaction` é o único caminho válido, usado só por `WalletCreator.Create`
- [x] `[!]` Regras por tipo: aplicação de fato (debitar/creditar a Wallet, checar saldo suficiente) implementada em `app.WagerSubmitter.process`, incluindo a direção condicional do `ROLLBACK`
- [x] `[!]` Validação de sinal do amount é responsabilidade desta camada (WagerTransaction), não de Money: implementado via `IsPositive()`/`IsZero()` por `Kind` em `NewTransaction`
- [x] `[!]` Resolução de referência: `(providerId, referenceExternalTransactionId)` deve bater provider/player/wallet/currency/round/amount (sem parciais) — `ValidateReference` implementado e revisado (inclui checagem de `status == PROCESSED`)
- [x] `[!]` Impedir reversão duplicada do mesmo tipo sobre a mesma referência — `WagerRepository.FindReversal` + checagem em `app.WagerSubmitter.process` (`FailureCodeDuplicateReversal`)
- [x] `[!]` Failure code distinto para reversão que excede saldo vs. BET com saldo insuficiente — `FailureCodeInsufficientBalance` vs. `FailureCodeReversalExceedsBalance` em `internal/app/failure_codes.go`, testado separadamente
- [x] `[!]` `WalletLedgerEntry` (em `internal/wallet`, já que pertence ao aggregate Wallet): `LedgerEntry`, imutável, `Direction` (DEBIT/CREDIT), valida `balanceAfter = balanceBefore ± amount` (a partir dos dois valores observados, não recalculado) — testado
- [x] `[!]` Testes unitários: transições de estado, os 5 tipos externos + regras de zero por tipo, hash de payload (detecção de conflito), OPENING interno/eventos — `transaction_test.go` completo, `go test -race -count=1 ./...` passando
- [x] `[doc]` ARCHITECTURE.md → "WagerTransaction, estados e tipos", "Ledger (WalletLedgerEntry)", "Reversões: REFUND e ROLLBACK"

## Fase 4 — internal/inbox, internal/outbox (modelos)
- [x] `[!]` `Inbox`: `(consumerName, messageId)` único, hash, flag de conclusão (`completedAt *time.Time`) — "receipt" ficou como decisão em aberto, documentada no ARCHITECTURE.md
- [x] `[!]` `Outbox`: eventId estável, aggregate, type, payload, occurredAt, retry count, next send, status de publicação (`PENDING`/`PUBLISHED`, sem estado terminal de falha)
- [x] `[~]` Testes unitários dos invariantes desses modelos (unicidade é responsabilidade de schema/Fase 6; transições de status testadas)
- [x] `[doc]` ARCHITECTURE.md → "Inbox / Outbox" (esboço de domínio; mecânica de fila/worker detalhada na Fase 8)

## Fase 5 — Casos de uso (Service dentro de wallet/wager, sem pacote `usecase` separado)
- [x] `[!]` `wallet.Service.Create` (playerId, initialBalance) — implementado como `app.WalletCreator.Create` (não `wallet.Service`; ver correção de arquitetura acima sobre `internal/app`) — cria OPENING+ledger+outbox atomicamente se saldo inicial > 0; pula tudo isso se saldo = 0; conflito se player+currency já existe
- [x] `[!]` `wager.Service.Submit` — implementado como `app.WagerSubmitter.Submit`: idempotência via hash canônico do payload, kind-specific processing (BET/WIN/LOSS/REFUND/ROLLBACK, incluindo direção de ROLLBACK dependendo do tipo referenciado), resolução de referência com `PENDING_REFERENCE`, prevenção de reversão duplicada, failure codes distintos (saldo insuficiente em BET vs. reversão excedendo saldo) — compartilhável entre HTTP e SQS (mesmo input struct)
- [x] `[!]` Lógica de replay: mesma key+conteúdo → retorna resultado persistido (`idempotentReplay: true`) com o saldo **observado originalmente** (via `WalletRepository.FindLedgerEntryByTransactionID`), não o atual — testado explicitamente movendo o saldo entre a submissão original e o replay
- [x] `[!]` Conflito: mesma key com conteúdo diferente → `ErrIdempotencyConflict` — nota: "mesmo (providerId, externalTransactionId) não pode reaplicar com key diferente" é validação de header vs. corpo, fica na camada HTTP (Fase 7), já que `Submit` deriva a chave diretamente de `providerId`+`externalTransactionId`, não recebe um header separado
- [x] `[!]` `wager.Service.Get` / `GetByProvider` — implementado como `app.WagerTransactionGetter.Get`/`.GetByProvider`; isolamento por providerId é estrutural (o lookup sempre filtra pelo providerId passado — quem garante que esse valor é o da identidade autenticada, e não um valor arbitrário do cliente, é a camada HTTP/auth na Fase 7/9)
- [x] `[!]` `wallet.Service.Reconciliation` — implementado como `app.WalletReconciler.Reconcile`: recalcula saldo a partir do ledger (incluindo OPENING), compara com saldo armazenado, retorna `difference`, flag de consistência, contagem de entradas — não altera saldo
- [x] `[~]` Bônus não listado originalmente: `app.WalletLedgerLister.List` (GET /wallets/:walletId/ledger, paginação por cursor de ID, limite padrão/máximo 50)
- [ ] `[!]` Estratégia de concorrência por wallet (escolher: pessimista, otimista c/ retry, ou update condicional atômico) — decisão de design que molda a interface `wallet.Repository`
- [ ] `[!]` Worker de resolução de `PENDING_REFERENCE`: retry com backoff exponencial até max attempts/TTL, depois REJECTED
- [x] `[!]` Testes unitários de casos de uso (com repositórios fake/in-memory, sem infra real ainda) — `app.WalletCreator`, `app.WalletGetter`, `app.WagerSubmitter`, `app.WalletReconciler`, `app.WagerTransactionGetter`, `app.WalletLedgerLister` todos cobertos
- [ ] `[doc]` ARCHITECTURE.md → "Idempotência", "Referências pendentes (PENDING_REFERENCE)", "Estratégia de concorrência"

## Fase 6 — internal/postgres
- [ ] `[!]` Migrations versionadas (apply/rollback documentados) — schema com uniqueness, non-negativity (constraint de saldo), imutabilidade do ledger
- [ ] `[!]` **Dependência da decisão de `wallet.FromPersistence`/`ReHydrate` não revalidar dados vindos do banco**: a tabela `wallets` precisa de `CHECK (balance >= 0)`, `CHECK (version >= 1)` e `NOT NULL` em `id`/`player_id`/`created_at`/`updated_at` — sem essas constraints, não sobra nenhuma garantia desses invariantes em lugar nenhum (nem no domínio, nem no banco)
- [ ] `[!]` Implementação de `wallet.Repository`, `wager.Repository`, repositórios de inbox/outbox com `pgx` e SQL explícito (transações, locks, constraints verificáveis)
- [ ] `[!]` Outbox transacional: estado da transação + saldo + ledger + inbox + evento confirmados na mesma transação SQL
- [ ] `[!]` Testes de integração com Postgres real (Docker) — sem mocks completos
- [ ] `[doc]` README.md → "Migrations" (comandos de apply/rollback); ARCHITECTURE.md → "Persistência (PostgreSQL)"

## Fase 7 — internal/httpapi
- [ ] `[!]` `POST /wallets`, `GET /wallets/:walletId`, `GET /wallets/:walletId/ledger?cursor=&limit=`, `POST /wallets/:walletId/reconciliation`
- [ ] `[!]` `POST /wagering/transactions` (header `Idempotency-Key`), `GET /wagering/transactions/:transactionId`, `GET /providers/:providerId/wagering/transactions/:externalTransactionId`
- [ ] `[!]` `GET /health/live`, `GET /health/ready` (checando Postgres e SQS)
- [ ] `[!]` Middleware de autenticação/autorização (ver Fase 9) aplicado às rotas de negócio
- [ ] `[!]` Testes de integração HTTP (com Postgres + IdP reais)
- [ ] `[doc]` README.md → "Exemplos de chamadas"

## Fase 8 — internal/sqs
- [ ] `[!]` Filas `wager-transactions.fifo` + `wager-transactions-dlq.fifo` com redrive policy
- [ ] `[!]` Consumer: dedup por `messageId` + `data.idempotencyKey` via inbox; remove da fila só após commit durável
- [ ] `[!]` Retry com backoff; erros permanentes → DLQ
- [ ] `[!]` Shutdown gracioso (SIGTERM): parar de puxar mensagens, terminar in-flight dentro do deadline ou liberar para redelivery
- [ ] `[!]` Outbox worker: publica eventos pós-commit, suporta múltiplos publishers, recovery de trabalho abandonado, republicação preservando eventId
- [ ] `[!]` LocalStack/MiniStack no docker-compose para execução local
- [ ] `[!]` Testes de integração: redelivery após interrupção pós-commit/pré-ack, DLQ, recovery pós-restart
- [ ] `[doc]` README.md → "Inicialização das filas"; ARCHITECTURE.md → "Mensageria (SQS)", detalhar "Inbox / Outbox"

## Fase 9 — internal/auth
- [ ] `[!]` Integração com IdP externo (Keycloak recomendado, via docker-compose) — `client_credentials` para service-to-service
- [ ] `[!]` Validação de token, extração de `providerId` autorizado a partir da identidade autenticada
- [ ] `[!]` Rejeitar credenciais ausentes/inválidas/expiradas
- [ ] `[!]` Isolamento de provider em queries e replays (nenhum vazamento de dado entre providers)
- [ ] `[!]` Restringir operações de wallet ao uso interno (sem acesso via API de provider)
- [ ] `[!]` Provisionamento automático do IdP + identidades de teste (script/config no docker-compose ou README)
- [ ] `[!]` Testes de integração de auth (IdP real, não mock)
- [ ] `[doc]` README.md → "Autenticação (IdP / Keycloak)"; ARCHITECTURE.md → "Autenticação e Autorização"

## Fase 10 — cmd/croupier (Uber Fx) & Docker Compose & Env
- [ ] `[!]` Módulos Fx (`fx.Module`, `fx.Provide`, `fx.Invoke`) para server, workers, recursos
- [ ] `[!]` `fx.Lifecycle` com timeouts de start/shutdown observáveis
- [ ] `[!]` `docker-compose.yml`: app, Postgres, LocalStack, Keycloak — os três serviços de infra já estão no ar e verificados (subidos de verdade, healthcheck passando, testado que Postgres aceita conexão, SQS do LocalStack responde, Keycloak serve HTTP) — falta o serviço `app` (Dockerfile + `cmd/croupier`, Fase 10 adiante)
- [x] `[!]` `.env.example` com valores locais (sem secrets) — cobre Postgres/LocalStack/Keycloak; vai crescer quando HTTP/SQS/auth de verdade existirem
- [ ] `[!]` Dockerfile com versão do Go alinhada ao `go.mod`
- [ ] `[doc]` README.md → "Pré-requisitos", "Variáveis de ambiente", "Subindo o ambiente local (Docker Compose)", "Rodando a aplicação"; ARCHITECTURE.md → "Composição (Uber Fx) e ciclo de vida", "Graceful shutdown"

## Fase 11 — Observabilidade
- [ ] `[~]` Logs JSON com correlationId, messageId, transactionId, walletId, providerId (sem credenciais/payloads financeiros completos)
- [ ] `[~]` Métricas: status outcomes, duplicatas, retries, DLQ, conflitos de concorrência, latência de outbox/processamento, divergência de reconciliação
- [ ] `[o]` Tracing OpenTelemetry (diferencial opcional)
- [ ] `[o]` Dashboards (diferencial opcional)
- [ ] `[doc]` ARCHITECTURE.md → "Observabilidade"

## Fase 12 — Cenários obrigatórios de concorrência e recuperação
- [ ] `[!]` 50 requisições paralelas da mesma aposta → um único débito
- [ ] `[!]` Wallet com 100.00 BRL + duas apostas concorrentes de 80.00 → uma processada, uma rejeitada, saldo final 20.00, um único ledger entry
- [ ] `[!]` Wallets distintas processam em paralelo sem lock global
- [ ] `[!]` 3+ instâncias independentes replicam os cenários acima
- [ ] `[!]` Interromper consumer após commit e antes da remoção da mensagem → verificar redelivery
- [ ] `[!]` Dois publishers disputando o mesmo outbox → verificar recovery sem duplicar evento
- [ ] `[!]` REFUND/ROLLBACK entregue antes da referência existir → `PENDING_REFERENCE`, depois resolve ou expira (TTL) como REJECTED
- [ ] `[!]` Reiniciar app → idempotência, trabalho pendente e consistência financeira preservados
- [ ] `[!]` Mesma operação via HTTP e via SQS → duplicidade tratada corretamente
- [ ] `[!]` Validação final: saldo armazenado = Σ(créditos) − Σ(débitos) do ledger
- [ ] `[!]` `go test -race ./...` passando em todos os testes aplicáveis
- [ ] `[doc]` ARCHITECTURE.md → "Instruções de teste" (simulando múltiplas instâncias, simulando falhas)

## Fase 13 — Checklist final
- [ ] `[!]` `gofmt` aplicado em tudo
- [ ] `[!]` `go vet ./...` limpo
- [ ] `[!]` `go test ./...` e `go test -race ./...` passando
- [ ] `[!]` Revisar lista de "falhas desqualificantes" do desafio uma a uma antes de entregar
- [ ] `[doc]` ARCHITECTURE.md → "Limitações, interpretações e trabalho incompleto"; revisão final de README.md/ARCHITECTURE.md por consistência
- [ ] `[o]` Diferenciais: double-entry bookkeeping completo, load testing com métricas (p50/p95/p99)

---

## Falhas desqualificantes (revisar antes de entregar)
- Ausência de autenticação efetiva em endpoints de negócio
- Acesso não autorizado a operações/transações de outro provider
- Cálculo de dinheiro usando float
- Saldo negativo via concorrência
- Movimento duplicado
- Idempotência apenas em memória
- Correção depender de uma única instância
- Publicação de evento antes do commit
- Ledger não auditável (edição/remoção de entradas)
- Testes com mock completo substituindo Postgres/SQS/IdP
