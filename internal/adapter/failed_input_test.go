package adapter

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	rpcv2 "sui-adapter/internal/suiv2"
)

func TestFailedTransactionInvalidPureInput(t *testing.T) {
	data, err := os.ReadFile("testdata/5MykKR8prfJvjX9RFa8uVtBVi6Qot52y9koR3XBp4veL.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Transaction json.RawMessage
		Functions   map[string]json.RawMessage
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"failed", "success", "missing status", "missing success", "missing error", "unknown input", "lookup failure"} {
		t.Run(variant, func(t *testing.T) {
			tx := &rpcv2.ExecutedTransaction{}
			if err := protojson.Unmarshal(fixture.Transaction, tx); err != nil {
				t.Fatal(err)
			}
			b := &SuiBackend{ledger: &fakeLedgerClient{response: &rpcv2.GetTransactionResponse{Transaction: tx}}, packages: &recordedFunctions{responses: fixture.Functions}}
			switch variant {
			case "success":
				tx.Effects.Status.Success = ptr(true)
			case "missing status":
				tx.Effects.Status = nil
			case "missing success":
				tx.Effects.Status.Success = nil
			case "missing error":
				tx.Effects.Status.Error = nil
			case "unknown input":
				tx.Transaction.Kind.GetProgrammableTransaction().Inputs[2].Kind = rpcv2.Input_InputKind(99).Enum()
			case "lookup failure":
				b.packages = &recordedFunctions{}
			}
			params := json.RawMessage(`["5MykKR8prfJvjX9RFa8uVtBVi6Qot52y9koR3XBp4veL",{"showInput":true,"showRawInput":true,"showEffects":true,"showEvents":true,"showObjectChanges":true,"showBalanceChanges":true}]`)
			got, err := b.transaction(context.Background(), params)
			if variant != "failed" {
				if err == nil || got != nil {
					t.Fatalf("unsafe partial response: %#v, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result := got.(map[string]any)
			errs, ok := result["errors"].([]string)
			if !ok || len(errs) != 1 || !strings.Contains(errs[0], "pure input 2 (vector<u64>)") {
				t.Fatalf("missing decode error: %v", result)
			}
			if _, ok := result["transaction"]; ok {
				t.Fatal("returned invalid parsed input")
			}
			baseline, err := b.transaction(context.Background(), json.RawMessage(strings.Replace(string(params), `"showInput":true`, `"showInput":false`, 1)))
			if err != nil {
				t.Fatal(err)
			}
			delete(result, "errors")
			if !reflect.DeepEqual(result, baseline) {
				t.Fatal("non-input fields changed")
			}
			encoded, _ := json.Marshal(result)
			var wire map[string]any
			json.Unmarshal(encoded, &wire)
			want := []any{map[string]any{"owner": map[string]any{"AddressOwner": "0x05e31c5727bd516d18b07eb3e3779a21eaaf89d9e92ffda7695a757beedab171"}, "coinType": "0x2::sui::SUI", "amount": "-417756"}}
			if !reflect.DeepEqual(wire["balanceChanges"], want) {
				t.Fatalf("balance changed: %v", wire["balanceChanges"])
			}
			if wire["effects"].(map[string]any)["status"].(map[string]any)["status"] != "failure" {
				t.Fatal("lost chain failure")
			}
		})
	}
}
