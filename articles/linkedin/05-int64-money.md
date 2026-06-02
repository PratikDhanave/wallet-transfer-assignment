`amount += 0.1 + 0.2` is `0.30000000000000004` in every language with IEEE 754 floats.

Now ask yourself: what's your `amount` field?

If the answer involves `float64`, `double`, `Number`, `decimal` (the JavaScript one, not the Python one), or anything other than an integer or a true fixed-point decimal type — you have a bug. You may not have hit it yet. You will.

---

The float trap is famous and still ships in production every week. Two halves of a pound aren't two halves of a pound. They round, they accumulate, they reconcile to one penny too many, then ten, then a dispute, then a refund of money that never existed. The fun part is that the bug is invisible at small scale. A single transfer of £1.00 looks fine. A million transfers of £0.01 do not.

The fix is not subtle and not new. Store money as an integer count of the smallest unit your domain cares about. Pounds become pence. Dollars become cents. Bitcoin becomes satoshis. The Go type is `int64`. The column type in Postgres is `BIGINT`. No floats anywhere in the stack.

Why `int64` and not a decimal library? In Go specifically, there is no first-class decimal type. The community options (shopspring/decimal, ericlagergren/decimal) are real packages run by real people, but they're packages. They introduce arithmetic that does not look like arithmetic. `a.Add(b).Mul(rate).Round(2)` is correct and ugly. `(a + b) * rate / 100` rounds for free in `int64` because integer division truncates, and you control the rounding strategy explicitly at the boundary where it matters.

The bonus is that the database does the same arithmetic the same way. `balance + amount` in SQL is one BIGINT plus another BIGINT. No driver coercion. No string-to-decimal round-trip. No "the bank reads it as `1.79e10`, the analytics team reads it as `17900000000`, the support agent reads it as `£17,900,000.00`" Tower of Babel.

In our wallet service the schema enforces it at the column level:

  `amount BIGINT NOT NULL CHECK (amount > 0)`

That CHECK constraint is what makes the int64 decision load-bearing. The check is impossible to write correctly against a float column — `CHECK (amount > 0)` on a float passes for `1e-323`, which is not a real amount of money. On a BIGINT it rejects zero, rejects negative, and the rejection happens at the storage layer no matter which service forgets to validate.

Five places a `float64` would have bitten us:

1. The balance check before a debit. `if src.Balance < amount` is a sharp comparison on integers and a probabilistic one on floats. We have a stress test that fires 1,000 concurrent debits at a wallet seeded with exactly 10,000. Exactly 100 must succeed. With floats, "exactly 100" becomes "approximately 100, sometimes 99, occasionally 101 when the planets align."

2. The ledger invariant. We assert at the end of every stress run that the sum of all wallet balances equals the sum of the initial balances. With integers, that equality is exact: 100 wallets × 1,000 initial balance = exactly 100,000 forever. With floats, the equality is a comparison-with-epsilon and the test would be flaky.

3. JSON. Go encodes `float64` to JSON in scientific notation past a certain magnitude. Your front-end displays `1.79e10` to a confused user. `int64` round-trips as a literal number every time. (Unless you're feeding a JavaScript client, in which case you encode as a string and the client parses with BigInt — but that's a different post.)

4. Postgres CHECK constraints. The `balance >= 0` invariant is enforced by the database, not the application, exactly because the application can be wrong. On a NUMERIC or BIGINT column, the constraint is exact. On a DOUBLE PRECISION column, you're hoping the rounding stays below your monitoring threshold.

5. Aggregations. `SUM(amount)` over a million rows of BIGINT is exact. `SUM(amount)` over a million rows of DOUBLE PRECISION is in the neighbourhood. Reconciliation reports built on the latter quietly disagree by pennies, which becomes pounds, which becomes the worst kind of bug — the one nobody reports because it looks like a rounding error in the dashboard.

If you're greenfield, choose int64. If you've already shipped float, the migration is a one-way door — schema, JSON contract, downstream consumers all have to move together. That migration is a separate project. Plan it before the first dispute hits your support queue.

What's the worst float bug you've ever shipped — or watched ship?
