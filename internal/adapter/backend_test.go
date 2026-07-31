package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	rpcv2 "sui-adapter/internal/suiv2"

	"google.golang.org/grpc"
)

func TestTransactionReadMask(t *testing.T) {
	options := transactionOptions{
		ShowInput:          true,
		ShowRawInput:       true,
		ShowRawEffects:     true,
		ShowEvents:         true,
		ShowObjectChanges:  true,
		ShowBalanceChanges: true,
	}

	mask := transactionReadMask(options)
	for _, expected := range []string{"transaction", "effects", "events", "balance_changes"} {
		found := false
		for _, path := range mask.Paths {
			found = found || path == expected
		}
		if !found {
			t.Errorf("read mask does not contain %q: %v", expected, mask.Paths)
		}
	}
}

func TestPositionalParams(t *testing.T) {
	values, err := positionalParams(json.RawMessage(`["owner",null,50]`), 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 || string(values[0]) != `"owner"` || !isNull(values[1]) {
		t.Fatalf("unexpected params: %q", values)
	}

	for _, raw := range []string{`{}`, `[]`, `["a","b"] trailing`} {
		if _, err := positionalParams(json.RawMessage(raw), 1, 1); err == nil {
			t.Fatalf("params %q should be rejected", raw)
		}
	}
}

func TestCoinTypeFromObjectType(t *testing.T) {
	fullSUI := "0x0000000000000000000000000000000000000000000000000000000000000002::sui::SUI"
	tests := map[string]string{
		"0x2::coin::Coin<" + fullSUI + ">": "0x2::sui::SUI",
		fullSUI:                            "0x2::sui::SUI",
		"0x2::coin::Coin<0xabc::x::Y>":     "0xabc::x::Y",
	}
	for input, want := range tests {
		if got := coinTypeFromObjectType(input); got != want {
			t.Errorf("coinTypeFromObjectType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBytesToNumbers(t *testing.T) {
	got := bytesToNumbers([]byte{0, 127, 255})
	if !reflect.DeepEqual(got, []int{0, 127, 255}) {
		t.Fatalf("bytesToNumbers() = %v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `[0,127,255]` {
		t.Fatalf("raw effects JSON = %s, want a numeric array", encoded)
	}
}

func TestLegacyRawTransaction(t *testing.T) {
	signature := base64.StdEncoding.EncodeToString([]byte{3, 4, 5})
	encoded := legacyRawTransaction([]byte{1, 2}, []string{signature})
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 0, 0, 0, 1, 2, 1, 3, 3, 4, 5}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("legacyRawTransaction() = %v, want %v", decoded, want)
	}
}

func TestExecuteTransactionUsesOfficialRequestTypes(t *testing.T) {
	execution := &fakeExecutionClient{
		response: &rpcv2.ExecuteTransactionResponse{
			Transaction: &rpcv2.ExecutedTransaction{
				Digest: ptr("digest"),
				Effects: &rpcv2.TransactionEffects{
					Status: &rpcv2.ExecutionStatus{Success: ptr(true)},
				},
			},
		},
	}
	backend := &SuiBackend{execution: execution}
	transaction := base64.StdEncoding.EncodeToString([]byte{1, 2})
	signature := base64.StdEncoding.EncodeToString([]byte{3, 4})
	params := json.RawMessage(`["` + transaction + `",["` + signature + `"],{}]`)

	result, err := backend.executeTransaction(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if execution.request == nil ||
		!reflect.DeepEqual(execution.request.GetTransaction().GetBcs().GetValue(), []byte{1, 2}) ||
		!reflect.DeepEqual(execution.request.GetSignatures()[0].GetBcs().GetValue(), []byte{3, 4}) {
		t.Fatalf("unexpected official gRPC request: %+v", execution.request)
	}
	legacy := result.(map[string]any)
	if legacy["digest"] != "digest" || legacy["confirmedLocalExecution"] != true {
		t.Fatalf("unexpected legacy result: %+v", legacy)
	}
}

func TestLegacyPureInputs(t *testing.T) {
	addressBytes := make([]byte, 32)
	addressBytes[31] = 42
	tests := []struct {
		name  string
		input *rpcv2.Input
		want  map[string]any
	}{
		{
			name: "u64",
			input: &rpcv2.Input{
				Kind: ptr(rpcv2.Input_PURE),
				Pure: []byte{42, 0, 0, 0, 0, 0, 0, 0},
			},
			want: map[string]any{"type": "pure", "valueType": "u64", "value": "42"},
		},
		{
			name: "address",
			input: &rpcv2.Input{
				Kind: ptr(rpcv2.Input_PURE),
				Pure: addressBytes,
			},
			want: map[string]any{
				"type": "pure", "valueType": "address",
				"value": "0x000000000000000000000000000000000000000000000000000000000000002a",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := legacyPureInput(test.input); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("legacyPureInput() = %#v, want %#v", got, test.want)
			}
		})
	}
}

type fakeExecutionClient struct {
	request  *rpcv2.ExecuteTransactionRequest
	response *rpcv2.ExecuteTransactionResponse
}

func (f *fakeExecutionClient) ExecuteTransaction(
	_ context.Context,
	request *rpcv2.ExecuteTransactionRequest,
	_ ...grpc.CallOption,
) (*rpcv2.ExecuteTransactionResponse, error) {
	f.request = request
	return f.response, nil
}

func (f *fakeExecutionClient) SimulateTransaction(
	context.Context,
	*rpcv2.SimulateTransactionRequest,
	...grpc.CallOption,
) (*rpcv2.SimulateTransactionResponse, error) {
	return nil, nil
}
