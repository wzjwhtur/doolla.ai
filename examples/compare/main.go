// Command compare runs local Ollama models against the same SQLite evaluation case.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/evaluation"
)

func saveReport(path string, reports []evaluation.Report, write func(io.Writer, []evaluation.Report) error) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(file, reports); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func run() error {
	endpoint := flag.String("url", "http://127.0.0.1:11434", "local Ollama base URL")
	flag.Parse()
	if flag.NArg() < 2 {
		return fmt.Errorf("pass at least two installed model names")
	}
	db, err := doolla.OpenWritableSQLite(":memory:")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE sales(units INTEGER); INSERT INTO sales VALUES (12), (8), (15); PRAGMA query_only=ON"); err != nil {
		return err
	}
	var variants []evaluation.Variant
	for _, name := range flag.Args() {
		model, err := doolla.NewModel(doolla.ModelConfig{Provider: "ollama", Model: name, BaseURL: *endpoint})
		if err != nil {
			return err
		}
		agent, err := doolla.NewAgent(model, "Use run_sql to answer the question from the SQLite table sales(units).",
			doolla.Registration{Tool: doolla.SQLTool{DB: db, DisableArtifacts: true}})
		if err != nil {
			return err
		}
		variants = append(variants, evaluation.Variant{Name: name, Agent: agent})
	}
	data := evaluation.Dataset{Name: "Sales totals", Cases: []evaluation.Case{{
		ID: "total", Message: "Call run_sql to execute SELECT SUM(units) AS total FROM sales and report the total.",
		Expected: evaluation.Expected{ToolsCalled: []string{"run_sql"}, Contains: []string{"35"}},
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	reports, err := (evaluation.Runner{MaxConcurrency: 1}).Compare(ctx, variants, data)
	if err != nil {
		return err
	}
	if err := evaluation.WriteSummary(os.Stdout, reports); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "doolla-comparison-")
	if err != nil {
		return err
	}
	if err := saveReport(filepath.Join(dir, "comparison.csv"), reports, evaluation.WriteCSV); err != nil {
		return err
	}
	if err := saveReport(filepath.Join(dir, "comparison.html"), reports, evaluation.WriteHTML); err != nil {
		return err
	}
	fmt.Println("Reports:", dir)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
