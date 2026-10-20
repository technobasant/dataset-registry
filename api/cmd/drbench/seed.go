package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/technobasant/dataset-registry/api/internal/auth"
)

// The catalogue of Lumen & Loom, an online homewares retailer: Kafka topics
// land in Iceberg through Spark, dbt models build marts, Airflow loads a few
// things by hand, and a feature pipeline feeds the ML team. Everyone and
// everything here is invented.

const (
	nsKafka   = "kafka://lumen-kafka:9092"
	nsLake    = "iceberg://lake"
	nsFX      = "https://fx-provider.example"
	nsAirflow = "airflow://lumen-airflow"
	nsDbt     = "dbt://lumen-analytics"
	nsSpark   = "spark://lumen-spark"
)

type person struct{ name, team string }

var (
	amara = person{"amara.okafor", "checkout"}
	theo  = person{"theo.lindqvist", "payments"}
	mateo = person{"mateo.silva", "fulfilment"}
	noor  = person{"noor.haddad", "analytics-engineering"}
	sofia = person{"sofia.ricci", "ml-platform"}
	ines  = person{"ines.moreau", "data-platform"}
)

type seedDataset struct {
	ref    dsRef
	owner  person
	desc   string
	tier   string
	slo    time.Duration
	schema string
}

func obj(props string, required ...string) string {
	req, _ := json.Marshal(required)
	return fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s}`, props, req)
}

var catalogue = []seedDataset{
	{dsRef{nsKafka, "orders.v1"}, amara, "Order events as placed at checkout, one per state change.", "gold", 10 * time.Minute,
		obj(`"order_id":{"type":"string"},"customer_id":{"type":"string"},"amount_cents":{"type":"integer","minimum":0},"currency":{"type":"string","enum":["EUR","GBP","USD"]},"status":{"type":"string","enum":["placed","paid","shipped","cancelled"]},"placed_at":{"type":"string","format":"date-time"}`,
			"order_id", "customer_id", "amount_cents", "currency", "status", "placed_at")},
	{dsRef{nsKafka, "payments.v1"}, theo, "Payment authorisations and captures.", "gold", 10 * time.Minute,
		obj(`"payment_id":{"type":"string"},"order_id":{"type":"string"},"amount_cents":{"type":"integer"},"state":{"type":"string","enum":["authorised","captured","refunded","failed"]},"at":{"type":"string","format":"date-time"}`,
			"payment_id", "order_id", "amount_cents", "state", "at")},
	{dsRef{nsLake, "raw.orders"}, amara, "Orders landed from Kafka by Spark, append-only.", "gold", 30 * time.Minute,
		obj(`"order_id":{"type":"string"},"customer_id":{"type":"string"},"amount_cents":{"type":"integer"},"currency":{"type":"string"},"status":{"type":"string"},"placed_at":{"type":"string","format":"date-time"},"ingested_at":{"type":"string","format":"date-time"}`,
			"order_id", "customer_id", "amount_cents", "currency", "placed_at")},
	{dsRef{nsLake, "raw.payments"}, theo, "Payments landed from Kafka by Spark.", "gold", 30 * time.Minute,
		obj(`"payment_id":{"type":"string"},"order_id":{"type":"string"},"amount_cents":{"type":"integer"},"state":{"type":"string"},"at":{"type":"string","format":"date-time"}`,
			"payment_id", "order_id", "amount_cents", "state")},
	{dsRef{nsLake, "raw.inventory_snapshots"}, mateo, "Nightly stock levels per SKU and warehouse, loaded by Airflow from the WMS export.", "silver", time.Hour,
		obj(`"sku":{"type":"string"},"warehouse":{"type":"string","enum":["AMS","LDN","MAD"]},"on_hand":{"type":"integer","minimum":0},"snapshot_at":{"type":"string","format":"date-time"}`,
			"sku", "warehouse", "on_hand", "snapshot_at")},
	{dsRef{nsLake, "staging.orders"}, noor, "Deduplicated orders with currency normalised (dbt).", "silver", time.Hour,
		obj(`"order_id":{"type":"string"},"customer_id":{"type":"string"},"amount_eur_cents":{"type":"integer"},"status":{"type":"string","enum":["placed","paid","shipped","cancelled"]},"order_date":{"type":"string","format":"date"}`,
			"order_id", "customer_id", "amount_eur_cents", "status", "order_date")},
	{dsRef{nsLake, "staging.payments"}, noor, "Latest state per payment (dbt).", "silver", time.Hour,
		obj(`"payment_id":{"type":"string"},"order_id":{"type":"string"},"captured_cents":{"type":"integer"},"refunded_cents":{"type":"integer"}`,
			"payment_id", "order_id", "captured_cents", "refunded_cents")},
	{dsRef{nsLake, "marts.daily_revenue"}, noor, "Net revenue per day and channel. Finance closes the month on this.", "gold", 2 * time.Hour,
		obj(`"day":{"type":"string","format":"date"},"channel":{"type":"string","enum":["web","app","marketplace"]},"orders":{"type":"integer","minimum":0},"net_revenue_cents":{"type":"integer"}`,
			"day", "channel", "orders", "net_revenue_cents")},
	{dsRef{nsLake, "marts.inventory_health"}, mateo, "Days of cover per SKU; drives reorder alerts.", "silver", 2 * time.Hour,
		obj(`"sku":{"type":"string"},"warehouse":{"type":"string"},"days_of_cover":{"type":"number","minimum":0}`, "sku", "warehouse", "days_of_cover")},
	{dsRef{nsLake, "features.customer_7d_spend"}, sofia, "Rolling 7-day spend per customer for the recommender and fraud models.", "silver", time.Hour,
		obj(`"customer_id":{"type":"string"},"spend_7d_cents":{"type":"integer","minimum":0},"orders_7d":{"type":"integer","minimum":0},"as_of":{"type":"string","format":"date-time"}`,
			"customer_id", "spend_7d_cents", "orders_7d", "as_of")},
}

type seedConsumer struct {
	dataset dsRef
	who     person
	purpose string
}

var (
	jun  = person{"jun.park", "finance-analytics"}
	hana = person{"hana.sato", "growth"}
	ravi = person{"ravi.menon", "recommendations"}
)

var consumersSeed = []seedConsumer{
	{dsRef{nsKafka, "orders.v1"}, person{"noor.haddad", "analytics-engineering"}, "lands raw.orders"},
	{dsRef{nsLake, "raw.orders"}, person{"noor.haddad", "analytics-engineering"}, "builds staging.orders"},
	{dsRef{nsLake, "raw.orders"}, sofia, "builds customer spend features"},
	{dsRef{nsLake, "raw.payments"}, person{"noor.haddad", "analytics-engineering"}, "builds staging.payments"},
	{dsRef{nsLake, "raw.inventory_snapshots"}, mateo, "builds inventory health"},
	{dsRef{nsLake, "marts.daily_revenue"}, jun, "month-end close and the revenue dashboard"},
	{dsRef{nsLake, "marts.daily_revenue"}, hana, "channel spend efficiency"},
	{dsRef{nsLake, "features.customer_7d_spend"}, ravi, "home page recommendations"},
}

// A job: how often it runs and which datasets it reads and writes.
type seedJob struct {
	ns, name, producer, integration string
	every                           time.Duration
	in, out                         []dsRef
	// stoppedAgo: the job's last successful run was this long ago and it has
	// not run since. Used for the dataset that goes stale.
	stoppedAgo time.Duration
}

func lake(n string) dsRef { return dsRef{nsLake, n} }

var jobs = []seedJob{
	{nsSpark, "ingest_orders", producerSpark, "SPARK", 15 * time.Minute, []dsRef{{nsKafka, "orders.v1"}}, []dsRef{lake("raw.orders")}, 0},
	{nsSpark, "ingest_payments", producerSpark, "SPARK", 15 * time.Minute, []dsRef{{nsKafka, "payments.v1"}}, []dsRef{lake("raw.payments")}, 0},
	{nsSpark, "orders_dedup_scratch", producerSpark, "SPARK", time.Hour, []dsRef{lake("raw.orders")}, []dsRef{lake("scratch.orders_dedup_tmp")}, 0},
	{nsAirflow, "checkout_stream_producer", producerAirflow, "AIRFLOW", 5 * time.Minute, nil, []dsRef{{nsKafka, "orders.v1"}}, 0},
	{nsAirflow, "payments_stream_producer", producerAirflow, "AIRFLOW", 5 * time.Minute, nil, []dsRef{{nsKafka, "payments.v1"}}, 0},
	{nsAirflow, "load_fx_rates", producerAirflow, "AIRFLOW", time.Hour, []dsRef{{nsFX, "/v1/rates"}}, []dsRef{lake("staging.fx_rates")}, 0},
	{nsAirflow, "snapshot_inventory", producerAirflow, "AIRFLOW", 30 * time.Minute, nil, []dsRef{lake("raw.inventory_snapshots")}, 3 * time.Hour},
	{nsDbt, "lumen.stg_orders", producerDbt, "DBT", 30 * time.Minute, []dsRef{lake("raw.orders"), lake("staging.fx_rates")}, []dsRef{lake("staging.orders")}, 0},
	{nsDbt, "lumen.stg_payments", producerDbt, "DBT", 30 * time.Minute, []dsRef{lake("raw.payments")}, []dsRef{lake("staging.payments")}, 0},
	{nsDbt, "lumen.daily_revenue", producerDbt, "DBT", time.Hour, []dsRef{lake("staging.orders"), lake("staging.payments")}, []dsRef{lake("marts.daily_revenue")}, 0},
	{nsDbt, "lumen.inventory_health", producerDbt, "DBT", time.Hour, []dsRef{lake("raw.inventory_snapshots"), lake("staging.orders")}, []dsRef{lake("marts.inventory_health")}, 0},
	{nsSpark, "feature_customer_spend", producerSpark, "SPARK", 20 * time.Minute, []dsRef{lake("raw.orders")}, []dsRef{lake("features.customer_7d_spend")}, 50 * time.Minute},
	{nsAirflow, "export_revenue_extract", producerAirflow, "AIRFLOW", time.Hour, []dsRef{lake("marts.daily_revenue")}, []dsRef{lake("bi.revenue_extract")}, 0},
}

func (c *client) emit(owner string, j seedJob, at time.Time, state string) error {
	tok := c.as(owner, auth.Owner, "pipelines")
	run := uuid.New()
	start := runEvent("START", at.Add(-2*time.Minute), run, j.producer, j.ns, j.name, j.integration, j.in, j.out)
	if err := c.do(tok, http.MethodPost, "/api/v1/lineage", start, nil); err != nil {
		return err
	}
	end := runEvent(state, at, run, j.producer, j.ns, j.name, j.integration, j.in, j.out)
	return c.do(tok, http.MethodPost, "/api/v1/lineage", end, nil)
}

func seed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	base := fs.String("api", envOr("DR_API_URL", "http://localhost:8088"), "registry base URL")
	hours := fs.Int("history-hours", 6, "hours of pipeline history to replay")
	_ = fs.Parse(args)
	c, err := newClient(*base)
	if err != nil {
		return err
	}

	ids := map[dsRef]int64{}
	for _, d := range catalogue {
		var out struct {
			ID int64 `json:"id"`
		}
		slo := int(d.slo.Seconds())
		err := c.do(c.as(d.owner.name, auth.Owner, d.owner.team), http.MethodPost, "/api/v1/datasets", map[string]any{
			"namespace": d.ref.Namespace, "name": d.ref.Name, "description": d.desc,
			"owner": map[string]string{"team": d.owner.team, "person": d.owner.name},
			"tier":  d.tier, "freshness_slo_seconds": slo, "schema": json.RawMessage(d.schema),
		}, &out)
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			fmt.Println("already registered:", d.ref.Name)
			if err := c.do("", http.MethodGet, "/api/v1/datasets/by-name?"+url.Values{"namespace": {d.ref.Namespace}, "name": {d.ref.Name}}.Encode(), nil, &out); err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("register %s: %w", d.ref.Name, err)
		}
		ids[d.ref] = out.ID
	}
	fmt.Printf("registered %d datasets\n", len(catalogue))

	for _, cs := range consumersSeed {
		err := c.do(c.as(cs.who.name, auth.Consumer, cs.who.team), http.MethodPost,
			fmt.Sprintf("/api/v1/datasets/%d/consumers", ids[cs.dataset]),
			map[string]string{"contact": cs.who.name + "@lumenloom.example", "purpose": cs.purpose}, nil)
		var ae *apiError
		if err != nil && (!errors.As(err, &ae) || ae.Status != http.StatusConflict) {
			return fmt.Errorf("consumer %s on %s: %w", cs.who.team, cs.dataset.Name, err)
		}
	}
	fmt.Printf("registered %d consumer dependencies\n", len(consumersSeed))

	// Pipeline history: each job runs on its schedule over the last few hours.
	now := time.Now()
	events := 0
	for _, j := range jobs {
		// Runs land on the schedule ending exactly at the last run.
		last := now.Add(-j.stoppedAgo)
		first := last.Add(-time.Duration(int64(time.Duration(*hours)*time.Hour) / int64(j.every) * int64(j.every)))
		for at := first; !at.After(last); at = at.Add(j.every) {
			state := "COMPLETE"
			if j.name == "lumen.inventory_health" && at.After(now.Add(-90*time.Minute)) && at.Before(now.Add(-30*time.Minute)) {
				state = "FAIL" // one failed run in the history, so the graph shows it
			}
			if err := c.emit(ines.name, j, at, state); err != nil {
				return fmt.Errorf("lineage for %s: %w", j.name, err)
			}
			events += 2
		}
	}
	fmt.Printf("replayed %d OpenLineage events (Airflow, dbt, Spark)\n", events)

	// raw.orders gets a compatible change: a new optional field.
	rawOrders := catalogue[2]
	v2 := strings.Replace(rawOrders.schema, `"ingested_at":`, `"channel":{"type":"string"},"ingested_at":`, 1)
	if err := c.do(c.as(amara.name, auth.Owner, amara.team), http.MethodPost, fmt.Sprintf("/api/v1/datasets/%d/schemas", ids[rawOrders.ref]),
		map[string]any{"schema": json.RawMessage(v2)}, nil); err != nil {
		fmt.Println("raw.orders v2:", err)
	}

	// marts.daily_revenue gets a breaking one: revenue becomes a decimal.
	// Finance has acknowledged; growth has not answered yet.
	rev := catalogue[7]
	v2rev := strings.Replace(rev.schema, `"net_revenue_cents":{"type":"integer"}`, `"net_revenue_cents":{"type":"number"}`, 1)
	var proposed struct {
		ChangeRequest *struct {
			ID int64 `json:"id"`
		} `json:"change_request"`
	}
	err = c.do(c.as(noor.name, auth.Owner, noor.team), http.MethodPost, fmt.Sprintf("/api/v1/datasets/%d/schemas", ids[rev.ref]),
		map[string]any{"schema": json.RawMessage(v2rev)}, &proposed)
	if err == nil && proposed.ChangeRequest != nil {
		_ = c.do(c.as(jun.name, auth.Consumer, jun.team), http.MethodPost, fmt.Sprintf("/api/v1/change-requests/%d/reviews", proposed.ChangeRequest.ID),
			map[string]string{"decision": "acknowledged", "reason": "the close workbook already reads it as a decimal"}, nil)
		fmt.Printf("opened change request #%d on marts.daily_revenue (waiting on growth)\n", proposed.ChangeRequest.ID)
	} else if err != nil {
		fmt.Println("marts.daily_revenue v2:", err)
	}
	fmt.Println("seeded. raw.inventory_snapshots stopped loading 3 h ago against a 1 h SLO; the evaluator will mark it breached.")
	return nil
}

// follow keeps the healthy pipelines running so their datasets stay fresh,
// while the inventory snapshot and the feature job stay stopped.
func follow(args []string) error {
	fs := flag.NewFlagSet("follow", flag.ExitOnError)
	base := fs.String("api", envOr("DR_API_URL", "http://localhost:8088"), "registry base URL")
	every := fs.Duration("every", time.Minute, "how often to emit a round of runs")
	_ = fs.Parse(args)
	c, err := newClient(*base)
	if err != nil {
		return err
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	t := time.NewTicker(*every)
	defer t.Stop()
	for {
		n := 0
		for _, j := range jobs {
			if j.stoppedAgo > 0 {
				continue
			}
			if err := c.emit(ines.name, j, time.Now(), "COMPLETE"); err != nil {
				return err
			}
			n++
		}
		fmt.Printf("%s emitted %d runs\n", time.Now().Format(time.TimeOnly), n)
		select {
		case <-stop:
			return nil
		case <-t.C:
		}
	}
}
