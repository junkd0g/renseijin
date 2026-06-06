package renseijin

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewConfig_Defaults(t *testing.T) {
	cfg := newConfig(nil)
	assert.Same(t, http.DefaultClient, cfg.httpClient)
	assert.Empty(t, cfg.baseURL)
	assert.Empty(t, cfg.namePrefix)
}

func TestWithHTTPClient_NilFallsBackToDefault(t *testing.T) {
	cfg := newConfig([]Option{WithHTTPClient(nil)})
	assert.Same(t, http.DefaultClient, cfg.httpClient)
}

func TestWithHTTPClient_Custom(t *testing.T) {
	custom := &http.Client{}
	cfg := newConfig([]Option{WithHTTPClient(custom)})
	assert.Same(t, custom, cfg.httpClient)
}

func TestWithBaseURL_Sets(t *testing.T) {
	cfg := newConfig([]Option{WithBaseURL("https://sandbox.example/v2")})
	assert.Equal(t, "https://sandbox.example/v2", cfg.baseURL)
}

func TestWithToolNamePrefix_Sets(t *testing.T) {
	cfg := newConfig([]Option{WithToolNamePrefix("pfx_")})
	assert.Equal(t, "pfx_", cfg.namePrefix)
}

func TestOptions_LastWriteWins(t *testing.T) {
	cfg := newConfig([]Option{
		WithBaseURL("https://first.example"),
		WithBaseURL("https://second.example"),
	})
	assert.Equal(t, "https://second.example", cfg.baseURL)
}
