package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	ui "github.com/gizak/termui"
	"github.com/influxdata/flux"
	_ "github.com/influxdata/flux/builtin"
	"github.com/influxdata/flux/execute"
	"github.com/influxdata/flux/lang"
	"github.com/influxdata/platform/http"
	"github.com/influxdata/platform/query"
)

// const host =
const host = "http://bcddb52e.ngrok.io:80"

var (
	host       string
	q          string
	columnName string

	sparklines *ui.Sparklines
	datasets   []*Dataset
)

var queryService = &http.FluxQueryService{URL: host}

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
	fs.StringVar(&host, "host", "http://localhost:8086", "host URL")
	fs.StringVar(&q, "query", "", "flux script")
	fs.StringVar(&columnName, "column", "_value", "column name")
	if err := fs.Parse(args); err != nil {
		return err
	} else if q == "" {
		return errors.New("query required")
	}

	if err := ui.Init(); err != nil {
		return err
	}
	defer ui.Close()

	initHandlers()
	initBody()
	render()
	go runTimer()

	ui.Loop()
	return nil
}

func initHandlers() {
	// Exit on "q" or CTRL-C.
	ui.Handle("q", func(ui.Event) { ui.StopLoop() })
	ui.Handle("<C-c>", func(ui.Event) { ui.StopLoop() })
	ui.Handle("r", func(ui.Event) { update() })

	ui.Handle("<Resize>", func(e ui.Event) {
		ui.Body.Width = e.Payload.(ui.Resize).Width
		ui.Body.Align()
		ui.Clear()
		render()
	})

	ui.Handle("/timer/10s", func(ui.Event) { render() })
}

func initBody() {
	sparklines = ui.NewSparklines()
	sparklines.Height = 20
	sparklines.BorderLabel = "termgraf"

	ui.Body.AddRows(
		ui.NewRow(
			ui.NewCol(12, 0, sparklines),
		),
	)

	ui.Body.Align()
}

func render() {
	// Remove sparklines if too many.
	if len(sparklines.Lines) > len(datasets) {
		sparklines.Lines = sparklines.Lines[:len(datasets)]
	}

	// Add sparklines as needed.
	for i := len(sparklines.Lines); i < len(datasets); i++ {
		sparklines.Add(ui.Sparkline{
			Height:     1,
			TitleColor: ui.ColorGreen,
			LineColor:  ui.ColorGreen,
		})
	}

	// Update sparkline title & data.
	for i, ds := range datasets {
		sparkline := &sparklines.Lines[i]
		sparkline.Title = ds.Title
		sparkline.Data = ds.Values
	}

	// ui.Clear()
	ui.Render(ui.Body)
}

func update() {
	ctx := context.Background()

	// Execute the flux query.
	itr, err := queryService.Query(ctx, &query.Request{
		Compiler: lang.FluxCompiler{Query: q},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer itr.Cancel()

	datasets = nil
	for itr.More() {
		result := itr.Next()
		tables := result.Tables()

		if err := tables.Do(func(tbl flux.Table) error {
			ds := &Dataset{Title: FormatDatasetTitle(tbl)}

			if err := tbl.Do(func(cr flux.ColReader) error {
				idx := execute.ColIdx(columnName, cr.Cols())
				if idx == -1 {
					return nil
				}

				switch ColType(columnName, cr.Cols()) {
				case flux.TInt:
					for _, v := range cr.Ints(idx) {
						ds.Values = append(ds.Values, int(v))
					}
				case flux.TUInt:
					for _, v := range cr.UInts(idx) {
						ds.Values = append(ds.Values, int(v))
					}
				case flux.TFloat:
					for _, v := range cr.Floats(idx) {
						ds.Values = append(ds.Values, int(v))
					}
				}

				return nil
			}); err != nil {
				return err
			}

			datasets = append(datasets, ds)
			return err
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
	}
	if err := itr.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	render()
}

func runTimer() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		update()
	}
}

type Dataset struct {
	Title  string
	Values []int
}

func FormatDatasetTitle(tbl flux.Table) string {
	var buf bytes.Buffer
	key := tbl.Key()

	var a []string
	for i := range key.Cols() {
		a = append(a, key.ValueString(i))
	}
	buf.WriteString(strings.Join(a, ", "))
	buf.WriteString("\n")
	return buf.String()
}

func ColType(label string, cols []flux.ColMeta) flux.DataType {
	for _, c := range cols {
		if c.Label == label {
			return c.Type
		}
	}
	return flux.TInvalid
}
