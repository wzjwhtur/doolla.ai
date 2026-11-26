//go:build cgo

package database_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/database"
)

func newMSSQLCatalogFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
ATTACH DATABASE ':memory:' AS sys;
CREATE TABLE sys.schemas (schema_id INTEGER, name TEXT);
CREATE TABLE sys.tables (object_id INTEGER, schema_id INTEGER, name TEXT);
CREATE TABLE sys.columns (object_id INTEGER, column_id INTEGER, name TEXT COLLATE NOCASE,
    system_type_id INTEGER DEFAULT 56, user_type_id INTEGER DEFAULT 56);
CREATE TABLE sys.types (user_type_id INTEGER, name TEXT);
INSERT INTO sys.types VALUES (56,'int'), (231,'nvarchar'), (1001,'MESTypeCode');
CREATE TABLE sys.indexes (object_id INTEGER, index_id INTEGER, is_primary_key INTEGER);
CREATE TABLE sys.index_columns (object_id INTEGER, index_id INTEGER, column_id INTEGER, key_ordinal INTEGER);
CREATE TABLE sys.foreign_keys (object_id INTEGER, name TEXT, parent_object_id INTEGER, referenced_object_id INTEGER);
CREATE TABLE sys.foreign_key_columns (constraint_object_id INTEGER, constraint_column_id INTEGER,
	parent_object_id INTEGER, parent_column_id INTEGER, referenced_object_id INTEGER, referenced_column_id INTEGER);
`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMSSQLSchemaScopesTablesAndOrdersCompositeKeys(t *testing.T) {
	// 使用本地目录夹具执行工具实际生成的 SELECT，覆盖同名表、复合键和范围外外键。
	db := newMSSQLCatalogFixture(t)
	_, err := db.Exec(`
INSERT INTO sys.schemas VALUES (1,'dbo'), (2,'ext');
INSERT INTO sys.tables VALUES (10,1,'Orders'), (20,2,'Orders'), (30,2,'Other');
INSERT INTO sys.columns (object_id,column_id,name) VALUES (10,1,'OrderId'), (10,2,'TenantId'), (20,1,'SourceTenant'), (20,2,'SourceOrder'), (30,1,'OtherId');
INSERT INTO sys.indexes VALUES (10,1,1), (10,2,0);
INSERT INTO sys.index_columns VALUES (10,1,1,2), (10,1,2,1), (10,2,1,1);
INSERT INTO sys.foreign_keys VALUES (50,'FK_source_order',20,10), (51,'FK_unrelated',30,30);
INSERT INTO sys.foreign_key_columns VALUES (50,2,20,2,10,1), (50,1,20,1,10,2), (51,1,30,1,30,1);
`)
	if err != nil {
		t.Fatal(err)
	}
	tool := database.MSSQLSchemaTool{DB: db}
	ctx := context.Background()
	input := json.RawMessage(`{"tables":[{"schema":"dbo","name":"Orders"},{"schema":"dbo","name":"Orders"},{"schema":"dbo","name":"x' OR 1=1 --"}]}`)
	result, err := tool.Run(ctx, doolla.User{}, input)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Scope                   string
		OtherVisibleForeignKeys *bool `json:"other_visible_foreign_keys_exist"`
		Tables                  []struct {
			Schema, Name, Status string
			Columns              []string
			PK                   []string `json:"primary_key"`
		}
		ForeignKeys []struct {
			Name        string
			From, To    struct{ Schema, Name string }
			ColumnPairs []struct{ From, To string } `json:"column_pairs"`
		} `json:"declared_foreign_keys"`
	}
	if err := json.Unmarshal([]byte(result.Content), &got); err != nil {
		t.Fatal(err)
	}
	if got.Scope == "" || len(got.Tables) != 2 || got.Tables[0].Schema != "dbo" || got.Tables[0].Name != "Orders" || got.Tables[0].Status != "inspected" {
		t.Fatalf("unexpected scope or tables: %s", result.Content)
	}
	if got.OtherVisibleForeignKeys == nil || !*got.OtherVisibleForeignKeys || strings.Contains(result.Content, "FK_unrelated") {
		t.Fatalf("expected only a flag for the unrelated visible key: %s", result.Content)
	}
	if !reflect.DeepEqual(got.Tables[0].Columns, []string{"OrderId", "TenantId"}) || !reflect.DeepEqual(got.Tables[0].PK, []string{"TenantId", "OrderId"}) {
		t.Fatalf("column or primary key order: %+v", got.Tables[0])
	}
	if got.Tables[1].Status != "missing_or_not_visible" || got.Tables[1].Name != "x' OR 1=1 --" {
		t.Fatalf("unmatched input: %+v", got.Tables[1])
	}
	if len(got.ForeignKeys) != 1 {
		t.Fatalf("expected one incident constraint: %+v", got.ForeignKeys)
	}
	fk := got.ForeignKeys[0]
	if fk.Name != "FK_source_order" || fk.From.Schema != "ext" || fk.To.Schema != "dbo" || fk.From.Name != "Orders" || fk.To.Name != "Orders" ||
		!reflect.DeepEqual(fk.ColumnPairs, []struct{ From, To string }{{"SourceTenant", "TenantId"}, {"SourceOrder", "OrderId"}}) {
		t.Fatalf("composite foreign key: %+v", fk)
	}
	for _, input := range []string{
		`{"tables":[{"schema":"ext","name":"Orders"}]}`,
		`{"tables":[{"schema":"dbo","name":"Orders"},{"schema":"ext","name":"Orders"}]}`,
	} {
		res, err := tool.Run(ctx, doolla.User{}, json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(res.Content), &got); err != nil || len(got.ForeignKeys) != 1 || got.ForeignKeys[0].Name != "FK_source_order" || len(got.ForeignKeys[0].ColumnPairs) != 2 {
			t.Fatalf("outgoing and both-endpoint lookup: %s, %v", res.Content, err)
		}
	}
	allKeys, err := tool.Run(ctx, doolla.User{}, json.RawMessage(`{"tables":[{"schema":"dbo","name":"Orders"},{"schema":"ext","name":"Other"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got.OtherVisibleForeignKeys = nil
	if err := json.Unmarshal([]byte(allKeys.Content), &got); err != nil || got.OtherVisibleForeignKeys == nil || *got.OtherVisibleForeignKeys || len(got.ForeignKeys) != 2 {
		t.Fatalf("both visible constraints touch this batch: %s, %v", allKeys.Content, err)
	}
}
