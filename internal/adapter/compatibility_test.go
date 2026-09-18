package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/grpc"
	rpcv2 "sui-adapter/internal/suiv2"
)

type pagedBalanceClient struct {
	rpcv2.StateServiceClient
	calls  int
	fail   bool
	repeat bool
}

func (f *pagedBalanceClient) ListBalances(_ context.Context, req *rpcv2.ListBalancesRequest, _ ...grpc.CallOption) (*rpcv2.ListBalancesResponse, error) {
	f.calls++
	if req.GetOwner() != "0xowner" {
		return nil, errors.New("owner changed")
	}
	if f.calls == 1 {
		if len(req.GetPageToken()) != 0 {
			return nil, errors.New("unexpected initial token")
		}
		return &rpcv2.ListBalancesResponse{Balances: []*rpcv2.Balance{{CoinType: ptr("first"), Balance: ptr(uint64(1))}}, NextPageToken: []byte("next")}, nil
	}
	if string(req.GetPageToken()) != "next" {
		return nil, errors.New("missing continuation")
	}
	if f.fail {
		return nil, errors.New("second page failed")
	}
	if f.repeat {
		return &rpcv2.ListBalancesResponse{NextPageToken: []byte("next")}, nil
	}
	return &rpcv2.ListBalancesResponse{Balances: []*rpcv2.Balance{{CoinType: ptr("second"), Balance: ptr(uint64(2))}}}, nil
}

func TestAllBalancesPagination(t *testing.T) {
	for _, mode := range []string{"complete", "failure", "repeated token", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			state := &pagedBalanceClient{fail: mode == "failure", repeat: mode == "repeated token"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			result, err := (&SuiBackend{state: state}).allBalances(ctx, json.RawMessage(`["0xowner"]`))
			if mode != "complete" {
				if err == nil || result != nil {
					t.Fatalf("must not return partial balances: %v, %v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			balances := result.([]map[string]any)
			if len(balances) != 2 || balances[1]["totalBalance"] != "2" || state.calls != 2 {
				t.Fatalf("incomplete balances: %v", balances)
			}
		})
	}
}

func TestIndependentResponseOptionDependencies(t *testing.T) {
	for _, tc := range []struct {
		options  transactionOptions
		required string
	}{
		{transactionOptions{ShowBalanceChanges: true}, "effects"},
		{transactionOptions{ShowObjectChanges: true}, "transaction.sender"},
		{transactionOptions{ShowObjectChanges: true, ShowRawInput: true}, "transaction.sender"},
	} {
		found := false
		for _, path := range transactionReadMask(tc.options).Paths {
			if path == tc.required {
				found = true
			}
		}
		if !found {
			t.Errorf("options %+v missing dependency %s", tc.options, tc.required)
		}
	}
}

func TestDryRunInputShape(t *testing.T) {
	result, err := legacyDryRunTransaction(&rpcv2.ExecutedTransaction{Transaction: &rpcv2.Transaction{Sender: ptr("0x123")}})
	if err != nil {
		t.Fatal(err)
	}
	input := result["input"].(map[string]any)
	if input["sender"] != "0x123" || input["messageVersion"] != "v1" || input["data"] != nil || input["txSignatures"] != nil {
		t.Fatalf("invalid dry-run input: %#v", input)
	}
}

func TestExecutionDefaultWaitAndOverride(t *testing.T) {
	for _, option := range []string{"showEffects", "showEvents", "showBalanceChanges", "showObjectChanges", "showRawEffects"} {
		for _, requestType := range []string{"", ",null", `,"WaitForEffectsCert"`} {
			t.Run(option+requestType, func(t *testing.T) {
				execution := &fakeExecutionClient{response: &rpcv2.ExecuteTransactionResponse{Transaction: &rpcv2.ExecutedTransaction{Digest: ptr("digest")}}}
				ledger := &fakeLedgerClient{response: &rpcv2.GetTransactionResponse{Transaction: &rpcv2.ExecutedTransaction{Digest: ptr("digest")}}}
				backend := &SuiBackend{execution: execution, ledger: ledger}
				result, err := backend.executeTransaction(context.Background(), json.RawMessage(`["AQ==",["Ag=="],{"`+option+`":true}`+requestType+`]`))
				if err != nil {
					t.Fatal(err)
				}
				wantConfirm := requestType != `,"WaitForEffectsCert"`
				confirmed, present := result.(map[string]any)["confirmedLocalExecution"]
				if present != wantConfirm || (present && confirmed != true) || (ledger.request != nil) != wantConfirm {
					t.Fatalf("unexpected confirmation: %v", result)
				}
			})
		}
	}
}

func TestExecutionRejectsParsedInputBeforeSubmission(t *testing.T) {
	execution := &fakeExecutionClient{}
	_, err := (&SuiBackend{execution: execution}).executeTransaction(context.Background(), json.RawMessage(`["AQ==",["Ag=="],{"showInput":true}]`))
	rpcErr, ok := err.(*RPCError)
	if !ok || rpcErr.Code != legacyIncompatible || execution.request != nil {
		t.Fatalf("must reject before execution: %v", err)
	}
}

func TestParsedPureInputRejectionPreservesRawRead(t *testing.T) {
	tx := &rpcv2.ExecutedTransaction{Transaction: &rpcv2.Transaction{
		Bcs:  &rpcv2.Bcs{Value: []byte{1, 2}},
		Kind: &rpcv2.TransactionKind{Data: &rpcv2.TransactionKind_ProgrammableTransaction{ProgrammableTransaction: &rpcv2.ProgrammableTransaction{Inputs: []*rpcv2.Input{{Kind: ptr(rpcv2.Input_PURE), Pure: []byte{1}}}}}},
	}}
	_, err := legacyTransaction(tx, transactionOptions{ShowInput: true}, nil)
	if rpcErr, ok := err.(*RPCError); !ok || rpcErr.Code != legacyIncompatible {
		t.Fatalf("expected compatibility error: %v", err)
	}
	if _, err := legacyDryRunTransaction(tx); err == nil {
		t.Fatal("dry-run guessed pure type")
	}
	result, err := legacyTransaction(tx, transactionOptions{ShowRawInput: true}, nil)
	if err != nil || result["rawTransaction"] == nil {
		t.Fatalf("raw input unavailable: %v, %v", result, err)
	}
}
