package doolla

import (
	"context"
	"errors"
	"net/http"
)

func (s *Server) resolveUser(w http.ResponseWriter, r *http.Request) (User, error) {
	ctx, trace := s.agent.startSpan(r.Context(), "agent.user_resolution", "agent.user_resolution.duration", map[string]any{"has_context": true}, nil)
	user, err := s.resolve(w, r.WithContext(ctx))
	if stores, ok := s.agent.config.Memory.(*TenantMemoryStore); ok && err == nil {
		_, err = stores.ForUser(user)
	}
	trace.set("user_id", user.ID)
	trace.end(err)
	return user, err
}

func (s *Server) reserveReplay(ctx context.Context, user User, request *ChatRequest, transport string) (*replayEntry, bool, error) {
	entry, owner, err := s.replay.reserve(user, request)
	if o := s.agent.config.Observability; o != nil {
		result := "new"
		switch {
		case errors.Is(err, errReplayFull):
			result = "full"
		case errors.Is(err, errReplayBusy):
			result = "busy"
		case errors.Is(err, errRequestConflict):
			result = "conflict"
		case err != nil:
			result = "error"
		case !owner:
			result = "replay"
		}
		o.RecordMetric(ctx, "server.replay.reserve.count", 1, "count", map[string]string{"transport": transport, "result": result})
	}
	return entry, owner, err
}
