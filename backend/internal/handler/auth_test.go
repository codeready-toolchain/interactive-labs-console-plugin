package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetupHandler_RejectsMissingAuthorization(t *testing.T) {
	rw := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1alpha1/labs/"+goodCatalogRef()+"/setup", nil)
	newSetupRouter(t).ServeHTTP(rw, req)

	require.Equal(t, http.StatusUnauthorized, rw.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rw.Body.Bytes(), &body))
	require.Equal(t, "unauthorized", body["type"])
}
