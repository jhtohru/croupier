package httpapi

import "net/http"

type healthResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// healthLive never checks a dependency — it answers "is this process alive
// and able to handle a request at all," which a readiness probe (below)
// deliberately doesn't answer on its own.
func (h *handler) healthLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// healthReady checks Postgres only for now — Fase 8 extends whatever
// cmd/croupier passes as Deps.Ready to also check SQS, with no change
// needed here.
func (h *handler) healthReady(w http.ResponseWriter, r *http.Request) {
	if h.deps.Ready == nil {
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
		return
	}
	if err := h.deps.Ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable", Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}
