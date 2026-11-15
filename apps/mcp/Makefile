# Home OS MCP server: developer targets. CI runs `go test ./...` from the
# repo root (see .github/workflows/pr-checks.yml); these are extras.

BENCH_N ?= 50
BENCH_P95_MS ?= 100
BENCH_REPORT ?= $(CURDIR)/bench-report.md

.PHONY: test bench

test:
	go test ./...

# MCP latency benchmark (#4386): every tool over the real authed /mcp
# handler against a seeded busy household (needs Docker). Fails when a tool's
# p95 exceeds BENCH_P95_MS; writes a markdown report to BENCH_REPORT.
bench:
	BENCH=1 BENCH_N=$(BENCH_N) BENCH_P95_MS=$(BENCH_P95_MS) BENCH_REPORT=$(BENCH_REPORT) \
		go test ./cmd/mcp -run '^TestMCPLatency$$' -count=1 -v -timeout 20m
