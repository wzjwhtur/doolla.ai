package doolla

import "net/http"

func (s *Server) resolveUser(w http.ResponseWriter, r *http.Request) (User, error) {
	ctx, trace := s.agent.startSpan(r.Context(), "agent.user_resolution", "agent.user_resolution.duration", map[string]any{"has_context": true}, nil)
	user, err := s.resolve(w, r.WithContext(ctx))
	trace.set("user_id", user.ID)
	trace.end(err)
	return user, err
}
