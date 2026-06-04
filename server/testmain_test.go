package server

import (
	"errors"
	"net/http"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	exeDevDefaultPortHTTPClient = &http.Client{Transport: defaultPortTestTransport{}}
	// Default to a failing reflection client so server tests never make real
	// network calls. Tests that exercise reflection override this explicitly.
	exeReflectionHTTPClient = &http.Client{Transport: defaultPortTestTransport{}}
	// Isolate from any host-wide git config (e.g. core.hooksPath that
	// enforces commit-message policy on agent-driven commits) so tests
	// that invoke `git commit` behave deterministically.
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	os.Exit(m.Run())
}

type defaultPortTestTransport struct{}

func (defaultPortTestTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("reflection disabled in tests")
}
