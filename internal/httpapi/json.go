// Package httpapi implements the inbound HTTP adapter: routes, request/
// response DTOs, and error-to-status mapping. It depends only on
// internal/app (the use-case layer) and the domain packages needed to shape
// JSON — never on internal/postgres, mirroring the same inbound/outbound
// separation the project already keeps between internal/app and
// internal/postgres.
package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// timeFormat is used for every timestamp field in every response DTO —
// RFC3339Nano keeps sub-second precision, which matters for fields like
// wallet ledger entries that can be created microseconds apart.
const timeFormat = time.RFC3339Nano

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	// Encoding failures here would mean a bug in a DTO's shape, not a
	// request problem — there's nothing meaningful left to tell the client
	// at this point since the status/headers are already written.
	_ = json.NewEncoder(w).Encode(body)
}

// decodeJSON rejects unknown fields, catching client/server contract drift
// early instead of silently ignoring typos or stale fields.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
