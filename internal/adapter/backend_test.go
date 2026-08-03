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
	if legacy["digest"] != "digest" {
		t.Fatalf("unexpected legacy result: %+v", legacy)
	}
	if _, ok := legacy["confirmedLocalExecution"]; ok {
		t.Fatalf("default execution should not claim local confirmation: %+v", legacy)
	}
}

func TestExecuteTransactionRequestType(t *testing.T) {
	transaction := base64.StdEncoding.EncodeToString([]byte{1, 2})
	signature := base64.StdEncoding.EncodeToString([]byte{3, 4})
	response := &rpcv2.ExecuteTransactionResponse{
		Transaction: &rpcv2.ExecutedTransaction{Digest: ptr("digest")},
	}

	tests := []struct {
		name          string
		requestType   string
		wantConfirmed bool
		wantErr       bool
		wantCode      int
	}{
		{name: "wait for effects", requestType: waitForEffectsCert},
		{name: "wait for local execution", requestType: waitForLocalExecution, wantErr: true, wantCode: legacyIncompatible},
		{name: "unknown", requestType: "ImmediateReturn", wantErr: true, wantCode: invalidParams},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &SuiBackend{execution: &fakeExecutionClient{response: response}}
			params := json.RawMessage(`["` + transaction + `",["` + signature + `"],{},"` + test.requestType + `"]`)

			result, err := backend.executeTransaction(context.Background(), params)
			if test.wantErr {
				if err == nil {
					t.Fatalf("executeTransaction() error = nil, want error")
				}
				if test.wantCode != 0 {
					rpcErr, ok := err.(*RPCError)
					if !ok || rpcErr.Code != test.wantCode {
						t.Fatalf("executeTransaction() error = %+v, want RPC code %d", err, test.wantCode)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			legacy := result.(map[string]any)
			if got, ok := legacy["confirmedLocalExecution"]; ok != test.wantConfirmed || (ok && got != true) {
				t.Fatalf("confirmedLocalExecution = %v, present %v, want present %v", got, ok, test.wantConfirmed)
			}
		})
	}
}

func TestExecuteTransactionAcceptsSingleSignature(t *testing.T) {
	execution := &fakeExecutionClient{
		response: &rpcv2.ExecuteTransactionResponse{
			Transaction: &rpcv2.ExecutedTransaction{Digest: ptr("digest")},
		},
	}
	backend := &SuiBackend{execution: execution}
	transaction := base64.StdEncoding.EncodeToString([]byte{1, 2})
	signature := base64.StdEncoding.EncodeToString([]byte{3, 4})

	_, err := backend.executeTransaction(context.Background(), json.RawMessage(`["`+transaction+`","`+signature+`"]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(execution.request.GetSignatures()) != 1 ||
		!reflect.DeepEqual(execution.request.GetSignatures()[0].GetBcs().GetValue(), []byte{3, 4}) {
		t.Fatalf("unexpected signatures: %+v", execution.request.GetSignatures())
	}
}

func TestBalanceUsesGRPCBalanceWithoutLockedBalance(t *testing.T) {
	state := &fakeStateClient{
		balanceResponse: &rpcv2.GetBalanceResponse{
			Balance: &rpcv2.Balance{
				CoinType: ptr("0x2::sui::SUI"),
				Balance:  ptr(uint64(12345)),
			},
		},
	}
	backend := &SuiBackend{state: state}

	result, err := backend.balance(context.Background(), json.RawMessage(`["0xabc","0x2::sui::SUI"]`))
	if err != nil {
		t.Fatal(err)
	}
	legacy := result.(map[string]any)
	if legacy["coinType"] != "0x2::sui::SUI" || legacy["totalBalance"] != "12345" {
		t.Fatalf("unexpected balance result: %+v", legacy)
	}
	if _, ok := legacy["lockedBalance"]; ok {
		t.Fatalf("lockedBalance should be omitted: %+v", legacy)
	}
	if got := state.balanceRequest.GetOwner(); got != "0xabc" {
		t.Fatalf("balance owner = %q, want 0xabc", got)
	}
	if got := state.balanceRequest.GetCoinType(); got != "0x2::sui::SUI" {
		t.Fatalf("balance coin type = %q, want 0x2::sui::SUI", got)
	}
}

func TestAllCoinsUsesOfficialCoinObjectCursor(t *testing.T) {
	state := &fakeStateClient{
		listOwnedObjectsResponses: []*rpcv2.ListOwnedObjectsResponse{
			{
				Objects: []*rpcv2.Object{
					{
						ObjectId: ptr("0xcoin1"), ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"),
						Version: ptr(uint64(1)), Digest: ptr("digest-1"), Balance: ptr(uint64(10)),
					},
					{
						ObjectId: ptr("0xcoin2"), ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"),
						Version: ptr(uint64(2)), Digest: ptr("digest-2"), Balance: ptr(uint64(20)),
					},
				},
				NextPageToken: []byte{1},
			},
			{
				Objects: []*rpcv2.Object{
					{
						ObjectId: ptr("0xcoin3"), ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"),
						Version: ptr(uint64(3)), Digest: ptr("digest-3"), Balance: ptr(uint64(30)),
					},
					{
						ObjectId: ptr("0xcoin4"), ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"),
						Version: ptr(uint64(4)), Digest: ptr("digest-4"), Balance: ptr(uint64(40)),
					},
				},
			},
		},
	}
	backend := &SuiBackend{state: state}

	result, err := backend.allCoins(context.Background(), json.RawMessage(`["0xowner","0xcoin2",1]`))
	if err != nil {
		t.Fatal(err)
	}
	legacy := result.(map[string]any)
	data := legacy["data"].([]map[string]any)
	if len(data) != 1 || data[0]["coinObjectId"] != "0xcoin3" {
		t.Fatalf("unexpected page data: %+v", legacy)
	}
	if legacy["nextCursor"] != "0xcoin3" || legacy["hasNextPage"] != true {
		t.Fatalf("unexpected page cursor: %+v", legacy)
	}
	if len(state.listOwnedObjectsRequests) != 2 ||
		len(state.listOwnedObjectsRequests[0].GetPageToken()) != 0 ||
		!reflect.DeepEqual(state.listOwnedObjectsRequests[1].GetPageToken(), []byte{1}) {
		t.Fatalf("unexpected internal pagination requests: %+v", state.listOwnedObjectsRequests)
	}
}

func TestGetCoinsFiltersByCoinTypeAndUsesOfficialCursor(t *testing.T) {
	state := &fakeStateClient{
		listOwnedObjectsResponses: []*rpcv2.ListOwnedObjectsResponse{
			{
				Objects: []*rpcv2.Object{
					{
						ObjectId: ptr("0xcoin1"), ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"),
						Version: ptr(uint64(1)), Digest: ptr("digest-1"), Balance: ptr(uint64(10)),
					},
				},
			},
		},
	}
	backend := &SuiBackend{state: state}

	result, err := backend.coins(context.Background(), json.RawMessage(`["0xowner","0x2::sui::SUI",null,1]`))
	if err != nil {
		t.Fatal(err)
	}
	legacy := result.(map[string]any)
	data := legacy["data"].([]map[string]any)
	if len(data) != 1 || data[0]["coinObjectId"] != "0xcoin1" {
		t.Fatalf("unexpected coins result: %+v", legacy)
	}
	if got := state.listOwnedObjectsRequests[0].GetObjectType(); got != "0x2::coin::Coin<0x2::sui::SUI>" {
		t.Fatalf("object type = %q, want coin type filter", got)
	}
}

func TestAllBalancesUsesGRPCBalancesWithoutLockedBalance(t *testing.T) {
	state := &fakeStateClient{
		listBalancesResponses: []*rpcv2.ListBalancesResponse{
			{
				Balances: []*rpcv2.Balance{
					{CoinType: ptr("0x2::sui::SUI"), Balance: ptr(uint64(100))},
					{CoinType: ptr("0x2::foo::FOO"), Balance: ptr(uint64(200))},
				},
			},
		},
	}
	backend := &SuiBackend{state: state}

	result, err := backend.allBalances(context.Background(), json.RawMessage(`["0xowner"]`))
	if err != nil {
		t.Fatal(err)
	}
	balances := result.([]map[string]any)
	if len(balances) != 2 || balances[0]["totalBalance"] != "100" || balances[1]["coinType"] != "0x2::foo::FOO" {
		t.Fatalf("unexpected all balances result: %+v", balances)
	}
	if _, ok := balances[0]["lockedBalance"]; ok {
		t.Fatalf("lockedBalance should be omitted: %+v", balances[0])
	}
	if got := state.listBalancesRequest.GetOwner(); got != "0xowner" {
		t.Fatalf("all balances owner = %q, want 0xowner", got)
	}
}

func TestTotalSupply(t *testing.T) {
	state := &fakeStateClient{
		coinInfoResponse: &rpcv2.GetCoinInfoResponse{
			Treasury: &rpcv2.CoinTreasury{TotalSupply: ptr(uint64(12023692))},
		},
	}
	backend := &SuiBackend{state: state}

	result, err := backend.totalSupply(context.Background(), json.RawMessage(`["0x2::sui::SUI"]`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, map[string]string{"value": "12023692"}) {
		t.Fatalf("totalSupply = %+v", result)
	}
}

func TestLegacyObjectChangesClassifiesOfficialVariants(t *testing.T) {
	sender := "0xsender"
	ownerA := &rpcv2.Owner{Kind: ptr(rpcv2.Owner_ADDRESS), Address: ptr("0xa")}
	ownerB := &rpcv2.Owner{Kind: ptr(rpcv2.Owner_ADDRESS), Address: ptr("0xb")}
	transaction := &rpcv2.ExecutedTransaction{
		Transaction: &rpcv2.Transaction{Sender: &sender},
		Effects: &rpcv2.TransactionEffects{
			ChangedObjects: []*rpcv2.ChangedObject{
				{
					ObjectId: ptr("0xcreated"), ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"),
					IdOperation:   ptr(rpcv2.ChangedObject_CREATED),
					OutputState:   ptr(rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_OBJECT_WRITE),
					OutputVersion: ptr(uint64(2)), OutputDigest: ptr("created-digest"), OutputOwner: ownerA,
				},
				{
					ObjectId: ptr("0xtransferred"), ObjectType: ptr("0x2::example::NFT"),
					IdOperation: ptr(rpcv2.ChangedObject_NONE),
					OutputState: ptr(rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_OBJECT_WRITE),
					InputOwner:  ownerA, OutputOwner: ownerB,
					InputVersion: ptr(uint64(2)), OutputVersion: ptr(uint64(3)), OutputDigest: ptr("transferred-digest"),
				},
				{
					ObjectId: ptr("0xdeleted"), ObjectType: ptr("0x2::example::Deleted"),
					IdOperation:  ptr(rpcv2.ChangedObject_DELETED),
					OutputState:  ptr(rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_DOES_NOT_EXIST),
					InputVersion: ptr(uint64(4)),
				},
				{
					ObjectId: ptr("0xwrapped"), ObjectType: ptr("0x2::example::Wrapped"),
					IdOperation:  ptr(rpcv2.ChangedObject_NONE),
					OutputState:  ptr(rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_DOES_NOT_EXIST),
					InputVersion: ptr(uint64(5)),
				},
				{
					ObjectId:      ptr("0xpackage"),
					OutputState:   ptr(rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_PACKAGE_WRITE),
					OutputVersion: ptr(uint64(6)), OutputDigest: ptr("package-digest"),
				},
			},
		},
	}

	changes := legacyObjectChanges(transaction, map[string][]string{"0xpackage": []string{"my_module"}})
	types := make([]string, 0, len(changes))
	for _, item := range changes {
		types = append(types, item.(map[string]any)["type"].(string))
	}
	want := []string{"created", "transferred", "deleted", "wrapped", "published"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("object change types = %v, want %v; changes = %+v", types, want, changes)
	}
	transferred := changes[1].(map[string]any)
	if transferred["recipient"] == nil || transferred["owner"] != nil {
		t.Fatalf("transferred change should use recipient, got %+v", transferred)
	}
	published := changes[4].(map[string]any)
	if !reflect.DeepEqual(published["modules"], []string{"my_module"}) {
		t.Fatalf("published modules = %v, want module names", published["modules"])
	}
}

func TestPublishedPackageModulesFetchesModuleNames(t *testing.T) {
	packages := &fakePackageClient{
		response: &rpcv2.GetPackageResponse{
			Package: &rpcv2.Package{
				Modules: []*rpcv2.Module{
					{Name: ptr("module_a")},
					{Name: ptr("module_b")},
				},
			},
		},
	}
	backend := &SuiBackend{packages: packages}
	transaction := &rpcv2.ExecutedTransaction{
		Effects: &rpcv2.TransactionEffects{
			ChangedObjects: []*rpcv2.ChangedObject{
				{
					ObjectId:    ptr("0xpackage"),
					OutputState: ptr(rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_PACKAGE_WRITE),
				},
			},
		},
	}

	modules, err := backend.publishedPackageModules(
		context.Background(), transaction, transactionOptions{ShowObjectChanges: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if packages.request.GetPackageId() != "0xpackage" {
		t.Fatalf("package request id = %q, want 0xpackage", packages.request.GetPackageId())
	}
	if !reflect.DeepEqual(modules["0xpackage"], []string{"module_a", "module_b"}) {
		t.Fatalf("modules = %v, want module names", modules["0xpackage"])
	}
}

func TestLegacyEffectsClassifiesWrappedObjects(t *testing.T) {
	owner := &rpcv2.Owner{Kind: ptr(rpcv2.Owner_ADDRESS), Address: ptr("0xa")}
	effects := &rpcv2.TransactionEffects{
		ChangedObjects: []*rpcv2.ChangedObject{
			{
				ObjectId:      ptr("0xunwrapped"),
				InputState:    ptr(rpcv2.ChangedObject_INPUT_OBJECT_STATE_DOES_NOT_EXIST),
				OutputState:   ptr(rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_OBJECT_WRITE),
				OutputVersion: ptr(uint64(2)), OutputDigest: ptr("unwrapped-digest"), OutputOwner: owner,
			},
			{
				ObjectId:     ptr("0xwrapped"),
				InputState:   ptr(rpcv2.ChangedObject_INPUT_OBJECT_STATE_EXISTS),
				InputVersion: ptr(uint64(3)), InputDigest: ptr("wrapped-digest"),
				OutputState: ptr(rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_DOES_NOT_EXIST),
			},
			{
				ObjectId:     ptr("0xunwrapped-deleted"),
				InputState:   ptr(rpcv2.ChangedObject_INPUT_OBJECT_STATE_DOES_NOT_EXIST),
				InputVersion: ptr(uint64(4)), InputDigest: ptr("unwrapped-deleted-digest"),
				OutputState: ptr(rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_DOES_NOT_EXIST),
				IdOperation: ptr(rpcv2.ChangedObject_DELETED),
			},
		},
	}

	legacy := legacyEffects(effects, &rpcv2.Transaction{})
	if legacy["unwrapped"] == nil || legacy["wrapped"] == nil || legacy["unwrapped_then_deleted"] == nil {
		t.Fatalf("effects missing wrapped classifications: %+v", legacy)
	}
}

func TestLegacyBalanceChangesUsesChangedObjectOwner(t *testing.T) {
	objectOwner := &rpcv2.Owner{Kind: ptr(rpcv2.Owner_OBJECT), Address: ptr("0xobject-owner")}
	transaction := &rpcv2.ExecutedTransaction{
		BalanceChanges: []*rpcv2.BalanceChange{
			{Address: ptr("0xobject-owner"), CoinType: ptr(defaultCoinType), Amount: ptr("5")},
		},
		Effects: &rpcv2.TransactionEffects{
			ChangedObjects: []*rpcv2.ChangedObject{
				{
					ObjectId:    ptr("0xcoin"),
					ObjectType:  ptr("0x2::coin::Coin<0x2::sui::SUI>"),
					OutputOwner: objectOwner,
				},
			},
		},
	}

	changes, err := legacyBalanceChanges(transaction)
	if err != nil {
		t.Fatal(err)
	}
	owner := changes[0].(map[string]any)["owner"]
	if !reflect.DeepEqual(owner, map[string]string{"ObjectOwner": "0xobject-owner"}) {
		t.Fatalf("balance owner = %+v, want object owner", owner)
	}
}

func TestLegacyBalanceChangesRejectsUnmappedOwner(t *testing.T) {
	transaction := &rpcv2.ExecutedTransaction{
		BalanceChanges: []*rpcv2.BalanceChange{
			{Address: ptr("0xowner"), CoinType: ptr(defaultCoinType), Amount: ptr("5")},
		},
		Effects: &rpcv2.TransactionEffects{},
	}

	_, err := legacyBalanceChanges(transaction)
	rpcErr, ok := err.(*RPCError)
	if !ok || rpcErr.Code != legacyIncompatible {
		t.Fatalf("legacyBalanceChanges() error = %+v, want legacy incompatible", err)
	}
}

func TestLegacyBalanceChangesAllowsMultipleCoinsForSameOwner(t *testing.T) {
	owner := &rpcv2.Owner{Kind: ptr(rpcv2.Owner_ADDRESS), Address: ptr("0xowner")}
	transaction := &rpcv2.ExecutedTransaction{
		BalanceChanges: []*rpcv2.BalanceChange{
			{Address: ptr("0xowner"), CoinType: ptr(defaultCoinType), Amount: ptr("5")},
		},
		Effects: &rpcv2.TransactionEffects{
			ChangedObjects: []*rpcv2.ChangedObject{
				{ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"), OutputOwner: owner},
				{ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"), OutputOwner: owner},
			},
		},
	}

	changes, err := legacyBalanceChanges(transaction)
	if err != nil {
		t.Fatal(err)
	}
	ownerResult := changes[0].(map[string]any)["owner"]
	if !reflect.DeepEqual(ownerResult, map[string]string{"AddressOwner": "0xowner"}) {
		t.Fatalf("balance owner = %+v, want address owner", ownerResult)
	}
}

func TestLegacyBalanceChangesRejectsAmbiguousOwner(t *testing.T) {
	addressOwner := &rpcv2.Owner{Kind: ptr(rpcv2.Owner_ADDRESS), Address: ptr("0xowner")}
	objectOwner := &rpcv2.Owner{Kind: ptr(rpcv2.Owner_OBJECT), Address: ptr("0xowner")}
	transaction := &rpcv2.ExecutedTransaction{
		BalanceChanges: []*rpcv2.BalanceChange{
			{Address: ptr("0xowner"), CoinType: ptr(defaultCoinType), Amount: ptr("5")},
		},
		Effects: &rpcv2.TransactionEffects{
			ChangedObjects: []*rpcv2.ChangedObject{
				{ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"), OutputOwner: addressOwner},
				{ObjectType: ptr("0x2::coin::Coin<0x2::sui::SUI>"), OutputOwner: objectOwner},
			},
		},
	}

	_, err := legacyBalanceChanges(transaction)
	rpcErr, ok := err.(*RPCError)
	if !ok || rpcErr.Code != legacyIncompatible {
		t.Fatalf("legacyBalanceChanges() error = %+v, want legacy incompatible", err)
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

type fakePackageClient struct {
	request  *rpcv2.GetPackageRequest
	response *rpcv2.GetPackageResponse
}

func (f *fakePackageClient) GetPackage(
	_ context.Context,
	request *rpcv2.GetPackageRequest,
	_ ...grpc.CallOption,
) (*rpcv2.GetPackageResponse, error) {
	f.request = request
	return f.response, nil
}

func (f *fakePackageClient) GetDatatype(
	context.Context,
	*rpcv2.GetDatatypeRequest,
	...grpc.CallOption,
) (*rpcv2.GetDatatypeResponse, error) {
	return nil, nil
}

func (f *fakePackageClient) GetFunction(
	context.Context,
	*rpcv2.GetFunctionRequest,
	...grpc.CallOption,
) (*rpcv2.GetFunctionResponse, error) {
	return nil, nil
}

func (f *fakePackageClient) ListPackageVersions(
	context.Context,
	*rpcv2.ListPackageVersionsRequest,
	...grpc.CallOption,
) (*rpcv2.ListPackageVersionsResponse, error) {
	return nil, nil
}

type fakeStateClient struct {
	balanceResponse           *rpcv2.GetBalanceResponse
	balanceRequest            *rpcv2.GetBalanceRequest
	coinInfoResponse          *rpcv2.GetCoinInfoResponse
	listBalancesResponses     []*rpcv2.ListBalancesResponse
	listBalancesCalls         int
	listBalancesRequest       *rpcv2.ListBalancesRequest
	listOwnedObjectsResponses []*rpcv2.ListOwnedObjectsResponse
	listOwnedObjectsCalls     int
	listOwnedObjectsRequests  []*rpcv2.ListOwnedObjectsRequest
}

func (f *fakeStateClient) ListDynamicFields(
	context.Context,
	*rpcv2.ListDynamicFieldsRequest,
	...grpc.CallOption,
) (*rpcv2.ListDynamicFieldsResponse, error) {
	return nil, nil
}

func (f *fakeStateClient) ListOwnedObjects(
	_ context.Context,
	arguments *rpcv2.ListOwnedObjectsRequest,
	_ ...grpc.CallOption,
) (*rpcv2.ListOwnedObjectsResponse, error) {
	f.listOwnedObjectsCalls++
	f.listOwnedObjectsRequests = append(f.listOwnedObjectsRequests, arguments)
	if len(f.listOwnedObjectsResponses) >= f.listOwnedObjectsCalls {
		return f.listOwnedObjectsResponses[f.listOwnedObjectsCalls-1], nil
	}
	return &rpcv2.ListOwnedObjectsResponse{}, nil
}

func (f *fakeStateClient) GetCoinInfo(
	context.Context,
	*rpcv2.GetCoinInfoRequest,
	...grpc.CallOption,
) (*rpcv2.GetCoinInfoResponse, error) {
	return f.coinInfoResponse, nil
}

func (f *fakeStateClient) GetBalance(
	_ context.Context,
	arguments *rpcv2.GetBalanceRequest,
	_ ...grpc.CallOption,
) (*rpcv2.GetBalanceResponse, error) {
	f.balanceRequest = arguments
	return f.balanceResponse, nil
}

func (f *fakeStateClient) ListBalances(
	_ context.Context,
	arguments *rpcv2.ListBalancesRequest,
	_ ...grpc.CallOption,
) (*rpcv2.ListBalancesResponse, error) {
	f.listBalancesCalls++
	f.listBalancesRequest = arguments
	if len(f.listBalancesResponses) >= f.listBalancesCalls {
		return f.listBalancesResponses[f.listBalancesCalls-1], nil
	}
	return &rpcv2.ListBalancesResponse{}, nil
}
