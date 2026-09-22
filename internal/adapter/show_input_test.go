package adapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	rpcv2 "sui-adapter/internal/suiv2"
)

type inputExecutionClient struct {
	rpcv2.TransactionExecutionServiceClient
	request  *rpcv2.ExecuteTransactionRequest
	response *rpcv2.ExecuteTransactionResponse
	calls    int
	err      error
}

func (f *inputExecutionClient) ExecuteTransaction(_ context.Context, r *rpcv2.ExecuteTransactionRequest, _ ...grpc.CallOption) (*rpcv2.ExecuteTransactionResponse, error) {
	f.calls++
	f.request = r
	return f.response, f.err
}

func parsedInputFixture() (*rpcv2.ExecutedTransaction, *functionClient) {
	call := &rpcv2.Command{Command: &rpcv2.Command_MoveCall{MoveCall: &rpcv2.MoveCall{
		Package: ptr("0x42"), Module: ptr("m"), Function: ptr("f"), TypeArguments: []string{"u64"}, Arguments: []*rpcv2.Argument{pureArg(0)},
	}}}
	tx := pureTx([]*rpcv2.Input{pureInput([]byte{42, 0, 0, 0, 0, 0, 0, 0})}, call)
	tx.Sender = ptr("0x123")
	tx.Bcs = &rpcv2.Bcs{Value: []byte{1, 2}}
	executed := &rpcv2.ExecutedTransaction{Digest: ptr("digest"), Transaction: tx, Signatures: []*rpcv2.UserSignature{{Bcs: &rpcv2.Bcs{Value: []byte{3, 4}}}}}
	packages := &functionClient{function: &rpcv2.FunctionDescriptor{Parameters: []*rpcv2.OpenSignature{{Body: &rpcv2.OpenSignatureBody{Type: ptr(rpcv2.OpenSignatureBody_TYPE_PARAMETER), TypeParameter: ptr(uint32(0))}}}}}
	return executed, packages
}

func TestShowInputReadAndExecution(t *testing.T) {
	for _, execute := range []bool{false, true} {
		for _, raw := range []bool{false, true} {
			t.Run(fmt.Sprintf("execute=%t/raw=%t", execute, raw), func(t *testing.T) {
				tx, packages := parsedInputFixture()
				ledger := &fakeLedgerClient{response: &rpcv2.GetTransactionResponse{Transaction: tx}}
				execution := &inputExecutionClient{response: &rpcv2.ExecuteTransactionResponse{Transaction: tx}}
				b := &SuiBackend{ledger: ledger, execution: execution, packages: packages}
				method := "sui_getTransactionBlock"
				params := fmt.Sprintf(`["digest",{"showInput":true,"showRawInput":%t}]`, raw)
				if execute {
					method = "sui_executeTransactionBlock"
					params = fmt.Sprintf(`["AQI=",["AwQ="],{"showInput":true,"showRawInput":%t},"WaitForLocalExecution"]`, raw)
				}
				value, err := b.Call(context.Background(), method, json.RawMessage(params))
				if err != nil {
					t.Fatal(err)
				}
				result := value.(map[string]any)
				input := result["transaction"].(map[string]any)
				data := input["data"].(map[string]any)
				inputs := data["transaction"].(map[string]any)["inputs"].([]any)
				if data["sender"] != "0x123" || !reflect.DeepEqual(input["txSignatures"], []string{"AwQ="}) || !reflect.DeepEqual(inputs, []any{map[string]any{"type": "pure", "valueType": "u64", "value": "42"}}) {
					t.Fatalf("bad input: %v", input)
				}
				if result["errors"] != nil || packages.calls != 1 {
					t.Fatalf("unexpected errors/lookups: %v %d", result, packages.calls)
				}
				rawValue, present := result["rawTransaction"]
				if present != raw {
					t.Fatalf("raw presence: %v", result)
				}
				if raw && rawValue != base64.StdEncoding.EncodeToString([]byte{1, 0, 0, 0, 1, 2, 1, 2, 3, 4}) {
					t.Fatalf("raw changed: %v", rawValue)
				}
				mask := ledger.request.GetReadMask()
				if execute {
					mask = execution.request.GetReadMask()
					if execution.calls != 1 || !bytes.Equal(execution.request.GetTransaction().GetBcs().GetValue(), []byte{1, 2}) || !bytes.Equal(execution.request.GetSignatures()[0].GetBcs().GetValue(), []byte{3, 4}) || result["confirmedLocalExecution"] != true {
						t.Fatalf("execution changed: %v", result)
					}
				}
				if !slices.Contains(mask.GetPaths(), "transaction") {
					t.Fatal("parsed transaction missing from read mask")
				}
			})
		}
	}
}

func TestShowInputResolutionFailures(t *testing.T) {
	for _, failure := range []string{"signature unavailable", "unknown type", "missing transaction", "invalid BCS"} {
		for _, execute := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/execute=%t", failure, execute), func(t *testing.T) {
				tx, packages := parsedInputFixture()
				switch failure {
				case "signature unavailable":
					packages.err = status.Error(codes.Unavailable, "signature unavailable")
				case "unknown type":
					tx.Transaction.Kind.GetProgrammableTransaction().Commands = nil
				case "missing transaction":
					tx.Transaction = nil
				case "invalid BCS":
					tx.Transaction.Kind.GetProgrammableTransaction().Inputs[0].Pure = []byte{1}
				}
				tx.Effects = &rpcv2.TransactionEffects{}
				ledger := &fakeLedgerClient{response: &rpcv2.GetTransactionResponse{Transaction: tx}}
				execution := &inputExecutionClient{response: &rpcv2.ExecuteTransactionResponse{Transaction: tx}}
				b := &SuiBackend{ledger: ledger, execution: execution, packages: packages}
				method := "sui_getTransactionBlock"
				params := `["digest",{"showInput":true}]`
				if execute {
					method = "sui_executeTransactionBlock"
					params = `["AQI=",["AwQ="],{"showInput":true,"showRawInput":true,"showEffects":true}]`
				}
				value, err := b.Call(context.Background(), method, json.RawMessage(params))
				if !execute {
					if err == nil {
						t.Fatal("read must report missing input")
					}
					return
				}
				if err != nil {
					t.Fatalf("input error hid executed transaction: %v", err)
				}
				result := value.(map[string]any)
				errors, ok := result["errors"].([]string)
				if result["digest"] != "digest" || result["transaction"] != nil || result["effects"] == nil || !ok || len(errors) != 1 || execution.calls != 1 || result["confirmedLocalExecution"] != true {
					t.Fatalf("execution result lost: %v", result)
				}
				if failure != "missing transaction" && result["rawTransaction"] == nil {
					t.Fatal("raw response lost")
				}
			})
		}
	}
}

func TestShowInputDisabledSkipsResolution(t *testing.T) {
	for _, execute := range []bool{false, true} {
		tx, packages := parsedInputFixture()
		packages.err = fmt.Errorf("must not fetch signatures")
		ledger := &fakeLedgerClient{response: &rpcv2.GetTransactionResponse{Transaction: tx}}
		execution := &inputExecutionClient{response: &rpcv2.ExecuteTransactionResponse{Transaction: tx}}
		b := &SuiBackend{ledger: ledger, execution: execution, packages: packages}
		method := "sui_getTransactionBlock"
		params := `["digest",{"showRawInput":true}]`
		if execute {
			method = "sui_executeTransactionBlock"
			params = `["AQI=",["AwQ="],{"showRawInput":true}]`
		}
		value, err := b.Call(context.Background(), method, json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		result := value.(map[string]any)
		if result["transaction"] != nil || result["rawTransaction"] == nil || packages.calls != 0 {
			t.Fatalf("unexpected parsing: %v", result)
		}
	}
}

func TestShowInputExecutionFailureRemainsError(t *testing.T) {
	tx, packages := parsedInputFixture()
	upstream := status.Error(codes.InvalidArgument, "invalid signature")
	execution := &inputExecutionClient{response: &rpcv2.ExecuteTransactionResponse{Transaction: tx}, err: upstream}
	value, err := (&SuiBackend{execution: execution, packages: packages}).Call(context.Background(), "sui_executeTransactionBlock", json.RawMessage(`["AQI=",["AwQ="],{"showInput":true}]`))
	if err != upstream || value != nil || execution.calls != 1 || packages.calls != 0 {
		t.Fatalf("upstream failure lost: %v %v", value, err)
	}
}
