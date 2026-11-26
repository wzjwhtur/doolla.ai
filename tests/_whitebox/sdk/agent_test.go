package doolla

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type replyFunc func(context.Context, []Message, []ToolSpec) (Message, error)

func (f replyFunc) Reply(c context.Context, m []Message, t []ToolSpec) (Message, error) {
	return f(c, m, t)
}

func sampleDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sales.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("create table sales (region text, value integer); insert into sales values ('North',2),('South',3),('North',5)"); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestQueryAndVisualization(t *testing.T) {
	ctx := context.Background()
	path := sampleDatabase(t)
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u := User{ID: "user"}
	files := Artifacts{t.TempDir()}
	sqlTool := SQLTool{DB: db, Artifacts: &files}
	result, err := sqlTool.Run(ctx, u, []byte(`{"sql":"WITH s AS (SELECT region,sum(value) AS total FROM sales GROUP BY region) SELECT * FROM s ORDER BY region"}`))
	if err != nil {
		t.Fatal(err)
	}
	records := result.Component.Data["data"].([]map[string]any)
	if len(records) != 2 || records[0]["total"] != int64(7) || records[1]["total"] != int64(3) {
		t.Fatal(records)
	}
	_, name, ok := strings.Cut(result.Content, "Results saved to file: ")
	if !ok {
		t.Fatal(result)
	}
	args, _ := json.Marshal(map[string]string{"filename": name})
	plot, err := (VisualizationTool{files}).Run(ctx, u, args)
	if err != nil {
		t.Fatal(err)
	}
	traces := plot.Component.Data["data"].([]map[string]any)
	if traces[0]["type"] != "bar" || !slices.Equal(traces[0]["y"].([]float64), []float64{7, 3}) {
		t.Fatal(traces)
	}
	if _, err = (VisualizationTool{files}).Run(ctx, User{ID: "guest"}, args); err == nil {
		t.Fatal("cross-user result file")
	}
	if _, err = sqlTool.Run(ctx, u, []byte(`{"sql":"UPDATE sales SET value = 99"}`)); err == nil {
		t.Fatal("unexpected write on read-only database")
	}
	rw, err := OpenWritableSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	modified, err := (SQLTool{DB: rw}).Run(ctx, u, []byte(`{"sql":"WITH v AS (SELECT 10 AS increment) UPDATE sales SET value=value+(SELECT increment FROM v)"}`))
	if err != nil || !strings.Contains(modified.Content, "3 row(s) affected") {
		t.Fatalf("%+v %v", modified, err)
	}
}
func TestAgentMemoryPermissionsHistory(t *testing.T) {
	mem := NewLocalMemory(10)
	if err := mem.Save(t.Context(), MemoryEntry{Kind: "text", Content: "weekly order quantity", ID: "k"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := 0
	model := replyFunc(func(ctx context.Context, msg []Message, tools []ToolSpec) (Message, error) {
		mu.Lock()
		defer mu.Unlock()
		seen++
		for _, s := range tools {
			if s.Name == "save_text_memory" {
				t.Fatal("admin tool advertised")
			}
		}
		switch seen {
		case 1:
			if !strings.Contains(msg[0].Content, "weekly order quantity") {
				t.Fatal(msg[0])
			}
			return Message{ToolCalls: []ToolCall{{"x", "function", FunctionCall{"save_text_memory", `{"content":"bad"}`}}}}, nil
		case 2:
			if msg[len(msg)-1].Content != "Tool unavailable" {
				t.Fatal(msg)
			}
		case 3:
			if !strings.Contains(fmt.Sprint(msg), "weekly order quantity") {
				t.Fatal("lost history")
			}
		case 4:
			if len(msg) != 2 {
				t.Fatal("cross-user history", msg)
			}
		}
		return Message{Content: "done"}, nil
	})
	a, err := NewConfiguredAgent(model, AgentConfig{Memory: mem}, MemoryTools(mem)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []struct{ user, q string }{{"me", "weekly order quantity"}, {"me", "and last quarter?"}, {"you", "hello"}} {
		if err := a.Chat(t.Context(), User{ID: turn.user, Groups: []string{"user"}}, "shared", turn.q, func(Component) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := mem.Recent(t.Context(), "text", 10)
	if len(data) != 1 {
		t.Fatal(data)
	}
}
func TestConversationFileImport(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "conv", "messages")
	if err := os.MkdirAll(src, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(p, data string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "conv", "metadata.json"), `{"id":"conv","user":{"id":"u"},"created_at":"2020-01-01T01:01:01","updated_at":"2020-01-01T01:01:01","metadata":{"locale":"zh"}}`)
	write(filepath.Join(src, "1.json"), `{"role":"assistant","content":"","timestamp":"2020-01-01T01:01:03","metadata":{"source":"demo"},"tool_calls":[{"id":"call_1","name":"run_sql","arguments":{"sql":"SELECT 7"}}]}`)
	store := FileConversations{dir}
	ctx := t.Context()
	u := User{ID: "u"}
	msg, err := store.Load(ctx, u, "conv")
	if err != nil || len(msg) != 1 || msg[0].ToolCalls[0].Function.Name != "run_sql" {
		t.Fatal(msg, err)
	}
	if msg, err := store.Load(ctx, User{ID: "other"}, "conv"); err != nil || len(msg) != 0 {
		t.Fatal(msg, err)
	}
	msg = append(msg, Message{Role: "assistant", Content: "seven", ModelState: json.RawMessage(`{"signature":"test"}`)})
	if err := store.Save(ctx, u, "conv", msg); err != nil {
		t.Fatal(err)
	}
	items, err := store.List(ctx, u, 10, 0)
	if err != nil || len(items) != 1 || len(items[0].Messages) != 2 || items[0].Created.Year() != 2020 || string(items[0].Metadata) != `{"locale":"zh"}` || string(items[0].Messages[0].Metadata) != `{"source":"demo"}` || items[0].Messages[0].Timestamp.Second() != 3 {
		t.Fatal(items, err)
	}
	exported, err := store.ExportPython(ctx, u, "conv", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	imported, err := (FileConversations{exported}).Load(ctx, u, "conv")
	if err != nil || len(imported) != 2 || imported[0].ToolCalls[0].Function.Arguments != `{"sql":"SELECT 7"}` || imported[0].Timestamp.Second() != 3 || imported[1].Content != "seven" {
		t.Fatal(imported, err)
	}
	if ok, err := store.Delete(ctx, u, "conv"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if msg, err := store.Load(ctx, u, "conv"); err != nil || len(msg) != 0 {
		t.Fatal(msg, err)
	}
}

func TestChatTransports(t *testing.T) {
	path := sampleDatabase(t)
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := replyFunc(func(ctx context.Context, msg []Message, tools []ToolSpec) (Message, error) {
		last := msg[len(msg)-1]
		if last.Role == "tool" {
			if !strings.Contains(last.Content, "10") {
				return Message{}, errors.New("incorrect SQL result")
			}
			return Message{Content: "Total is 10."}, nil
		}
		return Message{ToolCalls: []ToolCall{{"id", "function", FunctionCall{"run_sql", `{"sql":"SELECT SUM(value) AS total FROM sales"}`}}}}, nil
	})
	a, err := NewAgent(model, "sql", Registration{Tool: SQLTool{DB: db, Artifacts: Artifacts{t.TempDir()}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(a, nil))
	defer srv.Close()
	client := srv.Client()
	client.Jar, _ = cookiejar.New(nil)
	const body = `{"message":"total?","conversation_id":"abc","request_id":"req"}`
	resp, err := client.Post(srv.URL+"/api/doolla/v2/chat_poll", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var poll struct {
		Chunks []Chunk
		Total  int `json:"total_chunks"`
	}
	err = json.NewDecoder(resp.Body).Decode(&poll)
	resp.Body.Close()
	if err != nil || poll.Total != len(poll.Chunks) || poll.Total < 3 ||
		poll.Chunks[poll.Total-1].Rich["type"] != "turn_outcome" ||
		poll.Chunks[poll.Total-2].Rich["data"].(map[string]any)["content"] != "Total is 10." {
		t.Fatal(poll, err)
	}
	resp, err = client.Post(srv.URL+"/api/doolla/v2/chat_sse", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || !strings.HasSuffix(string(data), "data: [DONE]\n\n") || !strings.Contains(string(data), `"type":"dataframe"`) {
		t.Fatal(string(data), err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/doolla/v2/chat_websocket", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err := ws.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
		t.Fatal(err)
	}
	var types []string
	for {
		var msg struct {
			Rich      struct{ Type string }
			RequestID string `json:"request_id"`
		}
		if err := wsjson.Read(ctx, ws, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.RequestID != "req" {
			t.Fatal(msg)
		}
		types = append(types, msg.Rich.Type)
		if msg.Rich.Type == "completion" {
			break
		}
	}
	if !slices.Contains(types, "dataframe") || !slices.Contains(types, "text") {
		t.Fatal(types)
	}
	resp, err = client.Post(srv.URL+"/api/doolla/v2/chat_poll", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal(resp.Status)
	}
}

func TestBrowserFailureAndCrossOriginSession(t *testing.T) {
	const privateDetail = "internal provider diagnostic"
	const origin = "https://portal.example"
	a, err := NewAgent(replyFunc(func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{}, errors.New(privateDetail)
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(NewServerWithOptions(a, nil, ServerOptions{AllowedOrigins: []string{origin}}))
	defer srv.Close()
	client := srv.Client()
	client.Jar, _ = cookiejar.New(nil)
	for _, mode := range []string{"poll", "sse"} {
		req, err := http.NewRequest("POST", srv.URL+"/api/doolla/v2/chat_"+mode, strings.NewReader(`{"message":"hello"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != origin {
			t.Fatal(resp.Status, resp.Header)
		}
		if mode == "poll" {
			cs := resp.Cookies()
			if len(cs) != 1 || cs[0].SameSite != http.SameSiteNoneMode || !cs[0].Secure {
				t.Fatal(cs)
			}
		} else if len(resp.Cookies()) != 0 {
			t.Fatal("existing session was replaced")
		}
		data := string(body)
		if strings.Contains(data, privateDetail) || !strings.Contains(data, `"type":"chat_input_update"`) || !strings.Contains(data, `"status":"error"`) {
			t.Fatal(data)
		}
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/doolla/v2/chat_poll", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Origin", "https://untrusted.example")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.Status)
	}
}

func TestToolHistorySurvivesDeliveryFailure(t *testing.T) {
	db, err := OpenWritableSQLite(sampleDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	history := NewMemoryConversations()
	calls := []ToolCall{
		{ID: "first", Type: "function", Function: FunctionCall{"run_sql", `{"sql":"update sales set value=value+1"}`}},
		{ID: "second", Type: "function", Function: FunctionCall{"run_sql", `{"sql":"update sales set value=value+1"}`}},
	}
	model := replyFunc(func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{ToolCalls: calls}, nil
	})
	agent, err := NewConfiguredAgent(model, AgentConfig{Conversations: history}, Registration{Tool: SQLTool{DB: db}})
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: "u"}
	err = agent.Chat(t.Context(), user, "c", "increment", func(c Component) error {
		if c.Type == "notification" {
			return io.ErrClosedPipe
		}
		return nil
	})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	messages, err := history.Load(t.Context(), user, "c")
	if err != nil || len(messages) != 4 || !strings.Contains(messages[2].Content, "3 row(s) affected") || messages[3].ToolCallID != "second" || messages[3].Content != "Tool was not executed" {
		t.Fatal(messages, err)
	}
	var sum int
	if err := db.QueryRow("select sum(value) from sales").Scan(&sum); err != nil || sum != 13 {
		t.Fatal(sum, err)
	}
	calls[1].ID = calls[0].ID
	if err := agent.Chat(t.Context(), user, "another", "increment", func(Component) error { return nil }); err == nil {
		t.Fatal("accepted duplicate ID")
	}
	if err := db.QueryRow("select sum(value) from sales").Scan(&sum); err != nil || sum != 13 {
		t.Fatal(sum, err)
	}
}

func TestExplainUpdateReturnsQueryPlan(t *testing.T) {
	db, err := OpenWritableSQLite(sampleDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := (DatabaseRunner{db}).RunSQL(t.Context(), User{}, "EXPLAIN QUERY PLAN UPDATE sales SET value=1")
	if err != nil || !slices.Contains(got.Columns, "detail") || len(got.Rows) == 0 {
		t.Fatal(got, err)
	}
}
