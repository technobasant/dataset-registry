package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/technobasant/dataset-registry/api/internal/auth"
)

// demo walks through the registry's three promises against a seeded stack.
func demo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	base := fs.String("api", envOr("DR_API_URL", "http://localhost:8088"), "registry base URL")
	_ = fs.Parse(args)
	c, err := newClient(*base)
	if err != nil {
		return err
	}
	step := func(s string) { fmt.Printf("\n── %s\n", s) }
	lookup := func(ns, name string) (map[string]any, error) {
		var d map[string]any
		err := c.do("", http.MethodGet, "/api/v1/datasets/by-name?"+url.Values{"namespace": {ns}, "name": {name}}.Encode(), nil, &d)
		return d, err
	}

	step("1. Ownership before consumption")
	var list struct {
		Datasets []map[string]any `json:"datasets"`
	}
	if err := c.do("", http.MethodGet, "/api/v1/datasets?registered=false", nil, &list); err != nil {
		return err
	}
	for _, d := range list.Datasets {
		fmt.Printf("   unowned, seen only in lineage: %s/%s\n", d["namespace"], d["name"])
	}
	scratch, err := lookup(nsLake, "scratch.orders_dedup_tmp")
	if err != nil {
		return err
	}
	err = c.do(c.as(hana.name, auth.Consumer, hana.team), http.MethodPost, fmt.Sprintf("/api/v1/datasets/%v/consumers", scratch["id"]),
		map[string]string{"contact": "hana.sato@lumenloom.example"}, nil)
	fmt.Printf("   growth tries to depend on scratch.orders_dedup_tmp → %v\n", short(err))

	step("2. A breaking change waits for every consumer")
	rev, err := lookup(nsLake, "marts.daily_revenue")
	if err != nil {
		return err
	}
	cr, _ := rev["open_change_request"].(map[string]any)
	if cr == nil {
		fmt.Println("   no open change request on marts.daily_revenue (already published?) — run `make seed` on a fresh stack")
	} else {
		id := int64(cr["id"].(float64))
		report := cr["report"].(map[string]any)
		for _, ch := range report["changes"].([]any) {
			m := ch.(map[string]any)
			fmt.Printf("   %s %s: %s\n", m["path"], m["kind"], m["detail"])
		}
		owner := c.as(noor.name, auth.Owner, noor.team)
		err = c.do(owner, http.MethodPost, fmt.Sprintf("/api/v1/change-requests/%d/publish", id), nil, nil)
		fmt.Printf("   owner publishes before growth answers → %v\n", short(err))
		err = c.do(c.as(ines.name, auth.Admin, ines.team), http.MethodPost, fmt.Sprintf("/api/v1/change-requests/%d/reviews", id),
			map[string]string{"decision": "acknowledged"}, nil)
		fmt.Printf("   an admin tries to acknowledge for growth → %v\n", short(err))
		err = c.do(c.as(hana.name, auth.Consumer, hana.team), http.MethodPost, fmt.Sprintf("/api/v1/change-requests/%d/reviews", id),
			map[string]string{"decision": "acknowledged", "reason": "spend model reads revenue as float already"}, nil)
		fmt.Printf("   growth acknowledges → %v\n", short(err))
		var done map[string]any
		err = c.do(owner, http.MethodPost, fmt.Sprintf("/api/v1/change-requests/%d/publish", id), nil, &done)
		if err != nil {
			return err
		}
		fmt.Printf("   owner publishes → %s; marts.daily_revenue is now on version %v\n", done["state"], done["to_version"])
	}

	step("3. Lineage from OpenLineage events")
	var g struct {
		Nodes []map[string]any `json:"nodes"`
		Edges []any            `json:"edges"`
	}
	if err := c.do("", http.MethodGet, fmt.Sprintf("/api/v1/datasets/%v/lineage?direction=upstream&depth=3", rev["id"]), nil, &g); err != nil {
		return err
	}
	var up []string
	for _, n := range g.Nodes {
		if n["kind"] == "dataset" && n["name"] != "marts.daily_revenue" {
			owner := "UNOWNED"
			if n["owner_team"] != nil {
				owner = n["owner_team"].(string)
			}
			up = append(up, fmt.Sprintf("%s (%s)", n["name"], owner))
		}
	}
	fmt.Printf("   marts.daily_revenue depends on, within 3 hops: %s\n", strings.Join(up, ", "))

	step("4. Freshness SLOs")
	if err := c.do("", http.MethodGet, "/api/v1/datasets?registered=true", nil, &list); err != nil {
		return err
	}
	for _, d := range list.Datasets {
		age := "never written"
		if s, ok := d["last_write_at"].(string); ok {
			t, _ := time.Parse(time.RFC3339Nano, s)
			age = time.Since(t).Round(time.Minute).String() + " ago"
		}
		slo := time.Duration(d["freshness_slo_seconds"].(float64)) * time.Second
		fmt.Printf("   %-28s %-9s last write %-12s SLO %s\n", d["name"], d["slo_status"], age, slo)
	}
	fmt.Println("\n   Prometheus: http://localhost:9098/graph?g0.expr=registry_dataset_slo_status%7Bstatus%3D%22breached%22%7D%20%3D%3D%201")
	fmt.Println("   UI:         http://localhost:3088")
	return nil
}

func short(err error) string {
	if err == nil {
		return "ok"
	}
	var ae *apiError
	if errors.As(err, &ae) {
		var p struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal([]byte(ae.Body), &p)
		return fmt.Sprintf("%d %s", ae.Status, p.Detail)
	}
	return err.Error()
}
