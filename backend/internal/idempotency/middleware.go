package idempotency

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/kwabsntim/rok-store/internal/auth"
)

const keyTTL = 24 * time.Hour

// Middleware returns an HTTP middleware that enforces idempotency for POST/PUT
// requests that include an Idempotency-Key header.
// The /api/checkout route requires the header; all other routes treat it as optional.
func Middleware(repo *Repository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only apply to state-mutating methods.
			if r.Method != http.MethodPost && r.Method != http.MethodPut {
				next.ServeHTTP(w, r)
				return
			}

			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				// Checkout requires the header.
				if r.URL.Path == "/api/checkout" {
					http.Error(w, `{"error":"Idempotency-Key header is required"}`, http.StatusBadRequest)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			// Determine caller identity.
			userID, guestEmail := callerIdentity(r)

			// Check for an existing record.
			existing, err := repo.Get(r.Context(), key, userID, guestEmail)
			if err != nil && err != sql.ErrNoRows {
				log.Printf("[idempotency] db error on GET: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			if existing != nil {
				switch existing.Status {
				case "started":
					http.Error(w, `{"error":"An identical request is currently being processed"}`, http.StatusConflict)
					return
				case "completed":
					if existing.ResponseCode.Valid {
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("X-Idempotent-Replayed", "true")
						w.WriteHeader(int(existing.ResponseCode.Int32))
						w.Write([]byte(existing.ResponseBody.String)) //nolint:errcheck
						return
					}
				}
			}

			// Lock the key before processing.
			if err = repo.Lock(r.Context(), key, r.URL.Path, userID, guestEmail, keyTTL); err != nil {
				// A race between two simultaneous requests; the second one loses.
				http.Error(w, `{"error":"Request is already being processed"}`, http.StatusConflict)
				return
			}

			// Buffer the request body so both the idempotency layer and the
			// underlying handler can read it.
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				repo.Fail(r.Context(), key) //nolint:errcheck
				http.Error(w, "failed to read request body", http.StatusInternalServerError)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			// Intercept the response.
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, r)

			statusCode := rec.Code
			responseBody := rec.Body.String()

			// Flush intercepted response to the real writer.
			for k, v := range rec.Result().Header {
				w.Header()[k] = v
			}
			w.WriteHeader(statusCode)
			w.Write([]byte(responseBody)) //nolint:errcheck

			// Persist result.
			if err = repo.Complete(r.Context(), key, statusCode, responseBody); err != nil {
				log.Printf("[idempotency] failed to mark key completed: %v", err)
			}
		})
	}
}

// callerIdentity extracts the authenticated user ID from context, or tries to
// parse a guest email from the JSON request body for guest checkout flows.
func callerIdentity(r *http.Request) (userID sql.NullString, guestEmail sql.NullString) {
	if u := auth.GetUserFromContext(r); u != nil {
		userID = sql.NullString{String: u.ID, Valid: true}
		return
	}

	// Attempt to read email from body for guest requests.
	// We must not consume the body — peek then restore.
	if r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			var payload struct {
				Email string `json:"email"`
			}
			if json.Unmarshal(bodyBytes, &payload) == nil && payload.Email != "" {
				guestEmail = sql.NullString{String: payload.Email, Valid: true}
			}
		}
	}
	return
}
