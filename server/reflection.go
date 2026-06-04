package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// exeReflectionHTTPClient is the HTTP client used to query the exe.dev
// reflection API. It is a package var so tests can inject a transport.
var exeReflectionHTTPClient = http.DefaultClient

// reflectionIntegration is one entry in the reflection /integrations response.
type reflectionIntegration struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// exeDevHasNotifyIntegration reports whether this VM has a "notify" integration
// available (i.e. push notifications to the owner's devices are possible). It
// queries the default "reflection" integration. Returns false if the
// integration is disabled/detached or on any network error.
func exeDevHasNotifyIntegration() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://reflection.int.exe.xyz/integrations", nil)
	if err != nil {
		return false
	}
	resp, err := exeReflectionHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Integrations []reflectionIntegration `json:"integrations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	for _, ig := range body.Integrations {
		if ig.Type == "notify" {
			return true
		}
	}
	return false
}
