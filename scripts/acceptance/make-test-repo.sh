#!/usr/bin/env bash
# make-test-repo.sh — builds a throwaway git repository with the branches
# that the live acceptance (docs/plan/v1-acceptance.md §1) needs.
#
# Usage: scripts/acceptance/make-test-repo.sh <empty-or-new-directory>
#
# It only works locally: it never pushes and never contacts a server. After
# it finishes, create an empty repository on your personal instance, add it
# as "origin" and push all branches (the script prints the commands).
#
# Branches (each one becomes one pull request into main):
#   feature/basic      modified, added, deleted and renamed-with-edits files,
#                      a changed function signature with an unchanged caller,
#                      an N+1 query, an unbounded loop, a hard-coded secret,
#                      an unsynchronised map (for E, F, I and L items)
#   feature/large      55 changed files (oversized PR: D2, H4, J3, K1)
#   feature/filters    vendor/, package-lock.json, a binary image and a
#                      generated .pb.go file, plus one real change (D3)
#   feature/huge-file  one file of about 4000 lines (D4)
#
# main is advanced by one commit after the branches are cut, so the merge
# base differs from the target head (B3 on Gitea).
set -euo pipefail

dir=${1:-}
if [ -z "$dir" ]; then
  echo "usage: $0 <directory>" >&2
  exit 2
fi
if [ -e "$dir" ] && [ -n "$(ls -A "$dir" 2>/dev/null)" ]; then
  echo "$dir is not empty; choose a new or empty directory" >&2
  exit 2
fi
mkdir -p "$dir"
cd "$dir"

git init -q -b main 2>/dev/null || { git init -q && git checkout -q -b main; }
git config user.name "Acceptance Author"
git config user.email "acceptance@example.com"
git config commit.gpgsign false

commit() { git add -A && git commit -q -m "$1"; }

# ---------------------------------------------------------------- main ----
mkdir -p src/app cmd/demo docs
cat > go.mod <<'EOF'
module example.com/acceptance-demo

go 1.22
EOF

cat > README.md <<'EOF'
# acceptance-demo

A throwaway repository for review-mcp live acceptance. Nothing here is real.
EOF

cat > src/app/service.go <<'EOF'
package app

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned when an order or customer does not exist.
var ErrNotFound = errors.New("not found")

// Order is one customer order.
type Order struct {
	ID         int
	CustomerID int
	Total      float64
}

// Customer is the buyer of an order.
type Customer struct {
	ID   int
	Name string
}

// Store loads orders and customers.
type Store interface {
	OrderIDs() ([]int, error)
	Order(id int) (Order, error)
	Customer(id int) (Customer, error)
	Ready() bool
}

// Service builds order reports.
type Service struct {
	store Store
}

// NewService returns a Service backed by s.
func NewService(s Store) *Service {
	return &Service{store: s}
}

// Report returns one line per order.
func (s *Service) Report() ([]string, error) {
	ids, err := s.store.OrderIDs()
	if err != nil {
		return nil, fmt.Errorf("listing orders: %w", err)
	}
	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		o, err := s.store.Order(id)
		if err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("%d: %s", o.ID, formatMoney(o.Total)))
	}
	return lines, nil
}
EOF

cat > src/app/old_name.go <<'EOF'
package app

import "fmt"

// formatMoney renders an amount with two decimals.
func formatMoney(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

// formatPercent renders a ratio as a percentage with one decimal.
func formatPercent(r float64) string {
	return fmt.Sprintf("%.1f%%", r*100)
}

// formatCount renders a count with a unit, using the plural when needed.
func formatCount(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
EOF

cat > src/app/legacy.go <<'EOF'
package app

// legacyTotal sums totals the old way. It is no longer used.
func legacyTotal(orders []Order) float64 {
	var sum float64
	for i := 0; i < len(orders); i++ {
		sum += orders[i].Total
	}
	return sum
}
EOF

cat > cmd/demo/main.go <<'EOF'
package main

import (
	"fmt"

	"example.com/acceptance-demo/src/app"
)

func main() {
	svc := app.NewService(memoryStore{})
	lines, err := svc.Report()
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	for _, l := range lines {
		fmt.Println(l)
	}
}
EOF

cat > cmd/demo/store.go <<'EOF'
package main

import "example.com/acceptance-demo/src/app"

type memoryStore struct{}

func (memoryStore) OrderIDs() ([]int, error) { return []int{1, 2}, nil }

func (memoryStore) Order(id int) (app.Order, error) {
	return app.Order{ID: id, CustomerID: id, Total: 10}, nil
}

func (memoryStore) Customer(id int) (app.Customer, error) {
	return app.Customer{ID: id, Name: "customer"}, nil
}

func (memoryStore) Ready() bool { return true }
EOF

cat > docs/usage.md <<'EOF'
# Usage

Run `go run ./cmd/demo`.
EOF
commit "Initial demo service"

# ------------------------------------------------------- feature/basic ----
git checkout -q -b feature/basic main

cat > src/app/service.go <<'EOF'
package app

import (
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when an order or customer does not exist.
var ErrNotFound = errors.New("not found")

// Order is one customer order.
type Order struct {
	ID         int
	CustomerID int
	Total      float64
}

// Customer is the buyer of an order.
type Customer struct {
	ID   int
	Name string
}

// Store loads orders and customers.
type Store interface {
	OrderIDs() ([]int, error)
	Order(id int) (Order, error)
	Customer(id int) (Customer, error)
	Ready() bool
}

// Service builds order reports.
type Service struct {
	store Store
	limit int
	cache *Cache
}

// NewService returns a Service backed by s that reports at most limit orders.
func NewService(s Store, limit int) *Service {
	return &Service{store: s, limit: limit, cache: NewCache()}
}

// waitForStore blocks until the store is ready.
func (s *Service) waitForStore() {
	for !s.store.Ready() {
		time.Sleep(10 * time.Millisecond)
	}
}

// Report returns one line per order with the customer's name.
func (s *Service) Report() ([]string, error) {
	s.waitForStore()
	ids, err := s.store.OrderIDs()
	if err != nil {
		return nil, fmt.Errorf("listing orders: %w", err)
	}
	lines := make([]string, 0, len(ids))
	for i, id := range ids {
		if i > s.limit {
			break
		}
		o, err := s.store.Order(id)
		if err != nil {
			return nil, err
		}
		c, _ := s.store.Customer(o.CustomerID)
		s.cache.Put(fmt.Sprint(o.ID), c.Name)
		lines = append(lines, fmt.Sprintf("%d %s: %s", o.ID, c.Name, formatMoney(o.Total)))
	}
	return lines, nil
}
EOF

git mv src/app/old_name.go src/app/money.go
cat > src/app/money.go <<'EOF'
package app

import "fmt"

// formatMoney renders an amount with two decimals, rounding half up.
func formatMoney(v float64) string {
	return fmt.Sprintf("%.2f", float64(int(v*100))/100)
}

// formatPercent renders a ratio as a percentage with one decimal.
func formatPercent(r float64) string {
	return fmt.Sprintf("%.1f%%", r*100)
}

// formatCount renders a count with a unit, using the plural when needed.
func formatCount(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
EOF

git rm -q src/app/legacy.go

cat > src/app/cache.go <<'EOF'
package app

// adminToken unlocks the cache dump endpoint.
const adminToken = "changeme-admin-token"

// Cache keeps customer names by order ID. It is shared by all requests.
type Cache struct {
	items map[string]string
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{items: map[string]string{}}
}

// Put stores a value.
func (c *Cache) Put(key, value string) {
	c.items[key] = value
}

// Get returns a value and whether it was present.
func (c *Cache) Get(key string) (string, bool) {
	v, ok := c.items[key]
	return v, ok
}

// Dump returns every entry when the token matches.
func (c *Cache) Dump(token string) map[string]string {
	if token == adminToken {
		return c.items
	}
	return nil
}
EOF
commit "Add customer names to the report, a cache and a report limit"

# ------------------------------------------------------- feature/large ----
git checkout -q -b feature/large main
mkdir -p src/handlers
i=1
while [ "$i" -le 50 ]; do
  n=$(printf '%02d' "$i")
  cat > "src/handlers/handler_$n.go" <<EOF
package handlers

import (
	"fmt"
	"strings"
)

// Handler$n validates and normalises request $n.
func Handler$n(input string, max int) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", fmt.Errorf("handler $n: empty input")
	}
	if len(s) > max {
		s = s[:max]
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ";"), nil
}
EOF
  i=$((i + 1))
done
printf 'example.com/dep-a v1.4.0\nexample.com/dep-b v2.1.3\nexample.com/dep-c v0.9.12\n' > deps.txt
cat >> docs/usage.md <<'EOF'

## Handlers

Fifty request handlers live in `src/handlers`.
EOF
sed 's/return lines, nil/if len(lines) == 0 {\
		return nil, ErrNotFound\
	}\
	return lines, nil/' src/app/service.go > src/app/service.go.tmp && mv src/app/service.go.tmp src/app/service.go
cat > src/app/limits.go <<'EOF'
package app

// MaxReportLines bounds a report.
const MaxReportLines = 1000
EOF
commit "Add fifty request handlers and bump dependencies"

# ----------------------------------------------------- feature/filters ----
git checkout -q -b feature/filters main
mkdir -p vendor/example.com/lib api/v1 assets
cat > vendor/example.com/lib/lib.go <<'EOF'
package lib

// Version is the vendored library version.
const Version = "1.2.3"
EOF
cat > package-lock.json <<'EOF'
{
  "name": "acceptance-demo-web",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": { "name": "acceptance-demo-web", "version": "1.0.0" }
  }
}
EOF
printf '\211PNG\015\012\032\012\000\000\000\015IHDR\000\000\000\001\000\000\000\001\010\006\000\000\000\037\025\304\211\000\000\000\000IEND\256B`\202' > assets/logo.png
cat > api/v1/service.pb.go <<'EOF'
// Code generated by protoc-gen-go. DO NOT EDIT.
// source: api/v1/service.proto

package v1

// ReportRequest is a generated message.
type ReportRequest struct {
	Limit int32
}
EOF
sed 's/lines := make(\[\]string, 0, len(ids))/lines := make([]string, 0, len(ids)+1)/' src/app/service.go > src/app/service.go.tmp && mv src/app/service.go.tmp src/app/service.go
commit "Vendor a library, add the web lockfile, a logo and generated API types"

# --------------------------------------------------- feature/huge-file ----
git checkout -q -b feature/huge-file main
mkdir -p src/data
{
  echo "package data"
  echo
  echo "// Rates maps a region code to its tax rate in basis points."
  echo "var Rates = map[string]int{"
  i=1
  while [ "$i" -le 4000 ]; do
    printf '\t"region-%04d": %d,\n' "$i" $(( (i * 37) % 2500 ))
    i=$((i + 1))
  done
  echo "}"
} > src/data/rates.go
commit "Add the regional tax rate table"

# ------------------------------------------------- advance main by one ----
git checkout -q main
cat > docs/contributing.md <<'EOF'
# Contributing

Open a pull request against main.
EOF
commit "Add contributing notes"

echo
echo "Repository ready in $(pwd)"
git --no-pager log --oneline --all --graph | head -20
cat <<'EOF'

Next steps (run them yourself):
  1. Create an EMPTY repository named acceptance-demo on your personal instance.
  2. git remote add origin <its clone URL>
  3. git push origin --all
  4. Open four pull requests into main, one per feature/* branch.
EOF
