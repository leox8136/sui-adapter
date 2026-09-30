package adapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	rpcv2 "sui-adapter/internal/suiv2"
)

func pureArg(i uint32) *rpcv2.Argument {
	return &rpcv2.Argument{Kind: ptr(rpcv2.Argument_INPUT), Input: ptr(i)}
}
func pureTx(inputs []*rpcv2.Input, commands ...*rpcv2.Command) *rpcv2.Transaction {
	return &rpcv2.Transaction{Kind: &rpcv2.TransactionKind{Data: &rpcv2.TransactionKind_ProgrammableTransaction{ProgrammableTransaction: &rpcv2.ProgrammableTransaction{Inputs: inputs, Commands: commands}}}}
}
func pureInput(data []byte) *rpcv2.Input {
	return &rpcv2.Input{Kind: ptr(rpcv2.Input_PURE), Pure: data}
}

type simulationClient struct {
	rpcv2.TransactionExecutionServiceClient
	response *rpcv2.SimulateTransactionResponse
	request  *rpcv2.SimulateTransactionRequest
}

func (f *simulationClient) SimulateTransaction(_ context.Context, r *rpcv2.SimulateTransactionRequest, _ ...grpc.CallOption) (*rpcv2.SimulateTransactionResponse, error) {
	f.request = r
	return f.response, nil
}

// Regression fixture supplied by the caller. The simulated input below is the
// transaction's decoded PTB: SplitCoins(Gas, Input(1)), then transfer to Input(0).
const dryRunRegressionBCS = "AAACACBYSC11V72QjcDJPz/K3ZixXNBtLzyuSaHsI8UBwxW0QQAIQGYDAQAAAAACAgABAQEAAQECAAABAADa0/Gyc3hSL0BHF6DgvKeemEg/9EcgMN1OXxqBJ0Rn+gVQhJo7S8G8ZDdA49GFKd1S0qQpEYkLOAb3Iw9lC+jQtcN9szgAAAAAIJrq12mnhRu4gqHT0VeArY/M+ZKEwM+9UnHeyEwCBxzseU+KALPrmQi2P/gQx+6RPFd9LLsThvAzpAoOEgAq97vFfbM4AAAAACBeJdc9wkQtELCon7f2Ze+L0evcOKLZ6nQmA1d6kflLAzl9cb37zd13XpsI3wNPe/fuze6V5cv1/Yzs+n4d2hRB/hfQFAAAAAAgVocpKhXQ3mVugMr8irklPA0QYw/k6y497ng7dvP3nLG6hty0qbooKvaSEoDRP3VEut24zQf5YizAy0Vy0S5uR+NcXDYAAAAAIJHfG6yo/DltbOEfx80J84+ciAettOSLuzozmtb1OeIBF2HI6xHG13B6EsNM69PNvQPM46pzgTH9/XvWOGRE5DPjXFw2AAAAACDSczF7+xRwpd9WCduunsdvWdJ4/3Zve5I43MvugNRZq9rT8bJzeFIvQEcXoOC8p56YSD/0RyAw3U5fGoEnRGf66AMAAAAAAACAlpgAAAAAAAA="

func TestDryRunRegressionHTTP(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(dryRunRegressionBCS)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data[:5], []byte{0, 0, 2, 0, 32}) || !bytes.Equal(data[37:39], []byte{0, 8}) {
		t.Fatal("fixture PTB header changed")
	}
	if !bytes.Equal(data[47:64], []byte{2, 2, 0, 1, 1, 1, 0, 1, 1, 2, 0, 0, 1, 0, 0, 218, 211}) {
		t.Fatalf("fixture commands changed: %x", data[47:64])
	}
	tx := pureTx([]*rpcv2.Input{pureInput(data[5:37]), pureInput(data[39:47])},
		&rpcv2.Command{Command: &rpcv2.Command_SplitCoins{SplitCoins: &rpcv2.SplitCoins{Coin: &rpcv2.Argument{Kind: ptr(rpcv2.Argument_GAS)}, Amounts: []*rpcv2.Argument{pureArg(1)}}}},
		&rpcv2.Command{Command: &rpcv2.Command_TransferObjects{TransferObjects: &rpcv2.TransferObjects{Address: pureArg(0), Objects: []*rpcv2.Argument{{Kind: ptr(rpcv2.Argument_RESULT), Result: ptr(uint32(0))}}}}})
	execution := &simulationClient{response: &rpcv2.SimulateTransactionResponse{Transaction: &rpcv2.ExecutedTransaction{Transaction: tx}}}
	handler := newTestHandler(&SuiBackend{execution: execution})
	request := httptest.NewRequest(http.MethodPost, "/sui", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"sui_dryRunTransactionBlock","params":[%q]}`, dryRunRegressionBCS)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response struct {
		Result struct {
			Input struct {
				Transaction struct {
					Inputs []map[string]any `json:"inputs"`
				} `json:"transaction"`
			} `json:"input"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatal(recorder.Body.String())
	}
	inputs := response.Result.Input.Transaction.Inputs
	if len(inputs) != 2 || inputs[0]["valueType"] != "address" || inputs[0]["value"] != "0x58482d7557bd908dc0c93f3fcadd98b15cd06d2f3cae49a1ec23c501c315b441" || inputs[1]["valueType"] != "u64" || inputs[1]["value"] != "17000000" {
		t.Fatalf("unexpected inputs: %#v", inputs)
	}
	if !bytes.Equal(execution.request.GetTransaction().GetBcs().GetValue(), data) {
		t.Fatal("simulation BCS changed")
	}
}

type functionClient struct {
	rpcv2.MovePackageServiceClient
	function *rpcv2.FunctionDescriptor
	calls    int
	err      error
}

func (f *functionClient) GetFunction(_ context.Context, request *rpcv2.GetFunctionRequest, _ ...grpc.CallOption) (*rpcv2.GetFunctionResponse, error) {
	f.calls++
	if request.GetPackageId() != "0x42" || request.GetModuleName() != "m" || request.GetName() != "f" {
		return nil, fmt.Errorf("wrong function request")
	}
	return &rpcv2.GetFunctionResponse{Function: f.function}, f.err
}
func TestResolveMoveCallPureInputs(t *testing.T) {
	signature := &rpcv2.OpenSignatureBody{Type: ptr(rpcv2.OpenSignatureBody_VECTOR), TypeParameterInstantiation: []*rpcv2.OpenSignatureBody{{Type: ptr(rpcv2.OpenSignatureBody_TYPE_PARAMETER), TypeParameter: ptr(uint32(0))}}}
	client := &functionClient{function: &rpcv2.FunctionDescriptor{Parameters: []*rpcv2.OpenSignature{{Reference: ptr(rpcv2.OpenSignature_IMMUTABLE), Body: signature}}}}
	call := &rpcv2.Command{Command: &rpcv2.Command_MoveCall{MoveCall: &rpcv2.MoveCall{Package: ptr("0x42"), Module: ptr("m"), Function: ptr("f"), TypeArguments: []string{"u64"}, Arguments: []*rpcv2.Argument{pureArg(0)}}}}
	input := pureInput([]byte{1, 42, 0, 0, 0, 0, 0, 0, 0})
	values, err := (&SuiBackend{packages: client}).resolvePureInputs(context.Background(), pureTx([]*rpcv2.Input{input}, call, call))
	if err != nil || client.calls != 1 || !reflect.DeepEqual(values[input], map[string]any{"type": "pure", "valueType": "vector<u64>", "value": []any{"42"}}) {
		t.Fatalf("values=%v calls=%d err=%v", values, client.calls, err)
	}
	client.err = fmt.Errorf("upstream unavailable")
	if _, err := (&SuiBackend{packages: client}).resolvePureInputs(context.Background(), pureTx([]*rpcv2.Input{input}, call)); err != client.err {
		t.Fatalf("upstream error lost: %v", err)
	}
}

func TestPureInputUnresolved(t *testing.T) {
	input := pureInput(make([]byte, 32))
	_, err := (&SuiBackend{}).resolvePureInputs(context.Background(), pureTx([]*rpcv2.Input{input}))
	if rpcErr, ok := err.(*RPCError); !ok || rpcErr.Code != legacyIncompatible || strings.Contains(rpcErr.Message, "showRawInput") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPureInputReusedWithDifferentLayouts(t *testing.T) {
	option := &rpcv2.OpenSignatureBody{Type: ptr(rpcv2.OpenSignatureBody_DATATYPE), TypeName: ptr("0x1::option::Option"), TypeParameterInstantiation: []*rpcv2.OpenSignatureBody{{Type: ptr(rpcv2.OpenSignatureBody_U64)}}}
	boolean := &rpcv2.OpenSignatureBody{Type: ptr(rpcv2.OpenSignatureBody_BOOL)}
	for _, tc := range []struct {
		first, last *rpcv2.OpenSignatureBody
		typ         string
		want        any
	}{
		{option, boolean, "bool", false}, {boolean, option, "0x1::option::Option<u64>", []any{}},
	} {
		client := &functionClient{function: &rpcv2.FunctionDescriptor{Parameters: []*rpcv2.OpenSignature{{Body: tc.first}, {Body: tc.last}}}}
		call := &rpcv2.Command{Command: &rpcv2.Command_MoveCall{MoveCall: &rpcv2.MoveCall{Package: ptr("0x42"), Module: ptr("m"), Function: ptr("f"), Arguments: []*rpcv2.Argument{pureArg(0), pureArg(0)}}}}
		input := pureInput([]byte{0}) // Both None<u64> and false have this BCS encoding.
		values, err := (&SuiBackend{packages: client}).resolvePureInputs(context.Background(), pureTx([]*rpcv2.Input{input}, call))
		if err != nil || !reflect.DeepEqual(values[input], map[string]any{"type": "pure", "valueType": tc.typ, "value": tc.want}) {
			t.Fatalf("got %v err %v", values, err)
		}
	}
	// The selected final layout still has to decode valid BCS.
	input := pureInput(make([]byte, 32))
	transfer := &rpcv2.Command{Command: &rpcv2.Command_TransferObjects{TransferObjects: &rpcv2.TransferObjects{Address: pureArg(0)}}}
	split := &rpcv2.Command{Command: &rpcv2.Command_SplitCoins{SplitCoins: &rpcv2.SplitCoins{Amounts: []*rpcv2.Argument{pureArg(0)}}}}
	if _, err := (&SuiBackend{}).resolvePureInputs(context.Background(), pureTx([]*rpcv2.Input{input}, transfer, split)); err == nil {
		t.Fatal("invalid final u64 layout accepted")
	}
	values, err := (&SuiBackend{}).resolvePureInputs(context.Background(), pureTx([]*rpcv2.Input{input}, split, transfer))
	if err != nil || values[input]["valueType"] != "address" {
		t.Fatalf("last command layout not selected: %v %v", values, err)
	}
}

func TestDecodePureValue(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		data []byte
		want any
	}{
		{"bool", []byte{1}, true}, {"u8", []byte{255}, uint64(255)}, {"u16", []byte{255, 255}, uint64(65535)},
		{"u32", []byte{255, 255, 255, 255}, uint64(4294967295)}, {"u64", bytes.Repeat([]byte{255}, 8), "18446744073709551615"},
		{"u128", bytes.Repeat([]byte{255}, 16), "340282366920938463463374607431768211455"},
		{"u256", bytes.Repeat([]byte{255}, 32), "115792089237316195423570985008687907853269984665640564039457584007913129639935"},
		{"vector<u8>", []byte{2, 'h', 'i'}, "hi"}, {"vector<u8>", []byte{1, 255}, []any{uint64(255)}},
		{"vector<vector<u8>>", []byte{1, 1, 65}, []any{[]any{uint64(65)}}},
		{"0x1::string::String", []byte{2, 'h', 'i'}, "hi"}, {"0x1::option::Option<u64>", []byte{0}, []any{}},
		{"0x1::option::Option<u64>", []byte{1, 42, 0, 0, 0, 0, 0, 0, 0}, []any{"42"}},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			got, err := decodePureValue(tc.typ, tc.data)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%#v want=%#v err=%v", got, tc.want, err)
			}
		})
	}
	for _, tc := range []struct {
		typ  string
		data []byte
	}{
		{"bool", []byte{2}}, {"u64", []byte{1}}, {"u8", []byte{1, 2}}, {"vector<u8>", []byte{128, 0}},
		{"vector<u8>", []byte{255, 255, 255, 255, 16}}, {"vector<u64>", []byte{255, 255, 255, 255, 15}},
		{"0x1::option::Option<u8>", []byte{2, 1, 2}}, {"0x1::ascii::String", []byte{2, 195, 169}},
		{"vector<unknown>", []byte{0}}, {strings.Repeat("vector<", 66) + "u8" + strings.Repeat(">", 66), []byte{0}},
	} {
		if _, err := decodePureValue(tc.typ, tc.data); err == nil {
			t.Fatalf("accepted invalid %s %x", tc.typ, tc.data)
		}
	}
}

func TestResolveMakeMoveVectorAndMissingFunction(t *testing.T) {
	input := pureInput(make([]byte, 32))
	vector := &rpcv2.Command{Command: &rpcv2.Command_MakeMoveVector{MakeMoveVector: &rpcv2.MakeMoveVector{ElementType: ptr("u256"), Elements: []*rpcv2.Argument{pureArg(0)}}}}
	values, err := (&SuiBackend{}).resolvePureInputs(context.Background(), pureTx([]*rpcv2.Input{input}, vector))
	if err != nil || values[input]["valueType"] != "u256" || values[input]["value"] != "0" {
		t.Fatalf("32-byte integer confused with address: %v, %v", values, err)
	}
	vector.GetMakeMoveVector().ElementType = nil
	if _, err := (&SuiBackend{}).resolvePureInputs(context.Background(), pureTx([]*rpcv2.Input{input}, vector)); err == nil {
		t.Fatal("accepted missing vector type")
	}
	call := &rpcv2.Command{Command: &rpcv2.Command_MoveCall{MoveCall: &rpcv2.MoveCall{Package: ptr("0x42"), Module: ptr("m"), Function: ptr("f"), Arguments: []*rpcv2.Argument{pureArg(0)}}}}
	for _, client := range []rpcv2.MovePackageServiceClient{nil, &functionClient{}, &functionClient{function: &rpcv2.FunctionDescriptor{}}, &functionClient{function: &rpcv2.FunctionDescriptor{Parameters: []*rpcv2.OpenSignature{{Body: &rpcv2.OpenSignatureBody{Type: ptr(rpcv2.OpenSignatureBody_TYPE_PARAMETER), TypeParameter: ptr(uint32(0))}}}}}} {
		_, err := (&SuiBackend{packages: client}).resolvePureInputs(context.Background(), pureTx([]*rpcv2.Input{input}, call))
		if e, ok := err.(*RPCError); !ok || e.Code != legacyIncompatible {
			t.Fatalf("missing signature must fail: %v", err)
		}
	}
}
