package api

import (
	"encoding/json"
	"net/http"
)

// triggerDelivery used to live here — it called deliver-purchase with
// the project service-role key. As of Phase 2b the redeliver path
// goes through retry-purchase-delivery (a user-JWT EF) directly from
// purchases.go::handleRedeliver, so we no longer need a service-role
// helper. If a future caller needs server-context delivery
// re-trigger, the right move is a new HMAC-authenticated EF, not
// re-introducing service-role here.

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"message": message,
	})
}
