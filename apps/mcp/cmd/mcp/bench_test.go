package main

// MCP latency benchmark (#4386).
//
// TestMCPLatency calls every tool in wantTools through the production HTTP
// handler (Origin check, Bearer auth with a signed OIDC token and the
// membership lookup, the stateless Streamable HTTP transport) against a
// household seeded with a year of a busy family's data, next to three equally
// busy neighbour households, and reports p50/p95 per tool. It fails when a
// tool's p95 exceeds the budget.
//
// It also reports the tools/list payload with and without outputSchema, and
// traces every SQL statement each tool runs: per-tool query counts, and
// EXPLAIN (ANALYZE, BUFFERS) for the slowest statements, the input for index
// work.
//
// It is skipped unless BENCH=1:
//
//	BENCH=1 go test ./cmd/mcp -run TestMCPLatency -count=1 -v
//	make -C apps/mcp bench
//
// Knobs: BENCH_N calls per tool (default 50), BENCH_P95_MS budget (default
// 100), BENCH_EXPLAIN_MS explain statements slower than this (default 2),
// BENCH_REPORT write the markdown report to this path.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

const benchWarmup = 2

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// benchCase is one tool and how to build the arguments of its i-th call.
type benchCase struct {
	tool string
	args func(i int) map[string]any
}

func benchCases(f benchFixtures, today time.Time) []benchCase {
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }
	month := today.Format("2006-01")
	at := func(i int) string {
		return today.AddDate(0, 1, i%28).Add(15 * time.Hour).Format(time.RFC3339)
	}
	pick := func(ids []string, i int) string { return ids[i] }
	static := func(m map[string]any) func(int) map[string]any { return func(int) map[string]any { return m } }

	return []benchCase{
		{"agenda_get", static(map[string]any{"from": day(0), "to": day(30)})},
		{"asset_create", func(i int) map[string]any {
			return map[string]any{"name": fmt.Sprintf("Bench blender %d", i), "category": "appliance", "property_id": f.Property, "purchase_price": 49.99}
		}},
		{"asset_delete", func(i int) map[string]any { return map[string]any{"asset_id": pick(f.DisposableAssets, i)} }},
		{"asset_find", static(map[string]any{"query": "furnace"})},
		{"asset_update", func(i int) map[string]any { return map[string]any{"asset_id": f.Asset, "current_value": 100 + i} }},
		{"bill_create", func(i int) map[string]any {
			return map[string]any{"name": fmt.Sprintf("Bench bill %d", i), "amount": 12.5, "due_day": 1 + i%28}
		}},
		{"bill_delete", func(i int) map[string]any { return map[string]any{"bill_id": pick(f.DisposableBills, i)} }},
		{"bill_find", static(map[string]any{"due_within_days": 14})},
		{"bill_pay", func(i int) map[string]any { return map[string]any{"bill_id": pick(f.PayableBills, i)} }},
		{"bill_update", func(i int) map[string]any { return map[string]any{"bill_id": f.Bill, "amount": 100 + i} }},
		{"budget_summary", static(map[string]any{"month": month})},
		{"calendar_create", func(i int) map[string]any { return map[string]any{"name": fmt.Sprintf("Bench calendar %d", i)} }},
		{"calendar_find", static(map[string]any{})},
		{"calendar_find_free_time", static(map[string]any{"from": day(1), "to": day(7), "duration_minutes": 120})},
		{"contact_find", static(map[string]any{"trade": "plumbing"})},
		{"document_find", static(map[string]any{"query": "insurance"})},
		{"document_get", static(map[string]any{"document_id": f.Document})},
		{"document_upload", func(i int) map[string]any {
			return map[string]any{"name": fmt.Sprintf("bench-%d.txt", i), "text": "Bench uploaded document " + strconv.Itoa(i), "category": "receipt"}
		}},
		{"event_create", func(i int) map[string]any {
			s := at(i)
			e, _ := time.Parse(time.RFC3339, s)
			return map[string]any{"title": fmt.Sprintf("Bench event %d", i), "start": s, "end": e.Add(time.Hour).Format(time.RFC3339), "calendar_id": f.Calendar}
		}},
		{"event_delete", func(i int) map[string]any { return map[string]any{"event_id": pick(f.DisposableEvents, i)} }},
		{"event_find", static(map[string]any{"from": day(0), "to": day(30)})},
		{"event_update", func(i int) map[string]any {
			return map[string]any{"event_id": f.Event, "location": fmt.Sprintf("Room %d", i)}
		}},
		{"family_overview", static(map[string]any{})},
		{"family_update", func(i int) map[string]any { return map[string]any{"name": fmt.Sprintf("The Bench Family %d", i)} }},
		{"loan_create", func(i int) map[string]any {
			return map[string]any{"name": fmt.Sprintf("Bench loan %d", i), "original_amount": 1000, "remaining_balance": 500}
		}},
		{"loan_delete", func(i int) map[string]any { return map[string]any{"loan_id": pick(f.DisposableLoans, i)} }},
		{"loan_find", static(map[string]any{})},
		{"loan_update", func(i int) map[string]any { return map[string]any{"loan_id": f.Loan, "remaining_balance": 240000 - i} }},
		{"maintenance_complete", func(i int) map[string]any {
			return map[string]any{"task_id": pick(f.PendingTasks, i), "cost": 25}
		}},
		{"maintenance_create", func(i int) map[string]any {
			return map[string]any{"name": fmt.Sprintf("Bench chore %d", i), "due_date": day(10 + i%30), "property_id": f.Property}
		}},
		{"maintenance_delete", func(i int) map[string]any { return map[string]any{"task_id": pick(f.DisposableTasks, i)} }},
		{"maintenance_find", static(map[string]any{"due_within_days": 30})},
		{"maintenance_update", func(i int) map[string]any {
			return map[string]any{"task_id": f.Task, "description": fmt.Sprintf("Updated %d", i)}
		}},
		{"note_create", func(i int) map[string]any {
			return map[string]any{"title": fmt.Sprintf("Bench note %d", i), "content": "Remember the bench."}
		}},
		{"note_delete", func(i int) map[string]any { return map[string]any{"note_id": pick(f.DisposableNotes, i)} }},
		{"note_find", static(map[string]any{"query": "passports"})},
		{"note_get", static(map[string]any{"note_id": f.Note})},
		{"note_update", func(i int) map[string]any {
			return map[string]any{"note_id": f.Note, "append_text": fmt.Sprintf("line %d", i)}
		}},
		{"notification_find", static(map[string]any{"unread_only": true})},
		{"notification_mark_read", func(i int) map[string]any {
			return map[string]any{"notification_ids": []string{pick(f.UnreadNotifications, i)}}
		}},
		{"person_find", static(map[string]any{})},
		{"pet_find", static(map[string]any{})},
		{"property_find", static(map[string]any{})},
		{"search_all", static(map[string]any{"query": "insurance"})},
		{"vehicle_find", static(map[string]any{})},
	}
}

func TestMCPLatency(t *testing.T) {
	if os.Getenv("BENCH") != "1" {
		t.Skip("MCP latency benchmark: set BENCH=1 to run")
	}
	n := envInt("BENCH_N", 50)
	budget := time.Duration(envInt("BENCH_P95_MS", 100)) * time.Millisecond
	explainOver := time.Duration(envInt("BENCH_EXPLAIN_MS", 2)) * time.Millisecond
	calls := 1 + benchWarmup + n // global warm-up pass, per-tool warm-up, timed

	// Seed: the measured household plus three equally busy neighbours, so
	// household_id filters are as selective as in a shared instance.
	pool := testutil.NewPool(t)
	seedStart := time.Now()
	_, hh := testutil.SeedHousehold(t, pool)
	seedBusyFamily(t, pool, hh.HouseholdID, hh.UserID, calls)
	for range 3 {
		_, other := testutil.SeedHousehold(t, pool)
		seedBusyFamily(t, pool, other.HouseholdID, other.UserID, calls)
	}
	if _, err := pool.Exec(context.Background(), `ANALYZE`); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	fx := loadFixtures(t, pool, hh.HouseholdID)
	for name, ids := range map[string][]string{
		"disposable assets": fx.DisposableAssets, "disposable bills": fx.DisposableBills,
		"disposable loans": fx.DisposableLoans, "disposable tasks": fx.DisposableTasks,
		"disposable notes": fx.DisposableNotes, "disposable events": fx.DisposableEvents,
		"payable bills": fx.PayableBills, "pending tasks": fx.PendingTasks, "unread notifications": fx.UnreadNotifications,
	} {
		if len(ids) < calls {
			t.Fatalf("seed has %d %s, need %d (lower BENCH_N)", len(ids), name, calls)
		}
	}
	counts := seedCounts(t, pool, hh.HouseholdID)
	t.Logf("seeded 4 households in %v; measured household: %s", time.Since(seedStart).Round(time.Millisecond), counts)

	// The server runs on a traced pool so every statement is attributed to
	// the tool being called.
	tr := &queryTracer{stats: map[queryKey]*queryStat{}}
	pcfg := pool.Config()
	pcfg.ConnConfig.Tracer = tr
	traced, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		t.Fatalf("traced pool: %v", err)
	}
	t.Cleanup(traced.Close)

	cfg := &config.Config{
		PublicBaseURL:     "https://mcp.test",
		ResourceURL:       "https://mcp.test/mcp",
		DexIssuer:         testutil.TestIssuer,
		AcceptedAudiences: []string{testutil.TestAudience},
		AllowedOrigins:    []string{"https://mcp.test"},
	}
	iss := testutil.NewTokenIssuer(t)
	srv := server.NewServer(cfg, traced, iss.Verifier(traced))
	registerAll(srv, cfg, traced)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	c := &benchClient{t: t, url: ts.URL + "/mcp", token: iss.Token("bench-owner", hh.Email)}

	var initRes json.RawMessage
	c.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "bench", "version": "0.0.1"},
	}, &initRes)

	report := &strings.Builder{}
	fmt.Fprintf(report, "# MCP latency benchmark\n\n%s, %d timed calls per tool after a warm-up pass and %d warm-up calls each, budget p95 < %v.\n\nMeasured household: %s, plus 3 neighbour households of the same size.\n\n",
		time.Now().Format("2006-01-02"), n, benchWarmup, budget, counts)

	toolsListReport(t, c, report)

	// Location of "today" for date arguments: the household's timezone.
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		loc = time.UTC
	}
	today := time.Now().In(loc)
	cases := benchCases(fx, today)
	var names []string
	for _, bc := range cases {
		names = append(names, bc.tool)
	}
	if !slices.Equal(names, wantTools) {
		t.Fatalf("benchmark covers %v, want exactly wantTools %v", names, wantTools)
	}

	type row struct {
		tool               string
		p50, p95, max      time.Duration
		textBytes, queries int
	}
	// One untimed pass over every tool first, so the pool's connections,
	// prepared statements and Postgres caches are warm before any tool is
	// timed (otherwise whichever tool runs first pays for it). Each tool then
	// gets benchWarmup more untimed calls before n timed ones. Untimed calls
	// are not traced.
	for _, bc := range cases {
		c.callTool(bc.tool, bc.args(0))
	}
	var rows []row
	for _, bc := range cases {
		var took []time.Duration
		var textBytes int
		tr.queries.Store(0)
		for i := 1; i < calls; i++ {
			measured := i > benchWarmup
			if measured {
				tr.setTool(bc.tool)
			}
			start := time.Now()
			text := c.callTool(bc.tool, bc.args(i))
			d := time.Since(start)
			tr.setTool("")
			if measured {
				took = append(took, d)
				textBytes = len(text)
			}
		}
		sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
		rows = append(rows, row{
			tool: bc.tool, p50: percentile(took, 50), p95: percentile(took, 95), max: took[len(took)-1],
			textBytes: textBytes, queries: int(tr.queries.Load()) / n,
		})
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].p95 > rows[j].p95 })
	fmt.Fprintf(report, "## Per tool (slowest p95 first)\n\nAll times are full HTTP round trips including token verification and the membership lookup. text = bytes of content[0].text the model reads (~4 bytes/token); queries = SQL statements per call, auth included.\n\n")
	fmt.Fprintf(report, "| tool | p50 | p95 | max | text bytes | ~tokens | queries |\n|---|---:|---:|---:|---:|---:|---:|\n")
	var over []string
	for _, r := range rows {
		mark := ""
		if r.p95 > budget {
			mark = " **over budget**"
			over = append(over, fmt.Sprintf("%s p95 %v", r.tool, r.p95))
		}
		fmt.Fprintf(report, "| %s%s | %s | %s | %s | %d | %d | %d |\n", r.tool, mark, ms(r.p50), ms(r.p95), ms(r.max), r.textBytes, r.textBytes/4, r.queries)
	}

	explainReport(t, pool, tr, explainOver, report)

	out := report.String()
	t.Log("\n" + out)
	if path := os.Getenv("BENCH_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Errorf("write report: %v", err)
		} else {
			t.Logf("report written to %s", path)
		}
	}
	if len(over) > 0 {
		t.Errorf("tools over the %v p95 budget: %s", budget, strings.Join(over, ", "))
	}
}

func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	// Nearest-rank.
	idx := (p*len(sorted)+99)/100 - 1
	return sorted[max(0, min(idx, len(sorted)-1))]
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond)) }

func seedCounts(t *testing.T, pool *pgxpool.Pool, hh any) string {
	t.Helper()
	var ev, bills, tasks, notes, docs, assets, notifs int
	if err := pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id WHERE c.household_id = $1),
		(SELECT count(*) FROM bills WHERE household_id = $1),
		(SELECT count(*) FROM maintenance_tasks WHERE household_id = $1),
		(SELECT count(*) FROM notes WHERE household_id = $1),
		(SELECT count(*) FROM files WHERE household_id = $1),
		(SELECT count(*) FROM assets WHERE household_id = $1),
		(SELECT count(*) FROM notifications WHERE household_id = $1)`, hh,
	).Scan(&ev, &bills, &tasks, &notes, &docs, &assets, &notifs); err != nil {
		t.Fatalf("counts: %v", err)
	}
	return fmt.Sprintf("%d events, %d bills, %d maintenance tasks, %d notes, %d documents, %d assets, %d notifications",
		ev, bills, tasks, notes, docs, assets, notifs)
}

// toolsListReport measures tools/list and what it would cost without
// outputSchema.
func toolsListReport(t *testing.T, c *benchClient, report *strings.Builder) {
	t.Helper()
	var took []time.Duration
	var raw json.RawMessage
	for i := range 10 {
		start := time.Now()
		c.call("tools/list", map[string]any{}, &raw)
		if i > 0 {
			took = append(took, time.Since(start))
		}
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })

	var list struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	var withSchema, withoutSchema, outputOnly int
	for _, tool := range list.Tools {
		b, _ := json.Marshal(tool)
		withSchema += len(b)
		outputOnly += len(tool["outputSchema"])
		delete(tool, "outputSchema")
		b, _ = json.Marshal(tool)
		withoutSchema += len(b)
	}
	fmt.Fprintf(report, "## tools/list\n\n%d tools; p50 %s, p95 %s.\n\n", len(list.Tools), ms(percentile(took, 50)), ms(percentile(took, 95)))
	fmt.Fprintf(report, "| payload | bytes | ~tokens (bytes/4) |\n|---|---:|---:|\n")
	fmt.Fprintf(report, "| full result | %d | %d |\n| tools with outputSchema | %d | %d |\n| tools without outputSchema (what moltis sends the model) | %d | %d |\n| outputSchema alone | %d | %d |\n\n",
		len(raw), len(raw)/4, withSchema, withSchema/4, withoutSchema, withoutSchema/4, outputOnly, outputOnly/4)
}

// explainReport runs EXPLAIN (ANALYZE, BUFFERS) on every distinct statement
// whose slowest traced run took at least over, with that run's arguments,
// inside a rolled-back transaction (so writes leave no trace).
func explainReport(t *testing.T, pool *pgxpool.Pool, tr *queryTracer, over time.Duration, report *strings.Builder) {
	t.Helper()
	tr.mu.Lock()
	var stats []*queryStat
	for _, s := range tr.stats {
		stats = append(stats, s)
	}
	tr.mu.Unlock()
	sort.Slice(stats, func(i, j int) bool { return stats[i].max > stats[j].max })

	fmt.Fprintf(report, "\n## Slowest statements (max >= %v)\n\nEXPLAIN (ANALYZE, BUFFERS) re-run with the slowest call's arguments. Seq scans on a table with a household_id filter are index candidates.\n\n", over)
	seqRE := regexp.MustCompile(`Seq Scan on (\w+)`)
	shown := 0
	for _, s := range stats {
		if s.max < over || shown >= 25 {
			break
		}
		shown++
		plan, err := explain(pool, s.sql, s.args)
		seq := map[string]bool{}
		for _, m := range seqRE.FindAllStringSubmatch(plan, -1) {
			seq[m[1]] = true
		}
		var seqList []string
		for k := range seq {
			seqList = append(seqList, k)
		}
		sort.Strings(seqList)
		fmt.Fprintf(report, "### %d. %s — max %s, avg %s over %d runs\n\nSeq scans: %s\n\n```sql\n%s\n```\n\n",
			shown, s.key.tool, ms(s.max), ms(s.total/time.Duration(s.count)), s.count, orNone(seqList), compactSQL(s.sql))
		if err != nil {
			fmt.Fprintf(report, "EXPLAIN failed: %v\n\n", err)
			continue
		}
		fmt.Fprintf(report, "```\n%s```\n\n", plan)
	}
	if shown == 0 {
		fmt.Fprintf(report, "None.\n")
	}
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

var spaceRE = regexp.MustCompile(`\s+`)

func compactSQL(sql string) string { return strings.TrimSpace(spaceRE.ReplaceAllString(sql, " ")) }

func explain(pool *pgxpool.Pool, sql string, args []any) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", err
		}
		b.WriteString(line + "\n")
	}
	return b.String(), rows.Err()
}

// queryTracer attributes every statement to the tool being called.
type queryTracer struct {
	tool    atomic.Pointer[string]
	queries atomic.Int64

	mu    sync.Mutex
	stats map[queryKey]*queryStat
}

type queryKey struct{ tool, sql string }

type queryStat struct {
	key        queryKey
	sql        string
	args       []any // arguments of the slowest run
	count      int
	total, max time.Duration
}

type traceStartKey struct{}

type traceStart struct {
	at   time.Time
	sql  string
	args []any
}

func (q *queryTracer) setTool(name string) { q.tool.Store(&name) }

func (q *queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceStartKey{}, traceStart{at: time.Now(), sql: data.SQL, args: data.Args})
}

func (q *queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	st, ok := ctx.Value(traceStartKey{}).(traceStart)
	if !ok {
		return
	}
	q.record(st.sql, st.args, time.Since(st.at))
}

// Batched statements carry no per-statement timing; they are counted.
func (q *queryTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

func (q *queryTracer) TraceBatchQuery(_ context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	q.record(data.SQL, data.Args, 0)
}

func (q *queryTracer) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

func (q *queryTracer) record(sql string, args []any, d time.Duration) {
	tool := ""
	if p := q.tool.Load(); p != nil {
		tool = *p
	}
	if tool == "" {
		return
	}
	q.queries.Add(1)
	key := queryKey{tool, sql}
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.stats[key]
	if s == nil {
		s = &queryStat{key: key, sql: sql}
		q.stats[key] = s
	}
	s.count++
	s.total += d
	if d >= s.max {
		s.max = d
		s.args = args
	}
}

// benchClient speaks JSON-RPC to /mcp with a Bearer token, as an MCP client
// on protocol 2025-06-18 (the one moltis uses) does.
type benchClient struct {
	t      *testing.T
	url    string
	token  string
	nextID int
	http   http.Client
}

func (c *benchClient) call(method string, params any, out any) {
	c.t.Helper()
	c.nextID++
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params})
	if err != nil {
		c.t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("%s: read body: %v", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("%s: status %d: %s", method, resp.StatusCode, body)
	}
	payload := body
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		if payload, err = lastSSEData(body); err != nil {
			c.t.Fatalf("%s: %v", method, err)
		}
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		c.t.Fatalf("%s: decode: %v", method, err)
	}
	if env.Error != nil {
		c.t.Fatalf("%s: JSON-RPC error %d: %s", method, env.Error.Code, env.Error.Message)
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		c.t.Fatalf("%s: decode result: %v", method, err)
	}
}

// callTool calls a tool and returns its text content; a tool error fails
// the benchmark, since timing a refusal measures nothing.
func (c *benchClient) callTool(name string, args map[string]any) string {
	c.t.Helper()
	var res struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	c.call("tools/call", map[string]any{"name": name, "arguments": args}, &res)
	var text strings.Builder
	for _, part := range res.Content {
		text.WriteString(part.Text)
	}
	if res.IsError {
		c.t.Fatalf("%s(%v) returned a tool error: %s", name, args, text.String())
	}
	return text.String()
}
