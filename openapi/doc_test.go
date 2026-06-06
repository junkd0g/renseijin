package openapi_test

import (
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/junkd0g/renseijin/openapi"
)

const minimalSpec = `openapi: 3.0.3
info:
  title: Mini
  version: "0.0.1"
paths:
  /ping:
    get:
      operationId: ping
      responses:
        "200":
          description: ok
`

func TestLoadFile_Success(t *testing.T) {
	d, err := openapi.LoadFile(filepath.Join("..", "examples", "petstore", "petstore.yaml"))
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.NotNil(t, d.T)
}

func TestLoadFile_NotFound(t *testing.T) {
	_, err := openapi.LoadFile(filepath.Join("testdata", "this-file-does-not-exist.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openapi: load")
}

func TestLoadData_Success(t *testing.T) {
	d, err := openapi.LoadData([]byte(minimalSpec))
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.NotNil(t, d.T)
}

func TestLoadData_BadSpec(t *testing.T) {
	_, err := openapi.LoadData([]byte("\x00not-a-spec\x01"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openapi: load from data")
}

func TestFromT_Wraps(t *testing.T) {
	in := &openapi3.T{}
	d := openapi.FromT(in)
	require.NotNil(t, d)
	assert.Same(t, in, d.T)
}
