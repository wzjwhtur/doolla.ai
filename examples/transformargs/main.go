// Command transformargs scopes a tool call to the authenticated user's organization.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	doolla "doolla.ai/sdk"
)

type countOrders struct{ db *sql.DB }

func (countOrders) Spec() doolla.ToolSpec {
	return doolla.ToolSpec{Name: "count_orders", Description: "Count orders for the current organization.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"organization": map[string]string{"type": "string"},
		}, "additionalProperties": false}}
}

func (t countOrders) Run(ctx context.Context, _ doolla.User, raw json.RawMessage) (doolla.ToolResult, error) {
	var args struct{ Organization string }
	if err := json.Unmarshal(raw, &args); err != nil {
		return doolla.ToolResult{}, err
	}
	var count int
	err := t.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE organization = ?", args.Organization).Scan(&count)
	return doolla.ToolResult{Content: fmt.Sprint(count)}, err
}

func bindOrganization(_ context.Context, user doolla.User, tool doolla.Tool, args json.RawMessage) (json.RawMessage, error) {
	if tool.Spec().Name != "count_orders" {
		return args, nil
	}
	org, _ := user.Metadata["organization"].(string)
	if org == "" {
		return nil, errors.New("missing organization")
	}
	return json.Marshal(map[string]string{"organization": org})
}

func run() error {
	db, err := doolla.OpenWritableSQLite(":memory:")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE orders(organization TEXT); INSERT INTO orders VALUES ('north'), ('north'), ('south')"); err != nil {
		return err
	}
	registry := doolla.ToolRegistry{TransformArgs: bindOrganization}
	if err := registry.Register(doolla.Registration{Tool: countOrders{db}}); err != nil {
		return err
	}
	call := doolla.ToolCall{ID: "demo", Type: "function", Function: doolla.FunctionCall{Name: "count_orders", Arguments: `{"organization":"south"}`}}
	// In an application the resolver populates this metadata from the verified login.
	user := doolla.User{ID: "reader", Metadata: map[string]any{"organization": "north"}}
	result, err := registry.Execute(context.Background(), user, call)
	if err != nil {
		return err
	}
	fmt.Println("north orders:", result.Content)
	_, err = registry.Execute(context.Background(), doolla.User{ID: "unassigned"}, call)
	if err == nil {
		return errors.New("expected a rejection for the unassigned user")
	}
	fmt.Println("unassigned user:", err)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
