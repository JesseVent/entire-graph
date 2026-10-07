#!/bin/sh
# Walks `entire graph docs` through a throwaway repo: a body change, a removed
# function, an edited section that another doc links to, and then the doc fixes.
#
#   mise run build && sh scripts/demo-docs.sh [path/to/entire-graph]
set -eu

bin=${1:-./entire-graph}
bin=$(CDPATH= cd -- "$(dirname -- "$bin")" && pwd)/$(basename -- "$bin")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
repo=$work/shop
mkdir -p "$repo/billing" "$repo/docs"
cd "$repo"

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=demo GIT_AUTHOR_EMAIL=demo@example.com
export GIT_COMMITTER_NAME=demo GIT_COMMITTER_EMAIL=demo@example.com

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
docs() { printf '$ entire graph docs\n'; "$bin" docs --repo . --cache-dir "$work/cache" "$@"; }

cat >billing/invoice.go <<'EOF'
package billing

type Invoice struct {
	Lines []float64
}

func TotalWithTax(inv Invoice, rate float64) float64 {
	total := 0.0
	for _, line := range inv.Lines {
		total += line
	}
	return total * (1 + rate)
}

func ApplyDiscount(inv Invoice, pct float64) Invoice {
	for i := range inv.Lines {
		inv.Lines[i] *= 1 - pct
	}
	return inv
}
EOF
cat >README.md <<'EOF'
# Shop

## Billing

`billing.TotalWithTax` adds tax to the sum of an `Invoice`'s lines.
Rounding is described in [the design notes](docs/design.md#rounding).

## Discounts

Call `ApplyDiscount()` before computing the total.

## Install

Run go build.
EOF
cat >docs/design.md <<'EOF'
# Design

## Rounding

Totals are not rounded.

## Storage

Invoices are kept in memory.
EOF
cat >docs/tax-rates.md <<'EOF'
# Tax rates

The default rate is 10%. Totals are covered in [rounding](design.md#round).
EOF
git init -q -b main
git add -A
git commit -qm "initial"
# A second commit touching invoice.go and tax-rates.md together makes them a
# co-change pair (the graph needs two shared commits).
printf '\n// Rates are fractions: 0.1 is 10%%.\n' >>billing/invoice.go
printf '\nRates are fractions in code.\n' >>docs/tax-rates.md
git commit -qam "document rate format"

step "0. Starting out: audit the docs and add the trail runner"
mkdir -p .entire/runners # stands in for `entire runner setup`
printf '$ entire graph docs init\n'
"$bin" docs init --repo . --cache-dir "$work/cache"
git add .entire && git commit -qm "add the Doc staleness trail runner"

step "Which docs mention TotalWithTax? (the graph edge behind docs)"
printf '$ entire graph neighbors --symbol TotalWithTax --relation X-entire-graph:MENTIONS --direction in\n'
"$bin" neighbors --repo . --cache-dir "$work/cache" --symbol TotalWithTax \
	--relation X-entire-graph:MENTIONS --direction in --format text

step "1. Nothing changed yet"
docs

step "2. Change TotalWithTax to round to cents, and delete ApplyDiscount"
cat >billing/invoice.go <<'EOF'
package billing

import "math"

type Invoice struct {
	Lines []float64
}

func TotalWithTax(inv Invoice, rate float64) float64 {
	total := 0.0
	for _, line := range inv.Lines {
		total += line
	}
	return math.Round(total*(1+rate)*100) / 100
}

// Rates are fractions: 0.1 is 10%.
EOF
git diff --stat
docs

step "3. Update the Rounding section, which README links to"
cat >docs/design.md <<'EOF'
# Design

## Rounding

Totals round to the nearest cent.

## Storage

Invoices are kept in memory.
EOF
docs

step "4. Fix the README sections it listed and the tax-rates doc"
cat >README.md <<'EOF'
# Shop

## Billing

`billing.TotalWithTax` adds tax to the sum of an `Invoice`'s lines and rounds to the cent.
Rounding is described in [the design notes](docs/design.md#rounding).

## Discounts

Discounts were removed; apply them upstream.

## Install

Run go build.
EOF
printf 'Totals are rounded to the cent after tax.\n' >>docs/tax-rates.md
docs
