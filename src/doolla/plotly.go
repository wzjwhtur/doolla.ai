package doolla

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"doolla.ai/sdk/internal/i18n"
)

type VisualizationTool struct{ Artifacts ResultFiles }

// WithGenerator supplies an application-defined Plotly figure generator.
// The rows include the CSV header. A nil generator selects PlotlyFigure.
func (t VisualizationTool) WithGenerator(generate func([][]string, string) (map[string]any, error)) Tool {
	if generate == nil {
		return t
	}
	return functionTool{t.Spec(), func(ctx context.Context, u User, raw json.RawMessage) (ToolResult, error) {
		return t.run(ctx, u, raw, generate)
	}}
}

func (t VisualizationTool) Spec() ToolSpec {
	return ToolSpec{"visualize_data", "Create a Plotly chart from a query result. Omit the source when this turn has one result; select its result_id when there are multiple results.",
		objectSchema(map[string]any{
			"result_id": strField("Query result ID returned by run_sql in this turn, e.g. result_2"),
			"filename":  strField("Exact existing CSV filename, for charting a file directly"),
			"title":     strField("Chart title"),
		})}
}

func (t VisualizationTool) Run(ctx context.Context, u User, raw json.RawMessage) (ToolResult, error) {
	return t.run(ctx, u, raw, func(rows [][]string, title string) (map[string]any, error) {
		return plotlyFigure(ctx, rows, title)
	})
}

type visualizationArgs struct {
	ResultID string `json:"result_id"`
	Filename string `json:"filename"`
	Title    string `json:"title"`
}

func (args visualizationArgs) source(ctx context.Context, results []queryResultArtifact) (string, error) {
	if args.ResultID != "" {
		if args.Filename != "" {
			return "", errors.New(i18n.T(ctx, "server.chart.exclusive"))
		}
		for _, result := range results {
			if result.id == args.ResultID {
				return result.filename, nil
			}
		}
		return "", errors.New(i18n.T(ctx, "server.chart.unknown_id", strconv.Quote(args.ResultID)) + queryResultChoices(ctx, results))
	}
	if args.Filename != "" {
		return args.Filename, nil
	}
	switch len(results) {
	case 0:
		return "", errors.New(i18n.T(ctx, "server.chart.no_result"))
	case 1:
		return results[0].filename, nil
	default:
		return "", errors.New(i18n.T(ctx, "server.chart.multiple") + queryResultChoices(ctx, results))
	}
}

func queryResultChoices(ctx context.Context, results []queryResultArtifact) string {
	if len(results) == 0 {
		return ""
	}
	ids := make([]string, len(results))
	for i, result := range results {
		ids[i] = result.id
	}
	return i18n.T(ctx, "server.chart.choices", strings.Join(ids, ", "))
}

func (t VisualizationTool) run(ctx context.Context, u User, raw json.RawMessage, generate func([][]string, string) (map[string]any, error)) (ToolResult, error) {
	var args visualizationArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return ToolResult{}, err
	}
	results, _ := ctx.Value(queryResultsKey{}).([]queryResultArtifact)
	filename, err := args.source(ctx, results)
	if err != nil {
		return ToolResult{}, err
	}
	if t.Artifacts == nil {
		t.Artifacts = Artifacts{Directory: "."}
	}
	data, err := t.Artifacts.Read(u, filename)
	if err != nil {
		if args.Filename != "" && len(results) != 0 && errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("%w.%s", err, queryResultChoices(ctx, results))
		}
		return ToolResult{}, err
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		return ToolResult{}, err
	}
	if args.Title == "" {
		args.Title = i18n.T(ctx, "server.chart.default_title", filename)
	}
	if len(rows) < 2 || len(rows[0]) == 0 {
		return ToolResult{}, errors.New(i18n.T(ctx, "server.chart.empty"))
	}
	chart, err := generate(rows, args.Title)
	if err != nil {
		return ToolResult{}, err
	}
	if chart == nil {
		return ToolResult{}, errors.New(i18n.T(ctx, "server.chart.empty_figure"))
	}
	dataFields := maps.Clone(chart)
	dataFields["chart_type"] = "plotly"
	dataFields["title"] = args.Title
	dataFields["config"] = map[string]any{"data_shape": map[string]int{"rows": len(rows) - 1, "columns": len(rows[0])}, "source_file": filename}
	summary := i18n.T(ctx, "server.chart.created", len(rows)-1, filename)
	for _, result := range results {
		if result.filename == filename && result.truncated {
			notice := i18n.T(ctx, "server.query.limited", len(rows)-1)
			summary += "\n" + notice
			dataFields["truncated"] = true
			dataFields["description"] = notice
			break
		}
	}
	return ToolResult{Content: summary, Component: &Component{Type: "chart", Data: dataFields, Simple: plainToolText(summary)}, Metadata: map[string]any{"filename": filename, "rows": len(rows) - 1, "columns": len(rows[0]), "chart": chart}}, nil
}

// PlotlyFigure creates the same chart families as the Python chart generator.
// Input includes a CSV header in the first row.
func PlotlyFigure(rows [][]string, title string) (map[string]any, error) {
	return plotlyFigure(context.Background(), rows, title)
}

func plotlyFigure(ctx context.Context, rows [][]string, title string) (map[string]any, error) {
	if len(rows) < 2 || len(rows[0]) == 0 {
		return nil, errors.New("cannot visualize an empty data set")
	}
	names := rows[0]
	cols := make([][]any, len(names))
	nums := make([][]float64, len(names))
	var numeric, category, dates []int
	for i := range names {
		isNum, isDate := true, true
		nonempty := 0
		for _, row := range rows[1:] {
			if len(row) != len(names) {
				return nil, errors.New("inconsistent column count")
			}
			value := row[i]
			if value == "" {
				cols[i] = append(cols[i], nil)
				nums[i] = append(nums[i], math.NaN())
				continue
			}
			nonempty++
			n, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				isNum = false
				cols[i] = append(cols[i], value)
			} else {
				cols[i] = append(cols[i], n)
				nums[i] = append(nums[i], n)
			}
			if _, err := time.Parse("2006-01-02", value); err != nil {
				if _, err := time.Parse(time.RFC3339, value); err != nil {
					isDate = false
				}
			}
		}
		switch {
		case isNum && nonempty > 0:
			numeric = append(numeric, i)
		case isDate && nonempty > 0:
			dates = append(dates, i)
		default:
			category = append(category, i)
		}
	}
	layout := map[string]any{"title": map[string]string{"text": title}, "autosize": true, "font": map[string]string{"color": "#023d60"}, "colorway": []string{"#15a8a8", "#fe5d26", "#bf1363", "#023d60"}}
	axes := func(x, y string) {
		layout["xaxis"] = map[string]any{"title": map[string]string{"text": x}}
		layout["yaxis"] = map[string]any{"title": map[string]string{"text": y}}
	}
	traces := []map[string]any{}
	switch {
	case len(names) >= 4:
		traces = append(traces, map[string]any{"type": "table", "header": map[string]any{"values": names}, "cells": map[string]any{"values": cols}})
	case len(dates) > 0 && len(numeric) > 0:
		axes(names[dates[0]], i18n.T(ctx, "server.chart.value"))
		layout["hovermode"] = "x unified"
		for _, n := range numeric {
			traces = append(traces, map[string]any{"type": "scatter", "mode": "lines", "name": names[n], "x": cols[dates[0]], "y": cols[n]})
		}
	case len(numeric) == 1 && len(category) == 0:
		axes(names[numeric[0]], i18n.T(ctx, "server.chart.count"))
		layout["showlegend"] = false
		traces = append(traces, map[string]any{"type": "histogram", "x": cols[numeric[0]]})
	case len(numeric) == 1 && len(category) == 1:
		axes(names[category[0]], names[numeric[0]])
		sums := map[string]float64{}
		for i, key := range cols[category[0]] {
			if key == nil {
				continue
			}
			n := nums[numeric[0]][i]
			if math.IsNaN(n) {
				n = 0
			}
			sums[fmt.Sprint(key)] += n
		}
		keys := make([]string, 0, len(sums))
		for k := range sums {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		values := make([]float64, len(keys))
		for i, k := range keys {
			values[i] = sums[k]
		}
		traces = append(traces, map[string]any{"type": "bar", "x": keys, "y": values})
	case len(numeric) == 2:
		axes(names[numeric[0]], names[numeric[1]])
		traces = append(traces, map[string]any{"type": "scatter", "mode": "markers", "x": cols[numeric[0]], "y": cols[numeric[1]]})
	case len(numeric) >= 3:
		z := make([][]any, len(numeric))
		for i, a := range numeric {
			for _, b := range numeric {
				z[i] = append(z[i], correlation(nums[a], nums[b]))
			}
		}
		traces = append(traces, map[string]any{"type": "heatmap", "x": names, "y": names, "z": z, "zmin": -1, "zmax": 1})
	case len(category) >= 2:
		layout["barmode"] = "group"
		groups := map[string]map[string]int{}
		for i, v := range cols[category[1]] {
			if v == nil || cols[category[0]][i] == nil {
				continue
			}
			name := fmt.Sprint(v)
			if groups[name] == nil {
				groups[name] = map[string]int{}
			}
			groups[name][fmt.Sprint(cols[category[0]][i])]++
		}
		keys := make([]string, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			xs := make([]string, 0, len(groups[k]))
			for x := range groups[k] {
				xs = append(xs, x)
			}
			sort.Strings(xs)
			ys := make([]int, len(xs))
			for i, x := range xs {
				ys[i] = groups[k][x]
			}
			traces = append(traces, map[string]any{"type": "bar", "name": k, "x": xs, "y": ys})
		}
	case len(names) >= 2:
		traces = append(traces, map[string]any{"type": "bar", "x": cols[0], "y": cols[1]})
	case len(names) == 1 && len(category) == 1:
		counts := map[string]int{}
		for _, value := range cols[category[0]] {
			if value != nil {
				counts[fmt.Sprint(value)]++
			}
		}
		if len(counts) == 0 {
			return nil, errors.New(i18n.T(ctx, "server.chart.empty"))
		}
		keys := slices.Sorted(maps.Keys(counts))
		values := make([]int, len(keys))
		for i, k := range keys {
			values[i] = counts[k]
		}
		axes(names[category[0]], i18n.T(ctx, "server.chart.count"))
		traces = append(traces, map[string]any{"type": "bar", "x": keys, "y": values})
	default:
		return nil, errors.New("no suitable chart for these columns")
	}
	return map[string]any{"data": traces, "layout": layout}, nil
}
func correlation(a, b []float64) any {
	var sumA, sumB float64
	var n int
	for i := range a {
		if math.IsNaN(a[i]) || math.IsNaN(b[i]) {
			continue
		}
		sumA += a[i]
		sumB += b[i]
		n++
	}
	if n < 2 {
		return nil
	}
	avgA, avgB := sumA/float64(n), sumB/float64(n)
	var x, y, z float64
	for i := range a {
		if math.IsNaN(a[i]) || math.IsNaN(b[i]) {
			continue
		}
		da, db := a[i]-avgA, b[i]-avgB
		x += da * db
		y += da * da
		z += db * db
	}
	if y*z == 0 {
		return nil
	}
	return x / math.Sqrt(y*z)
}
