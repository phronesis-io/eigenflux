package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCreateOrderPreparationOrderPresence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		order   string
		upload  bool
		wantErr bool
	}{
		{name: "omitted", upload: true},
		{name: "null", order: `null`, upload: true},
		{name: "empty", order: `{}`, upload: true},
		{name: "zero", order: `{"order_id":0,"state":"","version":0}`, upload: true},
		{name: "finalized", order: `{"order_id":51}`},
		{name: "mismatched", order: `{"order_id":52}`, wantErr: true},
		{name: "negative", order: `{"order_id":-1}`, wantErr: true},
		{name: "malformed", order: `{"order_id":"invalid"}`, wantErr: true},
	} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resume=%t", tc.name, resume), func(t *testing.T) {
				var calls []string
				var server *httptest.Server
				content := "  材料\n"
				server = materialTestServer(t, func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, r.Method+" "+r.URL.Path)
					switch r.URL.Path {
					case "/api/v1/order-preparations", "/api/v1/order-preparations/51":
						order := ""
						if tc.order != "" {
							order = `,"order":` + tc.order
						}
						fmt.Fprintf(w, `{"code":0,"data":{"preparation_id":51%s}}`, order)
					case "/api/v1/order-preparations/51/uploads":
						fmt.Fprintf(w, `{"code":0,"data":{"grant":{"object_id":71,"method":"PUT","url":%q}}}`, server.URL+"/object")
					case "/object":
						body, _ := io.ReadAll(r.Body)
						if string(body) != content {
							t.Errorf("material bytes changed: %q", body)
						}
						w.WriteHeader(http.StatusOK)
					case "/api/v1/order-preparations/51/uploads/confirm":
						fmt.Fprint(w, `{"code":0,"data":{}}`)
					case "/api/v1/orders":
						fmt.Fprint(w, `{"code":0,"data":{"order":{"order_id":51,"state":"pending_payment"}}}`)
					default:
						t.Errorf("unexpected request: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				})
				materialFlag(t, orderCreateCmd, "buyer-input", content)
				materialFlag(t, orderCreateCmd, "idempotency-key", "preparation-order-presence")
				want := []string{"POST /api/v1/order-preparations"}
				if resume {
					materialFlag(t, orderCreateCmd, "preparation-id", "51")
					want[0] = "GET /api/v1/order-preparations/51"
				}
				err := orderCreateCmd.RunE(orderCreateCmd, []string{"77"})
				if tc.wantErr {
					if err == nil {
						t.Error("invalid order must fail before upload or finalization")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if tc.upload {
						want = append(want, "POST /api/v1/order-preparations/51/uploads", "PUT /object", "POST /api/v1/order-preparations/51/uploads/confirm")
					}
					want = append(want, "POST /api/v1/orders")
				}
				if !reflect.DeepEqual(want, calls) {
					t.Errorf("request sequence:\nwant %s\ngot  %s", strings.Join(want, " -> "), strings.Join(calls, " -> "))
				}
			})
		}
	}
}
