package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/technobasant/dataset-registry/api/internal/auth"
	"github.com/technobasant/dataset-registry/api/internal/compat"
	"github.com/technobasant/dataset-registry/api/internal/freshness"
)

func adminURLFlag(fs *flag.FlagSet) *string {
	return fs.String("admin-url", envOr("DR_BENCH_ADMIN_URL", "postgres://registry:registry@127.0.0.1:55433/registry?sslmode=disable"),
		"PostgreSQL URL with rights to create databases; each run gets a fresh one")
}

func ints(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

func environment() map[string]any {
	return map[string]any{
		"go": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH, "cpus": runtime.NumCPU(),
		"at": time.Now().UTC().Format(time.RFC3339),
	}
}

// ---- compatibility checker ----

type confusion struct {
	TP, FP, TN, FN int
}

func (c *confusion) add(label, got bool) {
	switch {
	case label && got:
		c.TP++
	case !label && got:
		c.FP++
	case !label && !got:
		c.TN++
	default:
		c.FN++
	}
}

func (c confusion) accuracy() float64 {
	return float64(c.TP+c.TN) / float64(c.TP+c.TN+c.FP+c.FN)
}

func benchCompat(args []string) error {
	fs := flag.NewFlagSet("compat", flag.ExitOnError)
	out := fs.String("out", "", "write results JSON here")
	_ = fs.Parse(args)
	cases, err := compat.LabelledCases()
	if err != nil {
		return err
	}
	modes := []compat.Mode{compat.Backward, compat.Forward, compat.Full}
	matrices := map[compat.Mode]*confusion{}
	for _, m := range modes {
		matrices[m] = &confusion{}
	}
	oracleAgrees, probes := 0, 0
	var misses []map[string]any
	var took []float64
	for _, c := range cases {
		start := time.Now()
		r, err := compat.Compare(c.Old, c.New, compat.Full)
		took = append(took, float64(time.Since(start).Microseconds()))
		if err != nil {
			return err
		}
		o, err := compat.Oracle(c.Old, c.New)
		if err != nil {
			return err
		}
		probes += o.Probes
		if (o.BreaksBackward != nil) == c.BreaksBackward() && (o.BreaksForward != nil) == c.BreaksForward() {
			oracleAgrees++
		}
		for _, m := range modes {
			var label, got bool
			switch m {
			case compat.Backward:
				label, got = c.BreaksBackward(), r.BreaksBackward
			case compat.Forward:
				label, got = c.BreaksForward(), r.BreaksForward
			default:
				label, got = c.BreaksBackward() || c.BreaksForward(), r.BreaksBackward || r.BreaksForward
			}
			matrices[m].add(label, got)
			if label != got {
				misses = append(misses, map[string]any{"case": c.Name, "mode": m, "label_breaking": label, "checker_breaking": got})
			}
		}
	}
	res := map[string]any{"environment": environment(), "cases": len(cases), "oracle_agrees_with_labels": oracleAgrees,
		"oracle_probes_total": probes, "compare_us_p50": median(took), "compare_us_p95": percentile(took, 95), "misses": misses}
	fmt.Printf("%d labelled cases; oracle agrees with %d/%d labels (%d probe documents)\n", len(cases), oracleAgrees, len(cases), probes)
	fmt.Println("| mode | TP | FP | TN | FN | accuracy |")
	fmt.Println("|---|---|---|---|---|---|")
	per := map[string]any{}
	for _, m := range modes {
		c := matrices[m]
		fmt.Printf("| %s | %d | %d | %d | %d | %.1f%% |\n", m, c.TP, c.FP, c.TN, c.FN, 100*c.accuracy())
		per[string(m)] = map[string]any{"tp": c.TP, "fp": c.FP, "tn": c.TN, "fn": c.FN, "accuracy": c.accuracy()}
	}
	for _, m := range misses {
		fmt.Printf("miss: %v\n", m)
	}
	fmt.Printf("compare: p50 %.0f µs, p95 %.0f µs\n", median(took), percentile(took, 95))
	res["by_mode"] = per
	return writeJSON(*out, res)
}

// ---- OpenLineage ingest ----

func benchIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	admin := adminURLFlag(fs)
	n := fs.Int("events", 20000, "events per run")
	conc := fs.String("concurrency", "1,8,32", "client concurrency levels")
	runs := fs.Int("runs", 3, "runs per level")
	datasets := fs.Int("datasets", 2000, "distinct datasets the events touch")
	out := fs.String("out", "", "write results JSON here")
	_ = fs.Parse(args)
	ctx := context.Background()

	type runResult struct {
		EventsPerSec float64 `json:"events_per_sec"`
		P50ms        float64 `json:"p50_ms"`
		P95ms        float64 `json:"p95_ms"`
		P99ms        float64 `json:"p99_ms"`
		Errors       int64   `json:"errors"`
	}
	results := map[string]any{"environment": environment(), "events_per_run": *n, "datasets": *datasets}
	var levels []map[string]any
	for _, c := range ints(*conc) {
		var rs []runResult
		for run := 0; run < *runs; run++ {
			st, err := newBenchStack(ctx, *admin, "bench_ingest", 32)
			if err != nil {
				return err
			}
			rng := rand.New(rand.NewPCG(uint64(run), 7))
			events := make([]map[string]any, *n+500)
			for i := range events {
				out := rng.IntN(*datasets)
				in1, in2 := rng.IntN(*datasets), rng.IntN(*datasets)
				typ := "START"
				if i%2 == 1 {
					typ = "COMPLETE"
				}
				events[i] = runEvent(typ, time.Now().Add(-time.Duration(*n-i)*time.Second), uuid.New(), producerSpark,
					nsSpark, fmt.Sprintf("job_%d", out%500), "SPARK",
					[]dsRef{{"bench://lake", fmt.Sprintf("d%d", in1)}, {"bench://lake", fmt.Sprintf("d%d", in2)}},
					[]dsRef{{"bench://lake", fmt.Sprintf("d%d", out)}})
			}
			tok := st.client.as("bench", auth.Owner, "pipelines")
			// Warm up connections and caches; not measured.
			for _, ev := range events[:500] {
				if err := st.client.do(tok, http.MethodPost, "/api/v1/lineage", ev, nil); err != nil {
					return err
				}
			}
			lat := make([]float64, *n)
			var next, errs atomic.Int64
			var wg sync.WaitGroup
			start := time.Now()
			for w := 0; w < c; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						i := int(next.Add(1)) - 1
						if i >= *n {
							return
						}
						t := time.Now()
						if err := st.client.do(tok, http.MethodPost, "/api/v1/lineage", events[500+i], nil); err != nil {
							errs.Add(1)
						}
						lat[i] = ms(time.Since(t))
					}
				}()
			}
			wg.Wait()
			wall := time.Since(start)
			r := runResult{EventsPerSec: float64(*n) / wall.Seconds(), P50ms: median(lat), P95ms: percentile(lat, 95), P99ms: percentile(lat, 99), Errors: errs.Load()}
			fmt.Printf("concurrency %d run %d: %.0f events/s, p50 %.2f ms, p95 %.2f ms, p99 %.2f ms, errors %d\n",
				c, run+1, r.EventsPerSec, r.P50ms, r.P95ms, r.P99ms, r.Errors)
			rs = append(rs, r)
			st.Close()
		}
		var eps, p50, p95, p99 []float64
		for _, r := range rs {
			eps, p50, p95, p99 = append(eps, r.EventsPerSec), append(p50, r.P50ms), append(p95, r.P95ms), append(p99, r.P99ms)
		}
		levels = append(levels, map[string]any{"concurrency": c, "runs": rs,
			"median": map[string]float64{"events_per_sec": median(eps), "p50_ms": median(p50), "p95_ms": median(p95), "p99_ms": median(p99)}})
		fmt.Printf("concurrency %d median of %d: %.0f events/s, p50 %.2f ms, p95 %.2f ms\n", c, *runs, median(eps), median(p50), median(p95))
	}
	results["levels"] = levels
	return writeJSON(*out, results)
}

// ---- lineage queries ----

// layeredDAG builds n datasets in `layers` layers. Each dataset after the
// first layer is written by its own job, which reads `fanIn` datasets from the
// layer before. Layer-0 datasets are written by source jobs with no inputs.
func layeredDAG(n, layers, fanIn int, seed uint64) [][]dsRef {
	rng := rand.New(rand.NewPCG(seed, 11))
	width := n / layers
	ref := func(l, i int) dsRef { return dsRef{"bench://lake", fmt.Sprintf("l%02d.d%05d", l, i)} }
	var jobs [][]dsRef // each: inputs..., output last
	for l := 0; l < layers; l++ {
		for i := 0; i < width; i++ {
			var ins []dsRef
			if l > 0 {
				seen := map[int]bool{}
				for len(ins) < fanIn && len(ins) < width {
					j := rng.IntN(width)
					if !seen[j] {
						seen[j] = true
						ins = append(ins, ref(l-1, j))
					}
				}
			}
			jobs = append(jobs, append(ins, ref(l, i)))
		}
	}
	return jobs
}

func ingestDAG(st *benchStack, dag [][]dsRef, workers int) error {
	tok := st.client.as("bench", auth.Owner, "pipelines")
	var next atomic.Int64
	var firstErr atomic.Value
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(dag) {
					return
				}
				j := dag[i]
				out := j[len(j)-1]
				ev := runEvent("COMPLETE", time.Now(), uuid.New(), producerDbt, nsDbt, "build_"+out.Name, "DBT", j[:len(j)-1], []dsRef{out})
				if err := st.client.do(tok, http.MethodPost, "/api/v1/lineage", ev, nil); err != nil {
					firstErr.CompareAndSwap(nil, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if err, ok := firstErr.Load().(error); ok {
		return err
	}
	return nil
}

func benchLineage(args []string) error {
	fs := flag.NewFlagSet("lineage", flag.ExitOnError)
	admin := adminURLFlag(fs)
	sizes := fs.String("sizes", "100,1000,10000", "datasets per graph")
	maxHops := fs.Int("hops", 5, "measure 1..hops")
	queries := fs.Int("queries", 100, "queries per (hops, direction) per run")
	runs := fs.Int("runs", 3, "runs")
	layers := fs.Int("layers", 10, "graph layers")
	fanIn := fs.Int("fan-in", 3, "inputs per job")
	out := fs.String("out", "", "write results JSON here")
	_ = fs.Parse(args)
	ctx := context.Background()

	results := map[string]any{"environment": environment(), "layers": *layers, "fan_in": *fanIn, "queries_per_cell": *queries, "runs": *runs}
	var bySize []map[string]any
	for _, size := range ints(*sizes) {
		st, err := newBenchStack(ctx, *admin, fmt.Sprintf("bench_lineage_%d", size), 32)
		if err != nil {
			return err
		}
		dag := layeredDAG(size, *layers, *fanIn, 42)
		t := time.Now()
		if err := ingestDAG(st, dag, 16); err != nil {
			return err
		}
		build := time.Since(t)
		var ids []int64
		if err := st.pool.QueryRow(ctx, "SELECT array_agg(id ORDER BY name) FROM datasets").Scan(&ids); err != nil {
			return err
		}
		width := size / *layers
		fmt.Printf("%d datasets, %d jobs ingested in %s\n", len(ids), len(dag), build.Round(time.Millisecond))
		// Names sort by layer, so the first `width` ids are layer 0 and the last
		// `width` are the final layer.
		first, last := ids[:width], ids[len(ids)-width:]

		var cells []map[string]any
		for hops := 1; hops <= *maxHops; hops++ {
			for _, dir := range []string{"upstream", "downstream"} {
				roots := last
				if dir == "downstream" {
					roots = first
				}
				var p50s, p95s, nodes []float64
				for run := 0; run < *runs; run++ {
					rng := rand.New(rand.NewPCG(uint64(run), uint64(hops)))
					var lat []float64
					for q := 0; q < *queries; q++ {
						root := roots[rng.IntN(len(roots))]
						var g struct {
							Nodes []struct{} `json:"nodes"`
						}
						t := time.Now()
						if err := st.client.do("", http.MethodGet, fmt.Sprintf("/api/v1/datasets/%d/lineage?direction=%s&depth=%d", root, dir, hops), nil, &g); err != nil {
							return err
						}
						lat = append(lat, ms(time.Since(t)))
						nodes = append(nodes, float64(len(g.Nodes)))
					}
					p50s, p95s = append(p50s, median(lat)), append(p95s, percentile(lat, 95))
				}
				cell := map[string]any{"hops": hops, "direction": dir, "p50_ms": median(p50s), "p95_ms": median(p95s),
					"p50_ms_runs": p50s, "p95_ms_runs": p95s, "nodes_median": median(nodes), "nodes_max": percentile(nodes, 100)}
				cells = append(cells, cell)
				fmt.Printf("  %5d datasets  %-10s %d hops: p50 %.2f ms  p95 %.2f ms  nodes %.0f\n", size, dir, hops, median(p50s), median(p95s), median(nodes))
			}
		}
		bySize = append(bySize, map[string]any{"datasets": size, "jobs": len(dag), "build_seconds": build.Seconds(), "cells": cells})
		st.Close()
	}
	results["sizes"] = bySize
	return writeJSON(*out, results)
}

// ---- read API ----

func benchReads(args []string) error {
	fs := flag.NewFlagSet("reads", flag.ExitOnError)
	admin := adminURLFlag(fs)
	size := fs.Int("datasets", 1000, "registered datasets")
	requests := fs.Int("requests", 2000, "requests per endpoint per run")
	conc := fs.Int("concurrency", 8, "client concurrency")
	runs := fs.Int("runs", 3, "runs")
	out := fs.String("out", "", "write results JSON here")
	_ = fs.Parse(args)
	ctx := context.Background()

	st, err := newBenchStack(ctx, *admin, "bench_reads", 32)
	if err != nil {
		return err
	}
	defer st.Close()
	dag := layeredDAG(*size, 10, 3, 42)
	if err := ingestDAG(st, dag, 16); err != nil {
		return err
	}
	// Register every dataset: owner, SLO, a schema, two consumers.
	owner := st.client.as("bench", auth.Admin, "data-platform")
	var ids []int64
	if err := st.pool.QueryRow(ctx, "SELECT array_agg(id ORDER BY id) FROM datasets").Scan(&ids); err != nil {
		return err
	}
	for i, j := range dag {
		d := j[len(j)-1]
		if err := st.client.do(owner, http.MethodPost, "/api/v1/datasets", map[string]any{
			"namespace": d.Namespace, "name": d.Name, "owner": map[string]string{"team": fmt.Sprintf("team-%d", i%12), "person": "p"},
			"freshness_slo_seconds": 3600, "schema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
		}, nil); err != nil {
			return err
		}
	}
	for _, id := range ids[:200] {
		for _, team := range []string{"finance-analytics", "growth"} {
			_ = st.client.do(owner, http.MethodPost, fmt.Sprintf("/api/v1/datasets/%d/consumers", id), map[string]string{"team": team, "contact": "x"}, nil)
		}
	}

	endpoints := []struct {
		name string
		path func(*rand.Rand) string
	}{
		{"GET /datasets (all)", func(*rand.Rand) string { return fmt.Sprintf("/api/v1/datasets?limit=%d", *size) }},
		{"GET /datasets/{id}", func(r *rand.Rand) string { return fmt.Sprintf("/api/v1/datasets/%d", ids[r.IntN(len(ids))]) }},
		{"GET /datasets/{id}/lineage depth 2", func(r *rand.Rand) string {
			return fmt.Sprintf("/api/v1/datasets/%d/lineage?direction=both&depth=2", ids[r.IntN(len(ids))])
		}},
	}
	results := map[string]any{"environment": environment(), "datasets": *size, "concurrency": *conc, "requests": *requests}
	var rows []map[string]any
	for _, ep := range endpoints {
		var p50s, p95s, rps []float64
		for run := 0; run < *runs; run++ {
			lat := make([]float64, *requests)
			var next atomic.Int64
			var wg sync.WaitGroup
			start := time.Now()
			for w := 0; w < *conc; w++ {
				wg.Add(1)
				go func(seed uint64) {
					defer wg.Done()
					rng := rand.New(rand.NewPCG(seed, 3))
					for {
						i := int(next.Add(1)) - 1
						if i >= *requests {
							return
						}
						t := time.Now()
						if err := st.client.do("", http.MethodGet, ep.path(rng), nil, nil); err != nil {
							fmt.Fprintln(os.Stderr, err)
						}
						lat[i] = ms(time.Since(t))
					}
				}(uint64(w + run*100))
			}
			wg.Wait()
			p50s, p95s = append(p50s, median(lat)), append(p95s, percentile(lat, 95))
			rps = append(rps, float64(*requests)/time.Since(start).Seconds())
		}
		rows = append(rows, map[string]any{"endpoint": ep.name, "p50_ms": median(p50s), "p95_ms": median(p95s), "requests_per_sec": median(rps),
			"p50_ms_runs": p50s, "p95_ms_runs": p95s})
		fmt.Printf("%-36s p50 %.2f ms  p95 %.2f ms  %.0f req/s\n", ep.name, median(p50s), median(p95s), median(rps))
	}
	results["endpoints"] = rows
	return writeJSON(*out, results)
}

// ---- freshness ----

func benchFreshness(args []string) error {
	fs := flag.NewFlagSet("freshness", flag.ExitOnError)
	admin := adminURLFlag(fs)
	intervals := fs.String("intervals", "1s,5s,15s", "evaluator intervals")
	k := fs.Int("datasets", 200, "datasets per run, deadlines spread evenly")
	runs := fs.Int("runs", 3, "runs per interval")
	scale := fs.Int("scale", 10000, "datasets for the evaluation-cost measurement (0 to skip)")
	out := fs.String("out", "", "write results JSON here")
	_ = fs.Parse(args)
	ctx := context.Background()
	results := map[string]any{"environment": environment(), "datasets_per_run": *k}

	var rows []map[string]any
	for _, s := range strings.Split(*intervals, ",") {
		interval, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		var p50s, p95s, maxs []float64
		for run := 0; run < *runs; run++ {
			st, err := newBenchStack(ctx, *admin, "bench_freshness", 8)
			if err != nil {
				return err
			}
			// Deadlines fall evenly over a window of two intervals starting 3 s
			// from now, so they land at every phase of the evaluator's cycle.
			const slo = 60
			window := 2 * interval
			t0 := time.Now().Add(3 * time.Second)
			if _, err := st.pool.Exec(ctx, `
				INSERT INTO datasets (namespace, name, registered, owner_team, owner_person, freshness_slo_seconds, last_write_at, slo_status)
				SELECT 'bench://fresh', 'd' || g, TRUE, 't', 'p', $1,
				       $2::timestamptz - make_interval(secs => $1) + make_interval(secs => $3 * g / $4::float8), 'healthy'
				FROM generate_series(0, $4 - 1) g`, slo, t0, window.Seconds(), *k); err != nil {
				return err
			}
			ev := freshness.New(st.pool, st.log, st.m, interval)
			// Start the evaluator at a random phase relative to the deadlines.
			time.Sleep(time.Duration(rand.Int64N(int64(interval))))
			rctx, cancel := context.WithCancel(ctx)
			go ev.Run(rctx)
			deadline := time.Now().Add(3*time.Second + window + 3*interval)
			var delays []float64
			for time.Now().Before(deadline) {
				time.Sleep(250 * time.Millisecond)
				delays = delays[:0]
				rows, err := st.pool.Query(ctx, `SELECT EXTRACT(EPOCH FROM detected_at - deadline_at)::float8 FROM slo_transitions WHERE to_status = 'breached'`)
				if err != nil {
					cancel()
					return err
				}
				for rows.Next() {
					var d float64
					_ = rows.Scan(&d)
					delays = append(delays, d)
				}
				rows.Close()
				if len(delays) == *k {
					break
				}
			}
			cancel()
			if len(delays) != *k {
				return fmt.Errorf("interval %s: only %d of %d breaches detected", interval, len(delays), *k)
			}
			p50s, p95s, maxs = append(p50s, median(delays)), append(p95s, percentile(delays, 95)), append(maxs, percentile(delays, 100))
			fmt.Printf("interval %s run %d: detection delay p50 %.2f s, p95 %.2f s, max %.2f s\n", interval, run+1, median(delays), percentile(delays, 95), percentile(delays, 100))
			st.Close()
		}
		rows = append(rows, map[string]any{"interval_seconds": interval.Seconds(), "p50_s": median(p50s), "p95_s": median(p95s), "max_s": median(maxs),
			"p50_s_runs": p50s, "p95_s_runs": p95s, "max_s_runs": maxs})
	}
	results["detection_delay"] = rows

	if *scale > 0 {
		st, err := newBenchStack(ctx, *admin, "bench_freshness_scale", 8)
		if err != nil {
			return err
		}
		defer st.Close()
		if _, err := st.pool.Exec(ctx, `
			INSERT INTO datasets (namespace, name, registered, owner_team, owner_person, freshness_slo_seconds, last_write_at)
			SELECT 'bench://fresh', 'd' || g, TRUE, 'team-' || (g % 12), 'p', 3600, now() - make_interval(secs => (g % 7200))
			FROM generate_series(1, $1) g`, *scale); err != nil {
			return err
		}
		ev := freshness.New(st.pool, st.log, st.m, time.Minute)
		var firstPass, steady []float64
		for run := 0; run < *runs; run++ {
			if _, err := st.pool.Exec(ctx, `UPDATE datasets SET slo_status = 'unknown'`); err != nil {
				return err
			}
			s1, err := ev.EvaluateOnce(ctx)
			if err != nil {
				return err
			}
			s2, err := ev.EvaluateOnce(ctx)
			if err != nil {
				return err
			}
			firstPass, steady = append(firstPass, s1.Took.Seconds()), append(steady, s2.Took.Seconds())
			fmt.Printf("evaluate %d SLOs: %d transitions in %s, then %d in %s\n", *scale, s1.Transitions, s1.Took.Round(time.Millisecond), s2.Transitions, s2.Took.Round(time.Millisecond))
		}
		results["evaluation_cost"] = map[string]any{"datasets": *scale, "all_transition_s": median(firstPass), "steady_state_s": median(steady),
			"all_transition_s_runs": firstPass, "steady_state_s_runs": steady}
	}
	return writeJSON(*out, results)
}
