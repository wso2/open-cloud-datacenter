package main_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/wso2/rca/pkg/plugin"
)

type mockCallResourceResponseSender struct {
	response *backend.CallResourceResponse
}

func (s *mockCallResourceResponseSender) Send(response *backend.CallResourceResponse) error {
	s.response = response
	return nil
}

func TestCallResource(t *testing.T) {
	inst, err := plugin.NewApp(context.Background(), backend.AppInstanceSettings{})
	if err != nil {
		t.Fatalf("new app: %s", err)
	}
	app, ok := inst.(*plugin.App)
	if !ok {
		t.Fatalf("inst must be of type *plugin.App")
	}

	for _, tc := range []struct {
		name      string
		method    string
		path      string
		body      []byte
		expStatus int
		expBody   []byte
	}{
		{
			name:      "analyze rejects an empty request",
			method:    http.MethodPost,
			path:      "rca/analyze",
			body:      []byte(`{}`),
			expStatus: http.StatusBadRequest,
		},
		{
			name:      "get non existing handler 404",
			method:    http.MethodGet,
			path:      "not_found",
			expStatus: http.StatusNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response mockCallResourceResponseSender
			err = app.CallResource(context.Background(), &backend.CallResourceRequest{
				Method: tc.method,
				Path:   tc.path,
				Body:   tc.body,
			}, &response)
			if err != nil {
				t.Fatalf("CallResource error: %s", err)
			}
			if response.response == nil {
				t.Fatal("no response received from CallResource")
			}
			if tc.expStatus != response.response.Status {
				t.Errorf("response status should be %d, got %d", tc.expStatus, response.response.Status)
			}
			if len(tc.expBody) > 0 {
				if body := bytes.TrimSpace(response.response.Body); !bytes.Equal(body, tc.expBody) {
					t.Errorf("response body should be %s, got %s", tc.expBody, body)
				}
			}
		})
	}
}
