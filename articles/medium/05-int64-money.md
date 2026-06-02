# Money is `int64` Minor Units: Every Place `float64` Will Bite You

Every greenfield financial system has a moment where someone types `Amount float64`. The cost of *not* catching that early scales with the size of the schema.

By the time you have a million rows, an event stream, a downstream analytics pipeline, and three integrations consuming the JSON, the type is no longer a choice. It's a contract. Changing it requires every party to redeploy in a coordinated migration, and there is no version of that migration where the reconciliation numbers come out clean on the first try.

This post is about why the only safe default for money in Go is `int64` (or a bounded fixed-point integer), why `decimal.Decimal` is not a sufficient excuse to do anything else, and the five concrete places `float64` would have caused trouble in the wallet-transfer service this article is extracted from.

---

## A 90-second IEEE 754 primer

IEEE 754 floats represent numbers as sign × mantissa × 2^exponent. The mantissa is a finite number of bits — 52 for `float64`. Numbers that aren't expressible as a finite binary fraction are approximated. `0.1` is one of them. So is `0.2`. So is most of the price list you've ever printed.

```go
package main

import "fmt"

func main() {
    a := 0.1
    b := 0.2
    fmt.Println(a + b) // 0.30000000000000004
}
```

This is not a Go bug. It's the same answer in Python, JavaScript, C, Rust, and every other language with IEEE 754 floats. The bug is choosing the type in the first place.

The error is small per-operation — on the order of 10^-16 for `float64`. The problem is that the error compounds across operations and across rows. Multiply a small per-op error by a million rows and the dust adds up to coins.

## The £1 dispute that becomes £10,000

The pattern that turns a rounding error into a production incident:

1. A back-office reconciliation report uses `SUM(amount)` over a `DOUBLE PRECISION` column.
2. A customer-facing balance display reads the same balance from a `DOUBLE PRECISION` column at a different timestamp.
3. The two figures disagree by some number of pennies.
4. A customer notices, opens a ticket.
5. Support escalates because the discrepancy is real.
6. An engineer is paged. They reproduce locally with three rows and conclude "it's a rounding error, harmless."
7. The discrepancy grows because the next million transfers each contribute a fraction of a penny in the same direction.
8. Six months later the discrepancy is large enough to feature in an audit.

There is no good moment to fix this. Every fix path requires backfilling historical balances from the ledger, which itself was computed under the same buggy arithmetic, which means the "correct" historical balance is a judgement call rather than a calculation.

The fix-at-greenfield is one decision: `BIGINT` everywhere, `int64` everywhere, and `decimal` only at the input/output boundary if humans are typing the numbers.

## Why `int64` minor units beats `decimal.Decimal` in Go

Other languages give you a first-class decimal type. Java has `BigDecimal`. C# has `decimal`. Python has `decimal.Decimal` in the standard library. Postgres has `NUMERIC`. All of these are correct.

Go does not have a first-class decimal type. The community options work — shopspring/decimal is the most common — but they introduce two costs:

**Cost 1: arithmetic doesn't look like arithmetic.**

```go
// With shopspring/decimal:
result := a.Add(b).Mul(rate).Div(hundred).Round(2)

// With int64 minor units:
result := (a + b) * rate / 100
```

The second form is something every Go developer reads on day one. The first form requires reading the decimal package's docs to know whether `Div` rounds, truncates, or returns an error, and what the precision of the intermediate result is.

**Cost 2: serialization boundary.**

`int64` round-trips through JSON, SQL, gRPC, Protobuf, and CSV as a literal number. `decimal.Decimal` round-trips as a string — which is correct! — but every consumer needs to know it's a string, and every consumer needs the same decimal library to parse it without precision loss.

The "int64 minor units" convention sidesteps both costs. Pence are integers. Pence add. Pence compare. Pence serialize. The unit (pence, cents, satoshis) lives in the schema documentation and the type system; the arithmetic stays plain Go.

The one place the convention falls down is when minor units aren't fine-grained enough — currencies with more than 8 decimal places, or financial products that quote to 10+ digits of precision. For those, you reach for `decimal`. For ordinary wallet flows, `int64` wins.

## Schema design: BIGINT + CHECK

The schema is where the choice becomes load-bearing. In our wallet service:

```sql
CREATE TABLE wallets (
    id          TEXT PRIMARY KEY,
    balance     BIGINT      NOT NULL CHECK (balance >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE transfers (
    id              UUID        PRIMARY KEY,
    from_wallet_id  TEXT        NOT NULL REFERENCES wallets(id),
    to_wallet_id    TEXT        NOT NULL REFERENCES wallets(id),
    amount          BIGINT      NOT NULL CHECK (amount > 0),
    state           TEXT        NOT NULL CHECK (state IN ('PENDING','PROCESSED','FAILED')),
    failure_reason  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_wallet_id <> to_wallet_id)
);

CREATE TABLE ledger_entries (
    id           BIGSERIAL   PRIMARY KEY,
    transfer_id  UUID        NOT NULL REFERENCES transfers(id),
    wallet_id    TEXT        NOT NULL REFERENCES wallets(id),
    type         TEXT        NOT NULL CHECK (type IN ('DEBIT','CREDIT')),
    amount       BIGINT      NOT NULL CHECK (amount > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (transfer_id, type)
);
```

Three things to notice:

1. **`BIGINT NOT NULL` everywhere money lives.** Not `NUMERIC`, not `DOUBLE PRECISION`, not `MONEY` (the Postgres `MONEY` type is locale-dependent and stores fewer fractional digits than you probably want).

2. **`CHECK (balance >= 0)` on the wallet.** This is the defence-in-depth invariant: even if the service has a bug, even if a future contributor forgets to validate, the database will refuse to write a negative balance. The constraint is exact on `BIGINT` and meaningless on a float (because `1e-323 >= 0` is true).

3. **`CHECK (amount > 0)` on transfers and ledger entries.** Same defence: zero-amount transfers are nonsensical, negative-amount transfers are an accounting bug, and the database rejects both.

All three checks rely on integer comparison being exact. On a `DOUBLE PRECISION` column, the same checks pass for values that aren't real amounts of money.

## Service-layer enforcement

The Go code mirrors the schema. The request type uses `int64`:

```go
type CreateTransferRequest struct {
    IdempotencyKey string `json:"idempotencyKey"`
    FromWalletID   string `json:"fromWalletId"`
    ToWalletID     string `json:"toWalletId"`
    Amount         int64  `json:"amount"`
}
```

Validation runs before any database call:

```go
func (r CreateTransferRequest) Validate() error {
    if r.IdempotencyKey == "" {
        return domain.ErrInvalidIdempotencyKey
    }
    if r.FromWalletID == "" || r.ToWalletID == "" {
        return domain.ErrInvalidWalletID
    }
    if r.FromWalletID == r.ToWalletID {
        return domain.ErrSelfTransfer
    }
    if r.Amount <= 0 {
        return domain.ErrInvalidAmount
    }
    return nil
}
```

The `Amount <= 0` check is exact. There is no `Amount < epsilon` and no need for one. And because the Go type and the SQL type agree (`int64` ↔ `BIGINT`), the driver translation is a no-op — no string parsing, no precision negotiation, no locale conversions.

## Testing the boundary

The test that proves this choice was correct is the conservation invariant in our many-wallets stress test:

```go
// Sum every wallet's balance — must equal the original total
// regardless of how many transfers landed as PROCESSED vs FAILED.
// This is the conservation invariant.
var sum int64
for id := range seed {
    w, err := wsvc.Get(ctx, id)
    if err != nil {
        t.Fatalf("get %s: %v", id, err)
    }
    if w.Balance < 0 {
        t.Fatalf("wallet %s went negative: %d", id, w.Balance)
    }
    sum += w.Balance
}
if sum != totalInitial {
    t.Fatalf("conservation broken: sum=%d want %d (delta=%d)",
        sum, totalInitial, sum-totalInitial)
}
```

The test seeds 100 wallets with 1,000 each (total 100,000), runs 10,000 random transfers between random pairs across 50 goroutines, and at the end asserts `sum == totalInitial`. That assertion is exact integer equality. With `float64`, it would need an epsilon, and the epsilon would need to grow with N, and a growing epsilon means "the test cannot fail, only complain."

Our measured throughput on this test is 533 transfers per second. The conservation invariant holds across all 10,000 transfers, every run, exactly.

## The migration story for codebases that already shipped float

If you're reading this and your `amount` field is already `float64`, the migration is real work. Here's the shape of it, in order:

1. **Add a parallel `BIGINT` column.** `amount_minor BIGINT` on the same row. Backfill from `amount` using a documented rounding policy. Write a one-time reconciliation report that shows the difference between `SUM(amount * 100)` (the old data) and `SUM(amount_minor)` (the backfill) at the boundary — that's your historical drift.

2. **Dual-write.** Every code path that writes the old `amount` also writes the new `amount_minor`. Add an invariant test that they agree, modulo the rounding policy. Run this for at least one full reconciliation cycle (often a month).

3. **Switch reads.** Move analytics, dashboards, and customer-facing displays to read from `amount_minor`. Compare results with the old reports for a full cycle. Resolve discrepancies — they will exist — by trusting the new column.

4. **Stop writing the old column.** Mark `amount` deprecated in the schema, log warnings on every write, then drop the column in a later migration.

5. **Change the JSON contract.** This is the painful one. Every external consumer needs to be updated to read `amount_minor` and interpret the unit correctly. Version your API. Run both shapes in parallel until every consumer has migrated.

The whole sequence takes months. The lesson, again, is to make the right choice on day one — when "day one" is the first migration file and the type costs nothing to change.

## When `int64` is not enough

Three cases where the convention breaks down:

- **Sub-cent precision.** FX trading, micropayments below a satoshi-equivalent, certain commodity contracts. Use a fixed-point `int64` with a larger scale factor (10^6 or 10^9) and document the unit clearly, or reach for `decimal`.

- **Cross-currency arithmetic.** `100 USD + 50 EUR` is not 150 of anything. Wrap the integer in a `Money` type that carries a currency tag and refuses to add unlike currencies. The integer is still the right primitive; the wrapper prevents nonsense.

- **Interest calculations with daily compounding.** The arithmetic compounds the rounding policy. You need an explicit rounding decision at every step, which `decimal` makes clearer than int64 division. The right move here is a hybrid: `int64` for storage and transfer, `decimal` for the calculation that produces the new `int64` balance.

## The summary

- Default to `int64` minor units for any money field in a Go service.
- Default to `BIGINT NOT NULL` in the corresponding column.
- Add `CHECK (amount > 0)` and `CHECK (balance >= 0)` as schema-level defence.
- Validate `Amount <= 0` in the service layer — but trust the schema as the last line.
- Test the conservation invariant. With integers, the test is exact. With floats, it can't be.
- If you're already on floats, the migration is real and the answer is "five steps over six months" — not a deploy.

The whole point of the convention is to make the arithmetic boring, and boring is what you want when the arithmetic is your livelihood.

---

Source code: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)

Follow for the next post in this series on Go financial services — covering why we kept stdlib `net/http` + `database/sql` instead of reaching for a framework, and the four design choices that fall out of that.
