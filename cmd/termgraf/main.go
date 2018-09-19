package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/influxdata/flux"
	_ "github.com/influxdata/flux/builtin"
	"github.com/influxdata/flux/execute"
	"github.com/influxdata/flux/lang"
	"github.com/influxdata/platform/http"
	"github.com/influxdata/platform/query"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err == flag.ErrHelp {
		os.Exit(1)
	} else if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	const host = "http://localhost:8086"

	// Parse flags.
	fs := flag.NewFlagSet("fluxgen", flag.ContinueOnError)
	q := fs.String("q", "", "flux script")
	if err := fs.Parse(args); err != nil {
		return err
	} else if *q == "" {
		return errors.New("query required")
	}

	// Compile query into a spec.
	compiler := lang.FluxCompiler{Query: *q}

	// Execute the flux query.
	svc := &http.FluxQueryService{URL: host}
	itr, err := svc.Query(ctx, &query.Request{
		Authorization:  nil, // q.Authorization,
		OrganizationID: nil, // q.OrganizationID,
		Compiler:       compiler,
	})
	if err != nil {
		return err
	}
	defer itr.Cancel()

	for itr.More() {
		result := itr.Next()
		tables := result.Tables()

		if err := tables.Do(func(tbl flux.Table) error {
			_, err := execute.NewFormatter(tbl, nil).WriteTo(os.Stdout)
			return err
		}); err != nil {
			return err
		}
	}
	if err := itr.Err(); err != nil {
		return err
	}

	return nil
}
