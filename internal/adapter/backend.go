package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	rpcv2 "sui-adapter/internal/suiv2"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const (
	defaultCoinType = "0x2::sui::SUI"
	maxPageSize     = uint32(1000)
)

var moveAddressPattern = regexp.MustCompile(`0x[0-9a-fA-F]+`)

type SuiBackend struct {
	ledger    rpcv2.LedgerServiceClient
	state     rpcv2.StateServiceClient
	execution rpcv2.TransactionExecutionServiceClient
}

func NewSuiBackend(connection grpc.ClientConnInterface) *SuiBackend {
	return &SuiBackend{
		ledger:    rpcv2.NewLedgerServiceClient(connection),
		state:     rpcv2.NewStateServiceClient(connection),
		execution: rpcv2.NewTransactionExecutionServiceClient(connection),
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
	case "suix_getAllCoins":
		return b.allCoins(ctx, params)
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
	return legacyTransaction(response.GetTransaction(), options), nil
}

func (b *SuiBackend) executeTransaction(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 2, 4)
	if err != nil {
		return nil, err
	}
	var transactionB64 string
	var signaturesB64 []string
	if json.Unmarshal(values[0], &transactionB64) != nil ||
		json.Unmarshal(values[1], &signaturesB64) != nil {
		return nil, invalidParamsError()
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
	result := legacyTransaction(response.GetTransaction(), options)
	result["confirmedLocalExecution"] = true
	return result, nil
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
	return legacyDryRunTransaction(response.GetTransaction()), nil
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
		Owner: &owner, CoinType: &coinType,
	})
	if err != nil {
		return nil, err
	}
	totalBalance := response.GetBalance().GetBalance()
	count, objectBalance, err := b.coinStats(ctx, owner, coinType)
	if err != nil {
		return nil, err
	}
	addressBalance := uint64(0)
	if totalBalance >= objectBalance {
		addressBalance = totalBalance - objectBalance
	}
	return map[string]any{
		"coinType":              coinType,
		"coinObjectCount":       count,
		"totalBalance":          strconv.FormatUint(totalBalance, 10),
		"lockedBalance":         map[string]string{},
		"fundsInAddressBalance": strconv.FormatUint(addressBalance, 10),
	}, nil
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

func (b *SuiBackend) allCoins(ctx context.Context, params json.RawMessage) (any, error) {
	values, err := positionalParams(params, 1, 3)
	if err != nil {
		return nil, err
	}
	var owner string
	if json.Unmarshal(values[0], &owner) != nil || owner == "" {
		return nil, invalidParamsError()
	}
	var pageToken []byte
	if len(values) >= 2 && !isNull(values[1]) {
		var cursor string
		if json.Unmarshal(values[1], &cursor) != nil {
			return nil, invalidParamsError()
		}
		pageToken, err = base64.StdEncoding.DecodeString(cursor)
		if err != nil {
			return nil, invalidParamsError()
		}
	}
	limit := uint32(50)
	if len(values) == 3 && !isNull(values[2]) {
		if json.Unmarshal(values[2], &limit) != nil || limit == 0 || limit > maxPageSize {
			return nil, invalidParamsError()
		}
	}
	objectType := "0x2::coin::Coin"
	response, err := b.state.ListOwnedObjects(ctx, &rpcv2.ListOwnedObjectsRequest{
		Owner: &owner, ObjectType: &objectType, PageSize: &limit, PageToken: pageToken,
		ReadMask: &fieldmaskpb.FieldMask{Paths: []string{
			"object_id", "version", "digest", "object_type", "balance", "previous_transaction",
		}},
	})
	if err != nil {
		return nil, err
	}
	data := make([]map[string]any, 0, len(response.GetObjects()))
	for _, object := range response.GetObjects() {
		data = append(data, map[string]any{
			"coinType":            coinTypeFromObjectType(object.GetObjectType()),
			"coinObjectId":        object.GetObjectId(),
			"version":             strconv.FormatUint(object.GetVersion(), 10),
			"digest":              object.GetDigest(),
			"balance":             strconv.FormatUint(object.GetBalance(), 10),
			"previousTransaction": object.GetPreviousTransaction(),
		})
	}
	var nextCursor any
	if len(response.GetNextPageToken()) > 0 {
		nextCursor = base64.StdEncoding.EncodeToString(response.GetNextPageToken())
	}
	return map[string]any{
		"data": data, "nextCursor": nextCursor,
		"hasNextPage": len(response.GetNextPageToken()) > 0,
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

func (b *SuiBackend) coinStats(ctx context.Context, owner, coinType string) (uint64, uint64, error) {
	objectType := "0x2::coin::Coin<" + coinType + ">"
	var count uint64
	var balance uint64
	var pageToken []byte
	for {
		response, err := b.state.ListOwnedObjects(ctx, &rpcv2.ListOwnedObjectsRequest{
			Owner: &owner, ObjectType: &objectType, PageSize: ptr(maxPageSize), PageToken: pageToken,
			ReadMask: &fieldmaskpb.FieldMask{Paths: []string{"object_id", "balance"}},
		})
		if err != nil {
			return 0, 0, err
		}
		count += uint64(len(response.GetObjects()))
		for _, object := range response.GetObjects() {
			if ^uint64(0)-balance < object.GetBalance() {
				return 0, 0, fmt.Errorf("coin balance overflow")
			}
			balance += object.GetBalance()
		}
		pageToken = response.GetNextPageToken()
		if len(pageToken) == 0 {
			return count, balance, nil
		}
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
