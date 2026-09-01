# Contributing to OpenRMM

Thanks for wanting to help. This page covers the two things you need before your
first pull request — the sign-off and the CLA — and then how to actually get the
thing running and tested.

## Before your first pull request

### 1. Sign off each commit (DCO)

Every commit must carry a `Signed-off-by` line, which is your statement that you
have the right to submit that code under the project's licence. `git commit -s`
adds it for you:

```
Signed-off-by: Your Name <your.email@example.com>
```

Use your real name and an address you can be reached at.

### 2. Sign the CLA (once)

OpenRMM is released under the AGPL-3.0 **and** offered to customers under
separate commercial terms. Holding both requires that one party hold the rights
to the whole codebase, which is what the [Contributor License Agreement](CLA.md)
establishes. You keep the copyright in your work; you grant a licence broad
enough to include relicensing.

Signing takes one pull request and covers everything you contribute afterwards —
[the instructions are at the end of the CLA](CLA.md#how-to-sign).

If the CLA is a problem for you, say so in an issue before writing code. It is a
reasonable thing to object to, and a conversation beforehand is cheaper than a
pull request nobody can merge.

## Getting it running

See [`docs/02-getting-started`](docs/02-getting-started.md). The short version:

```bash
docker compose up -d          # server, console and Postgres
```

## Running the tests

```bash
make test                     # Go
cd openrmm && npm test        # console
```

**That is not the whole suite.** Most of what verifies access scope, the approval
gates, migrations and the alert lifecycle is database-backed, and those tests
`t.Skip` without a database — so a green run without one proves much less than it
appears to:

```bash
export RMM_TEST_DATABASE_URL='postgres://rmm:rmm@localhost:5432/rmm_test?sslmode=disable'
make test
```

CI sets no `RMM_TEST_DATABASE_URL`, so integration tests skip there. Run them
locally before you trust a change to anything security-relevant.

New database-backed tests should take a schema of their own through
`storetest.NewSchema`, not truncate shared tables — a truncate reaches into
whatever another package is doing at the time.

## What tends to get a pull request merged

**Say why, not just what.** The commit message and the code comments should
explain the reasoning that is not visible in the diff. A comment that restates
the line below it costs a reader time; one that explains why the obvious approach
was wrong saves it.

**Cover the behaviour, not the function.** A test that passes whether or not the
guard exists is not covering the guard. If you are adding one, try breaking it on
purpose and confirm a test fails.

**Respect the guardrails.** The AI and MCP surfaces cannot execute anything: they
propose jobs that a human approves, and they create configuration disabled and
awaiting review. If a change touches those paths, read
[`docs/07-guardrails-security`](docs/07-guardrails-security.md) first and say in
the pull request how the change preserves them.

**Migrations are immutable.** Add a new numbered file; never edit one that has
shipped. Mirror the change into `db/schema.sql` — a test compares the two.

**Keep the docs true.** If a change makes a statement in `docs/` wrong, fix it in
the same pull request. A stale document is worse than a missing one, because
people act on it.

## Reporting a security issue

Please do **not** open a public issue — see [`SECURITY.md`](SECURITY.md) for how
to report one privately.
