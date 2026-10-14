// Command drbench seeds the demo, generates OpenLineage traffic and measures
// the registry.
//
//	drbench seed     register the Lumen & Loom catalogue and replay its pipeline history
//	drbench follow   keep emitting pipeline runs (except for the dataset that went stale)
//	drbench demo     walk through ownership, a breaking change and an SLO breach
//	drbench compat   score the compatibility checker on the labelled cases
//	drbench ingest   OpenLineage ingest throughput and latency
//	drbench lineage  lineage query latency by graph size and hop count
//	drbench reads    read API latency
//	drbench freshness  breach detection delay against the evaluator interval
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmds := map[string]func([]string) error{
		"seed": seed, "follow": follow, "demo": demo,
		"compat": benchCompat, "ingest": benchIngest, "lineage": benchLineage,
		"reads": benchReads, "freshness": benchFreshness,
	}
	run, ok := cmds[os.Args[1]]
	if !ok {
		usage()
	}
	if err := run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "drbench:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: drbench seed|follow|demo|compat|ingest|lineage|reads|freshness [flags]")
	os.Exit(2)
}
