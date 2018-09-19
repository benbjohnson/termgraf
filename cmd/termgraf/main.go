package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	ui "github.com/gizak/termui"
	"github.com/influxdata/flux"
	_ "github.com/influxdata/flux/builtin"
	"github.com/influxdata/flux/execute"
	"github.com/influxdata/flux/lang"
	"github.com/influxdata/platform/http"
	"github.com/influxdata/platform/query"
)

const (
	BorderThickness = 1
)

var (
	mu sync.Mutex

	config       = DefaultConfig()
	queryService = &http.FluxQueryService{}

	sparklinesMap = make(map[*Widget]*ui.Sparklines)
	datasetsMap   = make(map[*Widget][]*Dataset)
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
	fs.StringVar(&queryService.URL, "host", "http://localhost:8086", "host URL")
	configFile := fs.String("config", "", "config file")
	if err := fs.Parse(args); err != nil {
		return err
	} else if *configFile == "" {
		return errors.New("config required")
	}

	if err := ReadConfigFile(*configFile, &config); err != nil {
		return err
	}

	if err := ui.Init(); err != nil {
		return err
	}
	defer ui.Close()

	initHandlers()
	initBody()
	render()
	runTimers()

	ui.Loop()
	return nil
}

func initHandlers() {
	// Exit on "q" or CTRL-C.
	ui.Handle("q", func(ui.Event) { ui.StopLoop() })
	ui.Handle("<C-c>", func(ui.Event) { ui.StopLoop() })

	ui.Handle("<Resize>", func(e ui.Event) {
		ui.Body.Width = e.Payload.(ui.Resize).Width
		ui.Body.Align()
		ui.Clear()
		render()
	})
}

func initBody() {
	for _, row := range config.Rows {
		r := &ui.Row{Span: 12}
		for _, widget := range row.Widgets {
			sparklines := ui.NewSparklines()
			sparklines.Height = (widget.Limit * (widget.Height + 1)) + (BorderThickness * 2)
			sparklines.BorderLabel = widget.Title
			sparklinesMap[widget] = sparklines
			r.Cols = append(r.Cols, ui.NewCol(widget.Span, 0, sparklines))
		}
		ui.Body.AddRows(r)
	}
	ui.Body.Align()
}

func render() {
	for _, row := range config.Rows {
		for _, widget := range row.Widgets {
			renderWidget(widget)
		}
	}
}

func renderWidget(widget *Widget) {
	mu.Lock()
	datasets := datasetsMap[widget]
	mu.Unlock()

	sparklines := sparklinesMap[widget]

	// Remove sparklines if too many.
	if len(sparklines.Lines) > len(datasets) {
		sparklines.Lines = sparklines.Lines[:len(datasets)]
	}

	// Add sparklines as needed.
	for i := len(sparklines.Lines); i < len(datasets); i++ {
		sparklines.Add(ui.Sparkline{
			Height:     widget.Height,
			TitleColor: LookupColor(widget.Color),
			LineColor:  LookupColor(widget.Color),
		})
	}

	// Update sparkline title & data.
	for i, ds := range datasets {
		sparkline := &sparklines.Lines[i]
		sparkline.Title = ds.Title
		sparkline.Data = ds.Values
	}

	ui.Render(ui.Body)
}

func update(widget *Widget) {
	ctx := context.Background()

	// Process variables in template.
	var buf bytes.Buffer
	if err := widget.queryTemplate.Execute(&buf, &TemplateData{
		Range: TemplateRange{
			Start: "-40s",
			Stop:  "-10s",
		},
		Window: TemplateWindow{
			Every: "1s",
		},
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}

	// Execute the flux query.
	itr, err := queryService.Query(ctx, &query.Request{
		Compiler: lang.FluxCompiler{Query: buf.String()},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer itr.Cancel()

	var datasets []*Dataset
	for itr.More() {
		result := itr.Next()
		tables := result.Tables()

		if err := tables.Do(func(tbl flux.Table) error {
			ds := &Dataset{Title: FormatDatasetTitle(tbl)}

			if err := tbl.Do(func(cr flux.ColReader) error {
				columnName := widget.Column
				if columnName == "" {
					columnName = "_value"
				}

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

	mu.Lock()
	datasetsMap[widget] = datasets
	mu.Unlock()

	render()
}

func runTimers() {
	for _, row := range config.Rows {
		for i := range row.Widgets {
			widget := row.Widgets[i]
			go runWidgetTimer(widget)
		}
	}
}

func runWidgetTimer(widget *Widget) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		update(widget)
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

type Config struct {
	Rows []*Row `json:"rows"`
}

type Row struct {
	Widgets []*Widget `json:"widgets"`
}

type Widget struct {
	queryTemplate *template.Template `json:"-"`

	Title  string `json:"title"`
	Query  string `json:"query"`
	Column string `json:"column"`
	Color  string `json:"color"`
	Height int    `json:"height"`
	Span   int    `json:"span"`
	Limit  int    `json:"limit"`
}

func DefaultConfig() Config {
	return Config{}
}

func ReadConfigFile(filename string, config *Config) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := json.NewDecoder(f).Decode(config); err != nil {
		return err
	}

	for _, row := range config.Rows {
		for _, widget := range row.Widgets {
			if widget.Height < 1 {
				widget.Height = 1
			}

			if strings.HasPrefix(widget.Query, "@") {
				buf, err := ioutil.ReadFile(filepath.Join(filepath.Dir(filename), widget.Query[1:]))
				if err != nil {
					return err
				}

				tmpl, err := template.New("main").Parse(string(buf))
				if err != nil {
					return err
				}
				widget.queryTemplate = tmpl
			}
		}
	}
	return nil
}

func LookupColor(s string) ui.Attribute {
	switch s {
	case "black":
		return ui.ColorBlack
	case "red":
		return ui.ColorRed
	case "green":
		return ui.ColorGreen
	case "yellow":
		return ui.ColorYellow
	case "blue":
		return ui.ColorBlue
	case "magenta":
		return ui.ColorMagenta
	case "cyan":
		return ui.ColorCyan
	case "white":
		return ui.ColorWhite
	default:
		return ui.ColorGreen
	}
}

type TemplateData struct {
	Window TemplateWindow
	Range  TemplateRange
}

type TemplateRange struct {
	Start string
	Stop  string
}

type TemplateWindow struct {
	Every string
}
