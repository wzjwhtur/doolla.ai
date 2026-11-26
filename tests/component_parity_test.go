package tests

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"doolla.ai/sdk"
)

// 基准文件由 PyPI 发布的上游 Python 包执行相同的五个操作生成。
// 每次运行的时间戳都不同，其余字段（包括值为 null 的字段）按原样比较。
func TestPython202ComponentChanges(t *testing.T) {
	var m doolla.ComponentManager
	changes := make([]doolla.ComponentChange, 0, 5)
	add := func(delta doolla.ComponentChange, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		changes = append(changes, delta)
	}
	card := func(id string, data map[string]any) doolla.Component {
		return doolla.Component{Type: "card", ID: id, Data: data}
	}
	add(m.Emit(card("first", map[string]any{"name": "one"})))
	add(m.Update("first", map[string]any{"visible": false}))
	add(m.Emit(card("first", map[string]any{"name": "two"})))
	add(m.Replace("first", card("second", nil)))
	add(m.Remove("second"))
	raw, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	var got, want []map[string]any
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, delta := range got {
		delete(delta, "timestamp")
		if c, ok := delta["component"].(map[string]any); ok {
			delete(c, "timestamp")
		}
	}
	raw, err = os.ReadFile("testdata/component_changes_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestComponentTreeReplacementAndIsolation(t *testing.T) {
	var m doolla.ComponentManager
	root := doolla.Component{ID: "root", Type: "card"}
	rows := []map[string]any{{"value": int64(123)}}
	child := doolla.Component{ID: "child", Type: "dataframe", Data: map[string]any{"data": rows}}
	if _, err := m.Emit(root); err != nil {
		t.Fatal(err)
	}
	ix := 0
	pos := doolla.ComponentPosition{Index: &ix, AnchorID: "root", Relation: "inside"}
	d, err := m.Add(child, &pos)
	if err != nil {
		t.Fatal(err)
	}
	rows[0]["value"] = false
	*d.Position.Index = 9
	d.Component.Data["data"].([]map[string]any)[0]["value"] = nil
	c, _ := m.Get("child")
	if c.Data["data"].([]map[string]any)[0]["value"] != int64(123) || *m.ChangesSince(time.Time{})[1].Position.Index != 0 {
		t.Fatal("stored change mutated")
	}
	if _, err = m.Replace("root", child); err == nil {
		t.Fatal("overwrote unrelated id")
	}
	root.ID = "renamed"
	if _, err = m.Replace("root", root); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Apply(doolla.Component{ID: "renamed", Lifecycle: "remove"}); err != nil {
		t.Fatal(err)
	}
	if len(m.Snapshot()) != 0 {
		t.Fatal("left a descendant")
	}
}
