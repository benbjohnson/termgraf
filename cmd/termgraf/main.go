package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	ui "github.com/gizak/termui"
	"github.com/influxdata/flux"
	_ "github.com/influxdata/flux/builtin"
	"github.com/influxdata/flux/execute"
	"github.com/influxdata/flux/lang"
	"github.com/influxdata/platform/http"
	"github.com/influxdata/platform/query"
)

var (
	sparklines *ui.Sparklines
	sparklineN int
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
	// Parse flags.
	fs := flag.NewFlagSet("termgraf", flag.ContinueOnError)
	q := fs.String("q", "", "flux script")
	if err := fs.Parse(args); err != nil {
		return err
	} else if *q == "" {
		return errors.New("query required")
	}

	if err := ui.Init(); err != nil {
		return err
	}
	defer ui.Close()

	initHandlers()

	sparklines = ui.NewSparklines()
	sparklines.Height = 20
	sparklines.BorderLabel = "termgraf"

	ui.Body.AddRows(
		ui.NewRow(
			ui.NewCol(12, 0, sparklines),
		),
	)

	ui.Body.Align()
	render()

	ui.Loop()

	return nil
}

func initHandlers() {
	// Exit on "q" or CTRL-C.
	ui.Handle("q", func(ui.Event) { ui.StopLoop() })
	ui.Handle("<C-c>", func(ui.Event) { ui.StopLoop() })

	ui.Handle("a", func(ui.Event) { sparklineN++; render() })

	ui.Handle("<Resize>", func(e ui.Event) {
		ui.Body.Width = e.Payload.(ui.Resize).Width
		ui.Body.Align()
		ui.Clear()
		render()
	})

	ui.Handle("/timer/1s", func(ui.Event) { render() })
}

func render() {
	// Add/remove sparklines as needed.
	for i := len(sparklines.Lines); i < sparklineN; i++ {
		sparklines.Add(ui.Sparkline{
			Height:     1,
			Data:       []int{1, 2, 3, 4, 5, 6, 4, 3, 2, 1, 1, 2, 3, 4, 5, 6, 4, 3, 2, 1, 1, 2, 3, 4, 5, 6, 4, 3, 2, 1},
			Title:      fmt.Sprintf("Sparkline %d", i+1),
			TitleColor: ui.ColorGreen,
			LineColor:  ui.ColorGreen,
		})
	}

	ui.Render(ui.Body)
}

func runQuery(ctx context.Context, q string) error {
	const host = "http://localhost:8086"

	// Compile query into a spec.
	compiler := lang.FluxCompiler{Query: q}

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
