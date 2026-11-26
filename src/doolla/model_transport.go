package doolla

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type wireObject = map[string]any

func (m *remoteModel) post(ctx context.Context, endpoint string, headers http.Header, body wireObject) (io.ReadCloser, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header = headers
	req.Header.Set("Content-Type", "application/json")
	client := m.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		var detail struct{ Error json.RawMessage }
		_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&detail)
		return nil, fmt.Errorf("%s returned HTTP %d: %s", m.provider, resp.StatusCode, errorDetail(detail.Error))
	}
	return resp.Body, nil
}

func hasModelError(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
func errorDetail(raw json.RawMessage) string {
	var detail struct{ Message string }
	if json.Unmarshal(raw, &detail) == nil && detail.Message != "" {
		return detail.Message
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return text
	}
	return "model request failed"
}

func bearerHeaders(key string) http.Header {
	h := http.Header{}
	if key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	return h
}

func notifyText(onText func(string) error, text string) error {
	if onText != nil && text != "" {
		return onText(text)
	}
	return nil
}

// readSSE requires the protocol's terminal event before accepting a response.
// EOF in a broken stream must never turn partial tool arguments into a call.
func readSSE(r io.Reader, handle func([]byte) (bool, error)) error {
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 8192), 16<<20)
	var data []string
	dispatch := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		event := strings.Join(data, "\n")
		data = nil
		return handle([]byte(event))
	}
	for scan.Scan() {
		line := scan.Text()
		if line == "" {
			if done, err := dispatch(); done || err != nil {
				return err
			}
		} else if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	if done, err := dispatch(); done || err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func wireCall(id, name, args string) ToolCall {
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: name, Arguments: args}}
}

func jsonArgs(raw string) (json.RawMessage, error) {
	if raw == "" {
		raw = "{}"
	}
	if !json.Valid([]byte(raw)) || !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	return json.RawMessage(raw), nil
}

// Models may return a scalar, an array or malformed JSON where a tool's
// argument object is expected. Preserve the value using the keys expected by the Python API.
func modelArguments(raw string) json.RawMessage {
	if raw == "" {
		return json.RawMessage("{}")
	}
	if !json.Valid([]byte(raw)) {
		fallback, _ := json.Marshal(map[string]string{"_raw": raw})
		return fallback
	}
	raw = strings.TrimSpace(raw)
	if raw[0] == '{' {
		return json.RawMessage(raw)
	}
	return json.RawMessage(`{"args":` + raw + "}")
}

type savedModelState struct {
	Provider string            `json:"provider"`
	Parts    []json.RawMessage `json:"parts"`
}

func saveParts(msg *Message, provider string, parts []json.RawMessage) error {
	state, err := json.Marshal(savedModelState{provider, parts})
	if err == nil {
		msg.ModelState = state
	}
	return err
}

func savedParts(msg Message, provider string) []json.RawMessage {
	var state savedModelState
	if len(msg.ModelState) > 0 && json.Unmarshal(msg.ModelState, &state) == nil && state.Provider == provider {
		return state.Parts
	}
	return nil
}

// A provider can supply raw arguments or encode them inside a JSON string.
func encodedModelArguments(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage("{}"), nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		if value == "" {
			return json.RawMessage(`{"_raw":""}`), nil
		}
		return modelArguments(value), nil
	}
	return modelArguments(string(raw)), nil
}
