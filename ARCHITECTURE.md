# ARCHITECTURE

Decisões técnicas do croupier. Preenchido incrementalmente conforme o [TODO.md](TODO.md) avança — cada seção referencia a fase que a define.

## Visão geral

_(a preencher — diagrama/descrição geral do fluxo provider → wager → wallet → ledger → outbox, ao final das Fases 1-5)_

## Representação de dinheiro (Money)

`Money` é um value object imutável (`internal/money`), com dois campos privados: `currency` (código ISO 4217) e `amount` (`int64`, em unidade mínima da moeda — centavos, no caso de BRL).

**Por que `int64` em vez de uma biblioteca decimal.** O domínio trabalha com escala fixa de 2 casas decimais e apenas operações de soma, subtração, negação e comparação — nunca multiplicação ou divisão fracionária. Nesse cenário, `int64` em unidades mínimas é exato por construção (aritmética inteira, sem arredondamento), não introduz dependência externa, e é o padrão usado por processadores de pagamento reais (ex.: a API da Stripe representa valores como inteiro na menor unidade da moeda). Bibliotecas de precisão arbitrária (`shopspring/decimal`, `cockroachdb/apd`) foram consideradas e descartadas: elas não conhecem a regra "escala fixa em 2 casas" — essa validação teria que ser reimplementada por cima da biblioteca do mesmo jeito que já é feita sobre `int64`, sem ganho real dado o escopo do desafio. Dinheiro nunca passa por `float32`/`float64` em nenhum ponto do código.

**Money é simétrico quanto ao sinal.** O tipo aceita e representa valores negativos livremente — é matematicamente coerente que `Money` seja negativo (ex.: a `difference` de uma reconciliação, ou um delta de ledger). A regra "um valor de aposta não pode ser negativo" **não** é responsabilidade de `Money`; é uma regra de `WagerTransaction`, validada por tipo de operação (BET/WIN/REFUND/ROLLBACK exigem `!amount.IsNegative()`; LOSS exige `amount.Equal(Zero(currency))`). Isso mantém `Money` livre de conhecimento sobre um contexto de negócio específico, reutilizável em qualquer situação que precise representar uma quantia monetária, positiva ou não.

**Validação de moeda.** `validateCurrency` usa `golang.org/x/text/currency.ParseISO`, que valida contra a tabela real da ISO 4217 (mantida junto ao próprio Go, derivada do CLDR da Unicode) — não é só uma checagem estrutural de "3 letras maiúsculas". Isso evita manter uma lista própria de ~180 códigos e lida corretamente com códigos especiais reservados (ex.: `XTS`, `XXX`).

**Formato de string do amount.** Regex `^-?(0|[1-9]\d*)\.\d{2}$`: exige exatamente 2 casas decimais, rejeita zeros à esquerda desnecessários, e rejeita implicitamente qualquer coisa que não seja dígitos/ponto/sinal — o que já cobre `NaN`, `Infinity` e notação científica sem precisar de regra especial pra cada um. Sinal negativo é opcional.

Exceção deliberada: a string `"-0.00"` é rejeitada mesmo batendo na regex. "Zero negativo" não é um valor diferente de zero, mas seria uma segunda forma de escrever a mesma quantia — quebraria a exigência de uma representação canônica única por valor, necessária para o hash determinístico do payload usado na idempotência (`{providerId}:{externalTransactionId}` + hash canônico de campos de negócio). `"+0.00"` já é rejeitado de graça, já que a regex não aceita `+` em lugar nenhum.

**Dois construtores, dois pontos de confiança diferentes.**
- `New(currency, amountStr string) (Money, error)`: parseia uma string externa (não confiável) — HTTP, SQS, JSON. É o ponto estrito de validação.
- `FromMinorUnits(currency string, amount int64) (Money, error)`: reconstrói `Money` a partir de um dado já confiável (ex.: uma linha escaneada do Postgres via `pgx`, coluna `BIGINT`), sem parsing de string. Necessário porque os campos de `Money` são privados — nenhum outro pacote consegue montar a struct diretamente — e porque nesse caso o dado já chega como `int64`, não como string formatada.
- `Zero(currency)` é um atalho para `FromMinorUnits(currency, 0)`.

**Overflow.** `Add`, `Subtract` e `Negate` verificam overflow de `int64` explicitamente e retornam `ErrOverflow`, aproveitando o fato de que Go define (não é UB, ao contrário de C) o comportamento de wraparound em overflow de inteiro assinado — a checagem compara o resultado contra os operandos pra detectar o wraparound. `Subtract(n)` é implementado como `Add(Negate(n))`, reaproveitando a checagem de `Add`. `Negate` trata `math.MinInt64` como caso especial, já que negar esse valor estouraria o `int64`.

**Serialização JSON.** Formato de objeto, não string concatenada: `{"currency": "BRL", "amount": "25.00"}` — `amount` é string, não número JSON, pra não arriscar perda de precisão em parsers que decodificam número JSON como `float64`. A conversão `int64 → string` (`amountString`) usa `strconv.FormatInt` seguido de manipulação de string (inserir o ponto decimal, extrair o sinal) em vez de divisão/módulo — a abordagem aritmética (`amount/100`, `amount%100`) produz resultado incorreto pra valores negativos (os dois já vêm negativos, gerando `"-123.-45"` em vez de `"-123.45"`) e não teria como lidar com `math.MinInt64` sem estourar ao tentar obter o valor absoluto.

**Decisões descartadas por YAGNI.** `Money.String()` foi removido — nenhum consumidor real dependia dele, e `fmt` já imprime campos não exportados via reflection quando necessário para debug/teste. Um método `Sign()` (retornando -1/0/1) não foi criado — `IsNegative() bool` cobre a única necessidade concreta identificada até agora (validação de sinal em `WagerTransaction`).

**Erros sentinela:** `ErrInvalidAmount`, `ErrInvalidCurrency` (adjetivo + substantivo, espelham as mensagens `"invalid amount"`/`"invalid currency"`), `ErrCurrencyMismatch` (substantivo + substantivo, espelha `"currency mismatch"` — ordem diferente porque "mismatch" é substantivo, sem forma adjetiva natural equivalente), `ErrOverflow`.

## Wallet e invariantes de saldo

`Wallet` (`internal/wallet`) é o aggregate root identificado por `(playerId, currency)` — não existe um campo `currency` redundante na struct; a moeda é sempre `balance.Currency()`. Campos privados (`id`, `playerId`, `version`, `balance`, `createdAt`, `updatedAt`), expostos só por getters — a única forma de mutar o saldo é através dos métodos `Credit`/`Debit` do próprio agregado.

**O invariante de saldo não-negativo mora no agregado, não no caso de uso.** Isso é uma escolha deliberadamente diferente da que fizemos pra `Money`: lá, "não pode ser negativo" era uma regra de um contexto de negócio específico (`WagerTransaction`), porque `Money` é usado em vários contextos onde negativo é legítimo (ex.: `difference` de reconciliação). Aqui não há contexto nenhum em que uma `Wallet` válida possa ter saldo negativo — é o que define uma `Wallet` válida, sempre. Por isso a checagem fica dentro de `Credit`/`Debit`/`New`, tornando um saldo negativo estruturalmente impossível de existir em memória, em vez de depender de todo caso de uso lembrar de checar antes de mutar.

**Dois erros de saldo distintos, de propósito:**
- `ErrNegativeInitialBalance` (em `New`): saldo inicial inválido na criação da wallet — checagem estrutural de entrada.
- `ErrInsufficientBalance` (em `Debit`): o resultado de um débito específico deixaria o saldo negativo — resultado de uma operação de negócio.

Mantê-los separados (em vez de reaproveitar um erro genérico) é necessário porque a Fase 3 exige failure codes distintos pra "BET com saldo insuficiente" vs. "reversão que excede saldo" — a distinção semântica já nasce aqui, no nível da `Wallet`.

**`Credit`/`Debit` exigem `amount` estritamente positivo** (`ErrNonPositiveAmount`, rejeita zero e negativo). Dois motivos: (1) a direção do movimento é decidida por qual método é chamado, não pelo sinal do valor — aceitar negativo em `Credit` abriria uma forma de debitar sem passar pela checagem de saldo suficiente de `Debit`; (2) protege o invariante "version só incrementa quando o saldo muda" — um valor zero seria um "movimento" que não movimenta nada, e não deveria conseguir incrementar a versão.

**Version incrementa só em mutação bem-sucedida.** `Credit`/`Debit` só executam `w.version++` depois que a operação de saldo (`Add`/`Subtract`) é confirmada sem erro — qualquer falha (moeda incompatível, overflow, saldo insuficiente, amount não-positivo) retorna antes de tocar em `balance` ou `version`. `version` existe também como preparação pra a estratégia de concorrência otimista que será decidida na Fase 5.

**`FromPersistence` não revalida os dados.** Ao contrário de `New`, que é a porta de entrada para dado não confiável, `FromPersistence` assume que o dado já passou pela escrita (e, portanto, pelas constraints do schema) — reconstituir e revalidar de novo seria redundante. Essa decisão cria uma dependência explícita com a Fase 6: a tabela `wallets` precisa de `CHECK (balance >= 0)`, `CHECK (version >= 1)` e `NOT NULL` em `id`/`player_id`/`created_at`/`updated_at`, senão nenhuma camada garante esses invariantes pra dado lido de volta do banco.

## WagerTransaction, estados e tipos

_(Fase 3)_

## Ledger (WalletLedgerEntry)

_(Fase 3)_

## Reversões: REFUND e ROLLBACK

_(Fase 3 — documentar explicitamente a interação entre os dois)_

## Idempotência

_(Fase 5)_

## Referências pendentes (PENDING_REFERENCE)

_(Fase 5)_

## Estratégia de concorrência

_(Fase 5 — escolha entre lock pessimista, controle otimista com retry ou update condicional atômico, e justificativa)_

## Inbox / Outbox

_(Fase 4 esboço, detalhado na Fase 8)_

## Persistência (PostgreSQL)

_(Fase 6)_

## Mensageria (SQS)

_(Fase 8)_

## Autenticação e Autorização

_(Fase 9 — escolha do IdP, validação de credenciais, modelo de permissão)_

## Composição (Uber Fx) e ciclo de vida

_(Fase 10)_

## Graceful shutdown

_(Fase 10)_

## Observabilidade

_(Fase 11)_

## Limitações, interpretações e trabalho incompleto

_(Fase 13 — revisão final)_

## Instruções de teste

### Preparo do ambiente

_(Fase 13)_

### Testes de integração

_(Fase 13)_

### Simulando múltiplas instâncias

_(Fase 12)_

### Simulando falhas (kill, restart, interrupção)

_(Fase 12)_
