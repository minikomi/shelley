package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"shelley.exe.dev/modelproviders"
)

// handleListModelProviders lists saved connections. A server without a
// provider manager (predictable-only) simply has none; mutations stay disabled.
func (s *Server) handleListModelProviders(w http.ResponseWriter, r *http.Request) {
	if s.modelProviders == nil {
		writeJSON(w, http.StatusOK, []modelproviders.Connection{})
		return
	}
	connections, err := s.modelProviders.List(r.Context())
	if err != nil {
		s.internalError(w, "Failed to list model providers", err)
		return
	}
	writeJSON(w, http.StatusOK, connections)
}

func (s *Server) handleCreateModelProvider(w http.ResponseWriter, r *http.Request) {
	if s.modelProviders == nil {
		http.Error(w, "model provider management is disabled", http.StatusServiceUnavailable)
		return
	}
	var input modelproviders.Input
	if err := decodeModelProviderInput(w, r, &input); err != nil {
		http.Error(w, "invalid model provider request", http.StatusBadRequest)
		return
	}
	connection, err := s.modelProviders.Create(r.Context(), input)
	if err != nil {
		writeModelProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, connection)
}

func (s *Server) handleTestModelProvider(w http.ResponseWriter, r *http.Request) {
	if s.modelProviders == nil {
		http.Error(w, "model provider management is disabled", http.StatusServiceUnavailable)
		return
	}
	var input modelproviders.Input
	if err := decodeModelProviderInput(w, r, &input); err != nil {
		http.Error(w, "invalid model provider request", http.StatusBadRequest)
		return
	}
	result, err := s.modelProviders.Test(r.Context(), r.URL.Query().Get("provider_id"), input)
	if err != nil {
		writeModelProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpdateModelProvider(w http.ResponseWriter, r *http.Request) {
	if s.modelProviders == nil {
		http.Error(w, "model provider management is disabled", http.StatusServiceUnavailable)
		return
	}
	var input modelproviders.Input
	if err := decodeModelProviderInput(w, r, &input); err != nil {
		http.Error(w, "invalid model provider request", http.StatusBadRequest)
		return
	}
	connection, err := s.modelProviders.Update(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeModelProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, connection)
}

func (s *Server) handleDeleteModelProvider(w http.ResponseWriter, r *http.Request) {
	if s.modelProviders == nil {
		http.Error(w, "model provider management is disabled", http.StatusServiceUnavailable)
		return
	}
	if err := s.modelProviders.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeModelProviderError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRefreshModelProvider(w http.ResponseWriter, r *http.Request) {
	if s.modelProviders == nil {
		http.Error(w, "model provider management is disabled", http.StatusServiceUnavailable)
		return
	}
	connection, err := s.modelProviders.Refresh(r.Context(), r.PathValue("id"))
	if err != nil {
		writeModelProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, connection)
}

// handleRefreshModelProviders refreshes every saved connection and returns
// their states. Discovery failures are reported per connection (state=error),
// not as a request failure; only a failure to read the connections is a 500.
func (s *Server) handleRefreshModelProviders(w http.ResponseWriter, r *http.Request) {
	if s.modelProviders == nil {
		writeJSON(w, http.StatusOK, []modelproviders.Connection{})
		return
	}
	connections, err := s.modelProviders.RefreshAll(r.Context())
	if err != nil {
		s.logger.Error("Failed to refresh model providers", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to refresh model providers"})
		return
	}
	if connections == nil {
		connections = []modelproviders.Connection{}
	}
	writeJSON(w, http.StatusOK, connections)
}

func decodeModelProviderInput(w http.ResponseWriter, r *http.Request, input *modelproviders.Input) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeModelProviderError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, modelproviders.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, modelproviders.ErrDuplicate), errors.Is(err, modelproviders.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, modelproviders.ErrInvalid):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
