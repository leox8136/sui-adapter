package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	rpcv2 "sui-adapter/internal/suiv2"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const (
	defaultCoinType       = "0x2::sui::SUI"
	waitForEffectsCert    = "WaitForEffectsCert"
	waitForLocalExecution = "WaitForLocalExecution"
	localExecutionTimeout = 5 * time.Second
	localExecutionPoll    = 200 * time.Millisecond
	maxPageSize           = uint32(1000)
)

var moveAddressPattern = regexp.MustCompile(`0x[0-9a-fA-F]+`)

type SuiBackend struct {
	ledger    rpcv2.LedgerServiceClient
	state     rpcv2.StateServiceClient
	execution rpcv2.TransactionExecutionServiceClient
	packages  rpcv2.MovePackageServiceClient
}

func NewSuiBackend(connection grpc.ClientConnInterface) *SuiBackend {
	return &SuiBackend{
		ledger:    rpcv2.NewLedgerServiceClient(connection),
		state:     rpcv2.NewStateServiceClient(connection),
		execution: rpcv2.NewTransactionExecutionServiceClient(connection),
		packages:  rpcv2.NewMovePackageServiceClient(connection),
	}
}

func (b *SuiBackend) Call(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "sui_getLatestCheckpointSequenceNumber":
		return b.latestCheckpoint(ctx, params)
	case "sui_getCheckpoint":
		return b.checkpoint(ctx, params)
	case "sui_getTransactionBlock":
		return b.transaction(ctx, params)
	case "sui_executeTransactionBlock":
		return b.executeTransaction(ctx, params)
	case "suix_getBalance":
		return b.balance(ctx, params)
	case "suix_getCoinMetadata":
		return b.coinMetadata(ctx, params)
	case "suix_getCoins":
		return b.coins(ctx, params)
	case "suix_getAllCoins":
		return b.allCoins(ctx, params)
	case "suix_getAllBalances":
		return b.allBalances(ctx, params)
	case "suix_getTotalSupply":
		return b.totalSupply(ctx, params)
	case "suix_getReferenceGasPrice":
		return b.referenceGasPrice(ctx, params)
	case "sui_dryRunTransactionBlock":
		return b.dryRunTransaction(ctx, params)
	default:
		return nil, &RPCError{Code: methodNotFound, Message: "Method not found"}
	}
}

func (b *SuiBackend) latestCheckpoint(ctx context.Context, params json.RawMessage) (any, error) {
	if !validEmptyParams(params) {
		return nil, invalidParamsError()
	}
	response, err := b.ledger.GetCheckpoint(ctx, &rpcv2.GetCheckpointRequest{
		ReadMask: &fieldmaskpb.FieldMask{Paths: []string{"sequence_number"}},
	})
	if err != nil {
		return nil, err
	}
	if response.GetCheckpoint() == nil || response.GetCheckpoint().SequenceNumber == nil {
		return nil, fmt.Errorf("checkpoint sequence number missing")
	}
	return strconv.FormatUint(response.GetCheckpoint().GetSequenceNumber(), 10), nil
}

func (b *SuiBackend) checkpoint(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 1)
	if err != nil {
		return nil, err
	}
	var checkpointID string
	if json.Unmarshal(values[0], &checkpointID) != nil || checkpointID == "" {
		return nil, invalidParamsError()
	}

	request := &rpcv2.GetCheckpointRequest{
		ReadMask: &fieldmaskpb.FieldMask{Paths: []string{
			"sequence_number", "digest", "summary", "signature", "contents.transactions",
		}},
	}
	if sequence, parseErr := strconv.ParseUint(checkpointID, 10, 64); parseErr == nil {
		request.CheckpointId = &rpcv2.GetCheckpointRequest_SequenceNumber{SequenceNumber: sequence}
	} else {
		request.CheckpointId = &rpcv2.GetCheckpointRequest_Digest{Digest: checkpointID}
	}

	response, err := b.ledger.GetCheckpoint(ctx, request)
	if err != nil {
		return nil, err
	}
	checkpoint := response.GetCheckpoint()
	if checkpoint == nil || checkpoint.GetSummary() == nil {
		return nil, fmt.Errorf("checkpoint data missing")
	}
	return legacyCheckpoint(checkpoint), nil
}

func (b *SuiBackend) transaction(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 2)
	if err != nil {
		return nil, err
	}
	var digest string
	if json.Unmarshal(values[0], &digest) != nil || digest == "" {
		return nil, invalidParamsError()
	}
	options := transactionOptions{}
	if len(values) == 2 && !isNull(values[1]) {
		if json.Unmarshal(values[1], &options) != nil {
			return nil, invalidParamsError()
		}
	}

	response, err := b.ledger.GetTransaction(ctx, &rpcv2.GetTransactionRequest{
		Digest:   &digest,
		ReadMask: transactionReadMask(options),
	})
	if err != nil {
		return nil, err
	}
	if response.GetTransaction() == nil {
		return nil, fmt.Errorf("transaction data missing")
	}
	publishedModules, err := b.publishedPackageModules(ctx, response.GetTransaction(), options)
	if err != nil {
		return nil, err
	}
	return legacyTransaction(response.GetTransaction(), options, publishedModules)
}

func (b *SuiBackend) executeTransaction(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 2, 4)
	if err != nil {
		return nil, err
	}
	var transactionB64 string
	if json.Unmarshal(values[0], &transactionB64) != nil {
		return nil, invalidParamsError()
	}
	signaturesB64, err := transactionSignatures(values[1])
	if err != nil {
		return nil, err
	}
	transactionBytes, err := base64.StdEncoding.DecodeString(transactionB64)
	if err != nil {
		return nil, invalidParamsError()
	}
	signatures := make([]*rpcv2.UserSignature, len(signaturesB64))
	for index, signature := range signaturesB64 {
		decoded, decodeErr := base64.StdEncoding.DecodeString(signature)
		if decodeErr != nil {
			return nil, invalidParamsError()
		}
		signatures[index] = &rpcv2.UserSignature{Bcs: &rpcv2.Bcs{Value: decoded}}
	}
	options := transactionOptions{}
	if len(values) >= 3 && !isNull(values[2]) {
		if json.Unmarshal(values[2], &options) != nil {
			return nil, invalidParamsError()
		}
	}
	requestType := waitForEffectsCert
	if len(values) == 4 && !isNull(values[3]) {
		if json.Unmarshal(values[3], &requestType) != nil ||
			(requestType != waitForEffectsCert && requestType != waitForLocalExecution) {
			return nil, invalidParamsError()
		}
	}
	response, err := b.execution.ExecuteTransaction(ctx, &rpcv2.ExecuteTransactionRequest{
		Transaction: &rpcv2.Transaction{Bcs: &rpcv2.Bcs{Value: transactionBytes}},
		Signatures:  signatures,
		ReadMask:    transactionReadMask(options),
	})
	if err != nil {
		return nil, err
	}
	if response.GetTransaction() == nil {
		return nil, fmt.Errorf("executed transaction data missing")
	}
	publishedModules, err := b.publishedPackageModules(ctx, response.GetTransaction(), options)
	if err != nil {
		return nil, err
	}
	result, err := legacyTransaction(response.GetTransaction(), options, publishedModules)
	if err != nil {
		return nil, err
	}
	if requestType == waitForLocalExecution {
		result["confirmedLocalExecution"] = b.confirmLocalExecution(ctx, response.GetTransaction().GetDigest())
	}
	return result, nil
}

func (b *SuiBackend) confirmLocalExecution(ctx context.Context, digest string) bool {
	if digest == "" || b.ledger == nil {
		return false
	}

	confirmationContext, cancel := context.WithTimeout(ctx, localExecutionTimeout)
	defer cancel()

	ticker := time.NewTicker(localExecutionPoll)
	defer ticker.Stop()
	for {
		response, err := b.ledger.GetTransaction(confirmationContext, &rpcv2.GetTransactionRequest{
			Digest:   &digest,
			ReadMask: &fieldmaskpb.FieldMask{Paths: []string{"digest"}},
		})
		if err == nil && response.GetTransaction() != nil {
			return response.GetTransaction().GetDigest() == digest
		}
		if err != nil {
			code := status.Code(err)
			if code != codes.NotFound && code != codes.Unavailable && code != codes.DeadlineExceeded {
				return false
			}
		}

		select {
		case <-confirmationContext.Done():
			return false
		case <-ticker.C:
		}
	}
}

func (b *SuiBackend) dryRunTransaction(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 1)
	if err != nil {
		return nil, err
	}
	var transactionB64 string
	if json.Unmarshal(values[0], &transactionB64) != nil {
		return nil, invalidParamsError()
	}
	transactionBytes, err := base64.StdEncoding.DecodeString(transactionB64)
	if err != nil {
		return nil, invalidParamsError()
	}
	response, err := b.execution.SimulateTransaction(ctx, &rpcv2.SimulateTransactionRequest{
		Transaction: &rpcv2.Transaction{Bcs: &rpcv2.Bcs{Value: transactionBytes}},
		ReadMask:    &fieldmaskpb.FieldMask{Paths: []string{"*"}},
	})
	if err != nil {
		return nil, err
	}
	if response.GetTransaction() == nil {
		return nil, fmt.Errorf("simulated transaction data missing")
	}
	return legacyDryRunTransaction(response.GetTransaction())
}

func (b *SuiBackend) balance(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 2)
	if err != nil {
		return nil, err
	}
	var owner string
	if json.Unmarshal(values[0], &owner) != nil || owner == "" {
		return nil, invalidParamsError()
	}
	coinType := defaultCoinType
	if len(values) == 2 && !isNull(values[1]) {
		if json.Unmarshal(values[1], &coinType) != nil || coinType == "" {
			return nil, invalidParamsError()
		}
	}
	response, err := b.state.GetBalance(ctx, &rpcv2.GetBalanceRequest{
		Owner:    &owner,
		CoinType: &coinType,
	})
	if err != nil {
		return nil, err
	}
	return legacyBalance(response.GetBalance()), nil
}

func (b *SuiBackend) coinMetadata(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 1)
	if err != nil {
		return nil, err
	}
	var coinType string
	if json.Unmarshal(values[0], &coinType) != nil || coinType == "" {
		return nil, invalidParamsError()
	}
	response, err := b.state.GetCoinInfo(ctx, &rpcv2.GetCoinInfoRequest{CoinType: &coinType})
	if err != nil {
		return nil, err
	}
	metadata := response.GetMetadata()
	if metadata == nil {
		return nil, nil
	}
	return map[string]any{
		"id":          metadata.GetId(),
		"decimals":    metadata.GetDecimals(),
		"name":        metadata.GetName(),
		"symbol":      metadata.GetSymbol(),
		"description": metadata.GetDescription(),
		"iconUrl":     metadata.GetIconUrl(),
	}, nil
}

func (b *SuiBackend) coins(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 4)
	if err != nil {
		return nil, err
	}
	owner, coinType, cursor, pageToken, limit, err := coinPageParams(values)
	if err != nil {
		return nil, err
	}
	objectType := "0x2::coin::Coin<" + coinType + ">"
	return b.listCoins(ctx, owner, objectType, cursor, pageToken, limit)
}

func (b *SuiBackend) allCoins(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 3)
	if err != nil {
		return nil, err
	}
	owner, cursor, pageToken, limit, err := allCoinPageParams(values)
	if err != nil {
		return nil, err
	}
	return b.listCoins(ctx, owner, "0x2::coin::Coin", cursor, pageToken, limit)
}

func (b *SuiBackend) allBalances(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 1)
	if err != nil {
		return nil, err
	}
	var owner string
	if json.Unmarshal(values[0], &owner) != nil || owner == "" {
		return nil, invalidParamsError()
	}
	response, err := b.state.ListBalances(ctx, &rpcv2.ListBalancesRequest{Owner: &owner})
	if err != nil {
		return nil, err
	}
	balances := make([]map[string]any, 0, len(response.GetBalances()))
	for _, balance := range response.GetBalances() {
		balances = append(balances, legacyBalance(balance))
	}
	return balances, nil
}

func legacyBalance(balance *rpcv2.Balance) map[string]any {
	return map[string]any{
		"coinType":     balance.GetCoinType(),
		"totalBalance": strconv.FormatUint(balance.GetBalance(), 10),
	}
}

func (b *SuiBackend) totalSupply(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 1)
	if err != nil {
		return nil, err
	}
	var coinType string
	if json.Unmarshal(values[0], &coinType) != nil || coinType == "" {
		return nil, invalidParamsError()
	}
	response, err := b.state.GetCoinInfo(ctx, &rpcv2.GetCoinInfoRequest{CoinType: &coinType})
	if err != nil {
		return nil, err
	}
	if response.GetTreasury() == nil {
		return nil, fmt.Errorf("coin treasury data missing")
	}
	return map[string]string{
		"value": strconv.FormatUint(response.GetTreasury().GetTotalSupply(), 10),
	}, nil
}

func (b *SuiBackend) referenceGasPrice(ctx context.Context, params json.RawMessage) (any, error) {
	if !validEmptyParams(params) {
		return nil, invalidParamsError()
	}
	response, err := b.ledger.GetEpoch(ctx, &rpcv2.GetEpochRequest{
		ReadMask: &fieldmaskpb.FieldMask{Paths: []string{"reference_gas_price"}},
	})
	if err != nil {
		return nil, err
	}
	if response.GetEpoch() == nil || response.GetEpoch().ReferenceGasPrice == nil {
		return nil, fmt.Errorf("reference gas price missing")
	}
	return strconv.FormatUint(response.GetEpoch().GetReferenceGasPrice(), 10), nil
}

func transactionSignatures(raw json.RawMessage) ([]string, error) {
	var signatures []string
	if json.Unmarshal(raw, &signatures) == nil {
		return signatures, nil
	}
	var signature string
	if json.Unmarshal(raw, &signature) != nil || signature == "" {
		return nil, invalidParamsError()
	}
	return []string{signature}, nil
}

func (b *SuiBackend) publishedPackageModules(
	ctx context.Context,
	transaction *rpcv2.ExecutedTransaction,
	options transactionOptions,
) (map[string][]string, error) {
	if !options.ShowObjectChanges || b.packages == nil || transaction.GetEffects() == nil {
		return nil, nil
	}
	result := map[string][]string{}
	for _, object := range transaction.GetEffects().GetChangedObjects() {
		if object.GetOutputState() != rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_PACKAGE_WRITE {
			continue
		}
		packageID := object.GetObjectId()
		response, err := b.packages.GetPackage(ctx, &rpcv2.GetPackageRequest{PackageId: &packageID})
		if err != nil {
			return nil, err
		}
		modules := make([]string, 0, len(response.GetPackage().GetModules()))
		for _, module := range response.GetPackage().GetModules() {
			if module.GetName() != "" {
				modules = append(modules, module.GetName())
			}
		}
		result[packageID] = modules
	}
	return result, nil
}

func (b *SuiBackend) listCoins(
	ctx context.Context,
	owner string,
	objectType string,
	cursor string,
	pageToken []byte,
	limit uint32,
) (map[string]any, error) {
	readMask := &fieldmaskpb.FieldMask{Paths: []string{
		"object_id", "version", "digest", "object_type", "balance", "previous_transaction",
	}}
	data := make([]map[string]any, 0, limit)
	collecting := cursor == ""
	for {
		pageSize := maxPageSize
		if collecting {
			remaining := limit - uint32(len(data))
			if remaining < pageSize {
				pageSize = remaining
			}
		}
		response, err := b.state.ListOwnedObjects(ctx, &rpcv2.ListOwnedObjectsRequest{
			Owner: &owner, ObjectType: &objectType, PageSize: &pageSize, PageToken: pageToken,
			ReadMask: readMask,
		})
		if err != nil {
			return nil, err
		}
		objects := response.GetObjects()
		for index, object := range objects {
			if !collecting {
				if object.GetObjectId() == cursor {
					collecting = true
				}
				continue
			}
			data = append(data, legacyCoin(object))
			if uint32(len(data)) == limit {
				hasNextPage := index < len(objects)-1 || len(response.GetNextPageToken()) > 0
				var nextCursor any
				if hasNextPage {
					nextCursor = object.GetObjectId()
				}
				return map[string]any{
					"data": data, "nextCursor": nextCursor, "hasNextPage": hasNextPage,
				}, nil
			}
		}
		pageToken = response.GetNextPageToken()
		if len(pageToken) == 0 {
			if !collecting && cursor != "" {
				return nil, invalidParamsError()
			}
			return map[string]any{
				"data": data, "nextCursor": nil, "hasNextPage": false,
			}, nil
		}
	}
}

func coinPageParams(values []json.RawMessage) (string, string, string, []byte, uint32, error) {
	var owner string
	if json.Unmarshal(values[0], &owner) != nil || owner == "" {
		return "", "", "", nil, 0, invalidParamsError()
	}
	coinType := defaultCoinType
	if len(values) >= 2 && !isNull(values[1]) {
		if json.Unmarshal(values[1], &coinType) != nil || coinType == "" {
			return "", "", "", nil, 0, invalidParamsError()
		}
	}
	var cursor string
	var pageToken []byte
	if len(values) >= 3 && !isNull(values[2]) {
		if json.Unmarshal(values[2], &cursor) != nil {
			return "", "", "", nil, 0, invalidParamsError()
		}
		if cursor != "" && !strings.HasPrefix(cursor, "0x") {
			var err error
			pageToken, err = base64.StdEncoding.DecodeString(cursor)
			if err != nil {
				return "", "", "", nil, 0, invalidParamsError()
			}
			cursor = ""
		}
	}
	limit := uint32(50)
	if len(values) >= 4 && !isNull(values[3]) {
		if json.Unmarshal(values[3], &limit) != nil || limit == 0 || limit > maxPageSize {
			return "", "", "", nil, 0, invalidParamsError()
		}
	}
	return owner, coinType, cursor, pageToken, limit, nil
}

func allCoinPageParams(values []json.RawMessage) (string, string, []byte, uint32, error) {
	var owner string
	if json.Unmarshal(values[0], &owner) != nil || owner == "" {
		return "", "", nil, 0, invalidParamsError()
	}
	cursor, pageToken, err := cursorPageToken(values, 1)
	if err != nil {
		return "", "", nil, 0, err
	}
	limit, err := pageLimit(values, 2)
	if err != nil {
		return "", "", nil, 0, err
	}
	return owner, cursor, pageToken, limit, nil
}

func cursorPageToken(values []json.RawMessage, index int) (string, []byte, error) {
	if len(values) <= index || isNull(values[index]) {
		return "", nil, nil
	}
	var cursor string
	if json.Unmarshal(values[index], &cursor) != nil {
		return "", nil, invalidParamsError()
	}
	if cursor == "" || strings.HasPrefix(cursor, "0x") {
		return cursor, nil, nil
	}
	pageToken, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return "", nil, invalidParamsError()
	}
	return "", pageToken, nil
}

func pageLimit(values []json.RawMessage, index int) (uint32, error) {
	limit := uint32(50)
	if len(values) <= index || isNull(values[index]) {
		return limit, nil
	}
	if json.Unmarshal(values[index], &limit) != nil || limit == 0 || limit > maxPageSize {
		return 0, invalidParamsError()
	}
	return limit, nil
}

func legacyCoin(object *rpcv2.Object) map[string]any {
	return map[string]any{
		"coinType":            coinTypeFromObjectType(object.GetObjectType()),
		"coinObjectId":        object.GetObjectId(),
		"version":             strconv.FormatUint(object.GetVersion(), 10),
		"digest":              object.GetDigest(),
		"balance":             strconv.FormatUint(object.GetBalance(), 10),
		"previousTransaction": object.GetPreviousTransaction(),
	}
}

type transactionOptions struct {
	ShowInput          bool `json:"showInput"`
	ShowRawInput       bool `json:"showRawInput"`
	ShowEffects        bool `json:"showEffects"`
	ShowRawEffects     bool `json:"showRawEffects"`
	ShowEvents         bool `json:"showEvents"`
	ShowObjectChanges  bool `json:"showObjectChanges"`
	ShowBalanceChanges bool `json:"showBalanceChanges"`
}

func transactionReadMask(options transactionOptions) *fieldmaskpb.FieldMask {
	paths := []string{"digest", "signatures", "effects.status", "effects.epoch", "checkpoint", "timestamp"}
	if options.ShowInput {
		paths = append(paths, "transaction")
	} else if options.ShowRawInput {
		paths = append(paths, "transaction.bcs")
	}
	if options.ShowEffects || options.ShowRawEffects || options.ShowObjectChanges {
		paths = append(paths, "effects")
	}
	if options.ShowEvents {
		paths = append(paths, "events")
	}
	if options.ShowBalanceChanges {
		paths = append(paths, "balance_changes")
	}
	return &fieldmaskpb.FieldMask{Paths: paths}
}

func positionalParams(raw json.RawMessage, minimum, maximum int) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil ||
		len(values) < minimum || len(values) > maximum {
		return nil, invalidParamsError()
	}
	return values, nil
}

func invalidParamsError() *RPCError {
	return &RPCError{Code: invalidParams, Message: "Invalid params"}
}

func isNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func ptr[T any](value T) *T {
	return &value
}

func coinTypeFromObjectType(objectType string) string {
	start := strings.IndexByte(objectType, '<')
	if start >= 0 && strings.HasSuffix(objectType, ">") {
		return normalizeMoveType(objectType[start+1 : len(objectType)-1])
	}
	return normalizeMoveType(objectType)
}

func normalizeMoveType(value string) string {
	return moveAddressPattern.ReplaceAllStringFunc(value, func(address string) string {
		digits := strings.TrimLeft(address[2:], "0")
		if digits == "" {
			digits = "0"
		}
		return "0x" + digits
	})
}

func canonicalMoveType(value string) string {
	value = normalizeMoveType(value)
	value = strings.ReplaceAll(value, ", ", ",")
	return strings.ReplaceAll(value, ",", ", ")
}

func bytesToNumbers(value []byte) []int {
	result := make([]int, len(value))
	for index, item := range value {
		result[index] = int(item)
	}
	return result
}
