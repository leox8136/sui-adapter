package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	rpcv2 "sui-adapter/internal/suiv2"
)

type recordedFunctions struct {
	rpcv2.MovePackageServiceClient
	responses map[string]json.RawMessage
}

func (f *recordedFunctions) GetFunction(_ context.Context, r *rpcv2.GetFunctionRequest, _ ...grpc.CallOption) (*rpcv2.GetFunctionResponse, error) {
	key := r.GetPackageId() + "::" + r.GetModuleName() + "::" + r.GetName()
	data, ok := f.responses[key]
	if !ok {
		return nil, fmt.Errorf("unrecorded function %s", key)
	}
	response := &rpcv2.GetFunctionResponse{}
	if err := protojson.Unmarshal(data, response); err != nil {
		return nil, err
	}
	return response, nil
}

// Fixtures are read-only responses captured from the official mainnet gRPC
// endpoint on 2026-09-30. No network access or real execution occurs in this test.
func TestMainnetTransactionCompatibilityRegression(t *testing.T) {
	for _, digest := range []string{"DYMKT6gAhBkT4W1tpsUm2dgYC1nHHYqb8NxxYokyfNE7", "CPyHbe9cUvK4x8bSqZHJ4LnqDXaf6371yWVnqXt9122U", "84CiBJfK18QQZPF1akkXTwnZFS32jZdLtzuBVim5fgxn"} {
		for _, method := range []string{"sui_getTransactionBlock", "sui_dryRunTransactionBlock", "sui_executeTransactionBlock"} {
			t.Run(digest+"/"+method, func(t *testing.T) {
				data, err := os.ReadFile("testdata/" + digest + ".json")
				if err != nil {
					t.Fatal(err)
				}
				var fixture struct {
					Transaction json.RawMessage            `json:"transaction"`
					Functions   map[string]json.RawMessage `json:"functions"`
				}
				if err := json.Unmarshal(data, &fixture); err != nil {
					t.Fatal(err)
				}
				tx := &rpcv2.ExecutedTransaction{}
				if err := protojson.Unmarshal(fixture.Transaction, tx); err != nil {
					t.Fatal(err)
				}
				b := &SuiBackend{
					ledger:   &fakeLedgerClient{response: &rpcv2.GetTransactionResponse{Transaction: tx}},
					packages: &recordedFunctions{responses: fixture.Functions},
				}
				options := map[string]bool{"showInput": true, "showEffects": true, "showEvents": true, "showObjectChanges": true, "showBalanceChanges": true}
				var params any = []any{digest, options}
				switch method {
				case "sui_dryRunTransactionBlock":
					b.execution = &simulationClient{response: &rpcv2.SimulateTransactionResponse{Transaction: tx}}
					params = []any{"AQ=="}
				case "sui_executeTransactionBlock":
					b.execution = &inputExecutionClient{response: &rpcv2.ExecuteTransactionResponse{Transaction: tx}}
					params = []any{"AQ==", []string{"Ag=="}, options, "WaitForEffectsCert"}
				}
				body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
				recorder := httptest.NewRecorder()
				newTestHandler(b).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/sui", strings.NewReader(string(body))))
				var response struct {
					Result map[string]any `json:"result"`
					Error  any            `json:"error"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Error != nil || response.Result == nil || response.Result["errors"] != nil {
					t.Fatalf("conversion failed: %s", recorder.Body.String())
				}
				result := response.Result
				for _, field := range []string{"effects", "events", "objectChanges", "balanceChanges"} {
					if _, ok := result[field]; !ok {
						t.Fatalf("missing %s", field)
					}
				}
				if result["effects"].(map[string]any)["status"].(map[string]any)["status"] != "success" {
					t.Fatal("execution status changed")
				}
				var input map[string]any
				if method == "sui_dryRunTransactionBlock" {
					input = result["input"].(map[string]any)
				} else {
					if result["digest"] != digest {
						t.Fatal("digest changed")
					}
					input = result["transaction"].(map[string]any)["data"].(map[string]any)
				}
				inputs := input["transaction"].(map[string]any)["inputs"].([]any)
				balances := result["balanceChanges"].([]any)
				if strings.HasPrefix(digest, "DYM") {
					want := map[string]any{"type": "pure", "valueType": "0x1::option::Option<u64>", "value": []any{}}
					if !reflect.DeepEqual(inputs[9], want) {
						t.Fatalf("reused input: %v", inputs[9])
					}
					wantBalances := []any{map[string]any{"owner": map[string]any{"AddressOwner": "0x000037de3368098ebbb5bc6808283004f4746a34e16d32e171f9400cd58cdd69"}, "coinType": "0x2::sui::SUI", "amount": "-646920"}}
					if !reflect.DeepEqual(balances, wantBalances) {
						t.Fatalf("balance changes: %v", balances)
					}
				} else if strings.HasPrefix(digest, "84Ci") {
					raw := inputs[14].(map[string]any)
					if raw["type"] != "pure" {
						t.Fatal("input kind changed")
					}
					if valueType, ok := raw["valueType"]; !ok || valueType != nil {
						t.Fatalf("untyped input has type: %v", raw)
					}
					wantBytes := []int{83, 85, 73, 65, 82, 66, 48, 49, 31, 232, 65, 117, 247, 202, 186, 116, 143, 24, 30, 76, 167, 126, 3, 61, 139, 86, 234, 34, 35, 121, 130, 28, 98, 211, 177, 94, 103, 212, 207, 83}
					values, ok := raw["value"].([]any)
					if !ok || len(values) != len(wantBytes) {
						t.Fatalf("raw input shape: %v", raw)
					}
					for i, v := range values {
						if v != float64(wantBytes[i]) {
							t.Fatalf("raw byte %d changed: %v", i, v)
						}
					}
					wantBalances := []any{
						map[string]any{"owner": map[string]any{"AddressOwner": "0x2c3f65a489cfc8afe08acbf2f276b3a5301db295b1a3290e23f0bd683a775c33"}, "coinType": "0x2::sui::SUI", "amount": "20170039"},
						map[string]any{"owner": map[string]any{"AddressOwner": "0xd09783e7fe926e738c17522fed72866ef53b85d764bf10c0448854ba041a23ee"}, "coinType": "0x2::sui::SUI", "amount": "-252208"},
					}
					if !reflect.DeepEqual(balances, wantBalances) {
						t.Fatalf("balance changes changed: %v", balances)
					}
				} else if len(balances) != 0 {
					t.Fatalf("unexpected balance changes: %v", balances)
				}
			})
		}
	}
}
