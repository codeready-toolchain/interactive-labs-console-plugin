package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetupHandler_RejectsMissingAuthorization(t *testing.T) {
	rw := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup", strings.NewReader(`{"namespaceBindings":{"releaseNamespace":"labs"}}`))
	req.Header.Set("Content-Type", "application/json")
	newRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusUnauthorized, rw.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
	require.Equal(t, "unauthorized", body["type"])
}
