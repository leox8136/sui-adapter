package adapter

import (
	"encoding/base64"
	"strconv"
	"strings"

	rpcv2 "sui-adapter/internal/suiv2"
)

func legacyCheckpoint(checkpoint *rpcv2.Checkpoint) map[string]any {
	summary := checkpoint.GetSummary()
	transactions := make([]string, 0)
	if contents := checkpoint.GetContents(); contents != nil {
		for _, transaction := range contents.GetTransactions() {
			transactions = append(transactions, transaction.GetTransaction())
		}
	}

	result := map[string]any{
		"epoch":                      strconv.FormatUint(summary.GetEpoch(), 10),
		"sequenceNumber":             strconv.FormatUint(checkpoint.GetSequenceNumber(), 10),
		"digest":                     checkpoint.GetDigest(),
		"networkTotalTransactions":   strconv.FormatUint(summary.GetTotalNetworkTransactions(), 10),
		"epochRollingGasCostSummary": legacyGasSummary(summary.GetEpochRollingGasCostSummary()),
		"timestampMs":                timestampMillis(summary.GetTimestamp()),
		"transactions":               transactions,
		"checkpointCommitments":      legacyCommitments(summary.GetCommitments()),
		"validatorSignature":         legacyValidatorSignature(checkpoint.GetSignature()),
	}
	if summary.PreviousDigest != nil {
		result["previousDigest"] = summary.GetPreviousDigest()
	}
	if end := summary.GetEndOfEpochData(); end != nil {
		committee := make([][]string, 0, len(end.GetNextEpochCommittee()))
		for _, member := range end.GetNextEpochCommittee() {
			committee = append(committee, []string{
				base64.StdEncoding.EncodeToString(member.GetPublicKey()),
				strconv.FormatUint(member.GetWeight(), 10),
			})
		}
		result["endOfEpochData"] = map[string]any{
			"nextEpochCommittee":       committee,
			"nextEpochProtocolVersion": strconv.FormatUint(end.GetNextEpochProtocolVersion(), 10),
			"epochCommitments":         legacyCommitments(end.GetEpochCommitments()),
		}
	}
	return result
}

func legacyGasSummary(summary *rpcv2.GasCostSummary) map[string]string {
	if summary == nil {
		return map[string]string{
			"computationCost": "0", "storageCost": "0",
			"storageRebate": "0", "nonRefundableStorageFee": "0",
		}
	}
	return map[string]string{
		"computationCost":         strconv.FormatUint(summary.GetComputationCost(), 10),
		"storageCost":             strconv.FormatUint(summary.GetStorageCost(), 10),
		"storageRebate":           strconv.FormatUint(summary.GetStorageRebate(), 10),
		"nonRefundableStorageFee": strconv.FormatUint(summary.GetNonRefundableStorageFee(), 10),
	}
}

func legacyCommitments(commitments []*rpcv2.CheckpointCommitment) []any {
	result := make([]any, 0, len(commitments))
	for _, commitment := range commitments {
		if commitment.GetKind() != rpcv2.CheckpointCommitment_ECMH_LIVE_OBJECT_SET {
			continue
		}
		result = append(result, map[string]any{
			"ECMHLiveObjectSetDigest": map[string]string{"digest": commitment.GetDigest()},
		})
	}
	return result
}

func legacyValidatorSignature(signature *rpcv2.ValidatorAggregatedSignature) string {
	if signature == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(signature.GetSignature())
}

func timestampMillis(timestamp interface {
	GetSeconds() int64
	GetNanos() int32
}) string {
	if timestamp == nil {
		return "0"
	}
	millis := timestamp.GetSeconds()*1000 + int64(timestamp.GetNanos())/1_000_000
	return strconv.FormatInt(millis, 10)
}

func legacyTransaction(
	transaction *rpcv2.ExecutedTransaction,
	options transactionOptions,
	publishedModules map[string][]string,
	pureValues ...resolvedPureInputs,
) (map[string]any, error) {
	result := map[string]any{"digest": transaction.GetDigest()}
	if transaction.Checkpoint != nil {
		result["checkpoint"] = strconv.FormatUint(transaction.GetCheckpoint(), 10)
	}
	if transaction.Timestamp != nil {
		result["timestampMs"] = timestampMillis(transaction.GetTimestamp())
	}
	signatures := legacySignatures(transaction.GetSignatures())
	if options.ShowInput && transaction.GetTransaction() != nil {
		input, err := legacyTransactionInput(transaction.GetTransaction(), signatures, pureValues...)
		if err != nil {
			return nil, err
		}
		result["transaction"] = input
	}
	if options.ShowRawInput && transaction.GetTransaction().GetBcs() != nil {
		result["rawTransaction"] = legacyRawTransaction(
			transaction.GetTransaction().GetBcs().GetValue(), signatures,
		)
	}
	if options.ShowEffects && transaction.GetEffects() != nil {
		result["effects"] = legacyEffects(transaction.GetEffects(), transaction.GetTransaction())
	}
	if options.ShowRawEffects && transaction.GetEffects().GetBcs() != nil {
		result["rawEffects"] = bytesToNumbers(transaction.GetEffects().GetBcs().GetValue())
	}
	if options.ShowEvents {
		result["events"] = legacyEvents(transaction)
	}
	if options.ShowObjectChanges {
		result["objectChanges"] = legacyObjectChanges(transaction, publishedModules)
	}
	if options.ShowBalanceChanges {
		balanceChanges, err := legacyBalanceChanges(transaction)
		if err != nil {
			return nil, err
		}
		result["balanceChanges"] = balanceChanges
	}
	return result, nil
}

func legacyDryRunTransaction(transaction *rpcv2.ExecutedTransaction, pureValues ...resolvedPureInputs) (map[string]any, error) {
	options := transactionOptions{
		ShowInput: true, ShowEffects: true, ShowEvents: true,
		ShowObjectChanges: true, ShowBalanceChanges: true,
	}
	result, err := legacyTransaction(transaction, options, nil, pureValues...)
	if err != nil {
		return nil, err
	}
	delete(result, "digest")
	delete(result, "checkpoint")
	delete(result, "timestampMs")
	if input, ok := result["transaction"].(map[string]any); ok {
		result["input"] = input["data"]
	}
	delete(result, "transaction")
	if status := transaction.GetEffects().GetStatus(); status != nil && !status.GetSuccess() {
		result["executionErrorSource"] = status.GetError().GetDescription()
	}
	return result, nil
}

func legacySignatures(signatures []*rpcv2.UserSignature) []string {
	result := make([]string, 0, len(signatures))
	for _, signature := range signatures {
		if signature.GetBcs() != nil {
			result = append(result, base64.StdEncoding.EncodeToString(signature.GetBcs().GetValue()))
		}
	}
	return result
}

func legacyRawTransaction(transactionBCS []byte, signatures []string) string {
	data := []byte{1, 0, 0, 0}
	data = append(data, transactionBCS...)
	data = appendULEB128(data, uint64(len(signatures)))
	for _, signature := range signatures {
		decoded, err := base64.StdEncoding.DecodeString(signature)
		if err != nil {
			return base64.StdEncoding.EncodeToString(transactionBCS)
		}
		data = appendULEB128(data, uint64(len(decoded)))
		data = append(data, decoded...)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func appendULEB128(destination []byte, value uint64) []byte {
	for {
		next := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			next |= 0x80
		}
		destination = append(destination, next)
		if value == 0 {
			return destination
		}
	}
}

func legacyTransactionInput(transaction *rpcv2.Transaction, signatures []string, pureValues ...resolvedPureInputs) (map[string]any, error) {
	kind, err := legacyTransactionKind(transaction.GetKind(), pureValues...)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"data": map[string]any{
			"messageVersion": "v1",
			"transaction":    kind,
			"sender":         transaction.GetSender(),
			"gasData":        legacyProtoGasData(transaction.GetGasPayment()),
		},
		"txSignatures": signatures,
	}, nil
}

func legacyTransactionKind(kind *rpcv2.TransactionKind, pureValues ...resolvedPureInputs) (map[string]any, error) {
	if kind == nil {
		return map[string]any{}, nil
	}
	if programmable := kind.GetProgrammableTransaction(); programmable != nil {
		inputs := make([]any, 0, len(programmable.GetInputs()))
		for _, input := range programmable.GetInputs() {
			value, err := legacyInput(input, pureValues...)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, value)
		}
		commands := make([]any, 0, len(programmable.GetCommands()))
		for _, command := range programmable.GetCommands() {
			commands = append(commands, legacyCommand(command))
		}
		return map[string]any{
			"kind": "ProgrammableTransaction", "inputs": inputs, "transactions": commands,
		}, nil
	}
	if prologue := kind.GetConsensusCommitPrologue(); prologue != nil {
		return legacyConsensusCommitPrologue(kind.GetKind(), prologue), nil
	}
	return map[string]any{"kind": enumName(kind.GetKind().String())}, nil
}

func legacyConsensusCommitPrologue(
	kind rpcv2.TransactionKind_Kind,
	prologue *rpcv2.ConsensusCommitPrologue,
) map[string]any {
	result := map[string]any{
		"kind":                enumName(kind.String()),
		"epoch":               strconv.FormatUint(prologue.GetEpoch(), 10),
		"round":               strconv.FormatUint(prologue.GetRound(), 10),
		"commit_timestamp_ms": timestampMillis(prologue.GetCommitTimestamp()),
	}
	if prologue.ConsensusCommitDigest != nil {
		result["consensus_commit_digest"] = prologue.GetConsensusCommitDigest()
	}
	if prologue.SubDagIndex != nil {
		result["sub_dag_index"] = strconv.FormatUint(prologue.GetSubDagIndex(), 10)
	} else {
		result["sub_dag_index"] = nil
	}
	if assignments := prologue.GetConsensusDeterminedVersionAssignments(); assignments != nil {
		key := "CancelledTransactions"
		if assignments.GetVersion() == 2 {
			key = "CancelledTransactionsV2"
		}
		cancelled := make([]any, 0, len(assignments.GetCanceledTransactions()))
		for _, item := range assignments.GetCanceledTransactions() {
			versions := make([]map[string]any, 0, len(item.GetVersionAssignments()))
			for _, version := range item.GetVersionAssignments() {
				versions = append(versions, map[string]any{
					"object_id":     version.GetObjectId(),
					"start_version": strconv.FormatUint(version.GetStartVersion(), 10),
					"version":       strconv.FormatUint(version.GetVersion(), 10),
				})
			}
			cancelled = append(cancelled, map[string]any{
				"digest": item.GetDigest(), "version_assignments": versions,
			})
		}
		result["consensus_determined_version_assignments"] = map[string]any{key: cancelled}
	}
	if prologue.AdditionalStateDigest != nil {
		result["additional_state_digest"] = prologue.GetAdditionalStateDigest()
	}
	return result
}

func legacyInput(input *rpcv2.Input, pureValues ...resolvedPureInputs) (any, error) {
	switch input.GetKind() {
	case rpcv2.Input_PURE:
		if len(pureValues) > 0 {
			if value, ok := pureValues[0][input]; ok {
				return value, nil
			}
		}
		return legacyPureInput(input)
	case rpcv2.Input_IMMUTABLE_OR_OWNED:
		return map[string]any{
			"type": "object", "objectType": "immOrOwnedObject",
			"objectId": input.GetObjectId(),
			"version":  strconv.FormatUint(input.GetVersion(), 10),
			"digest":   input.GetDigest(),
		}, nil
	case rpcv2.Input_SHARED:
		return map[string]any{
			"type": "object", "objectType": "sharedObject",
			"objectId":             input.GetObjectId(),
			"initialSharedVersion": strconv.FormatUint(input.GetVersion(), 10),
			"mutable":              input.GetMutable(),
		}, nil
	case rpcv2.Input_RECEIVING:
		return map[string]any{
			"type": "object", "objectType": "receiving",
			"objectId": input.GetObjectId(),
			"version":  strconv.FormatUint(input.GetVersion(), 10),
			"digest":   input.GetDigest(),
		}, nil
	case rpcv2.Input_FUNDS_WITHDRAWAL:
		return legacyFundsWithdrawal(input.GetFundsWithdrawal())
	default:
		return nil, legacyIncompatibleError("transaction input kind cannot be represented in legacy JSON-RPC")
	}
}

func legacyFundsWithdrawal(value *rpcv2.FundsWithdrawal) (any, error) {
	if value == nil || value.Amount == nil || strings.TrimSpace(value.GetCoinType()) == "" {
		return nil, legacyIncompatibleError("funds withdrawal input is missing amount or coin type")
	}
	var source string
	switch value.GetSource() {
	case rpcv2.FundsWithdrawal_SENDER:
		source = "sender"
	case rpcv2.FundsWithdrawal_SPONSOR:
		source = "sponsor"
	default:
		return nil, legacyIncompatibleError("funds withdrawal source cannot be represented in legacy JSON-RPC")
	}
	// This is the reserved maximum, not the transaction's actual balance delta.
	return map[string]any{
		"type":         "fundsWithdrawal",
		"reservation":  map[string]any{"maxAmountU64": strconv.FormatUint(value.GetAmount(), 10)},
		"typeArg":      map[string]any{"balance": canonicalMoveType(value.GetCoinType())},
		"withdrawFrom": source,
	}, nil
}

// BCS is not self-describing, and Input.literal is input-only in gRPC.
// Missing layouts must never be inferred from byte length.
func legacyPureInput(_ *rpcv2.Input) (map[string]any, error) {
	return nil, legacyIncompatibleError("parsed pure input type could not be resolved from transaction commands")
}

func legacyCommand(command *rpcv2.Command) any {
	if value := command.GetMoveCall(); value != nil {
		typeArguments := make([]string, len(value.GetTypeArguments()))
		for index, item := range value.GetTypeArguments() {
			typeArguments[index] = canonicalMoveType(item)
		}
		return map[string]any{"MoveCall": map[string]any{
			"package": value.GetPackage(), "module": value.GetModule(),
			"function": value.GetFunction(), "type_arguments": typeArguments,
			"arguments": legacyArguments(value.GetArguments()),
		}}
	}
	if value := command.GetTransferObjects(); value != nil {
		return map[string]any{"TransferObjects": []any{
			legacyArguments(value.GetObjects()), legacyArgument(value.GetAddress()),
		}}
	}
	if value := command.GetSplitCoins(); value != nil {
		return map[string]any{"SplitCoins": []any{
			legacyArgument(value.GetCoin()), legacyArguments(value.GetAmounts()),
		}}
	}
	if value := command.GetMergeCoins(); value != nil {
		return map[string]any{"MergeCoins": []any{
			legacyArgument(value.GetCoin()), legacyArguments(value.GetCoinsToMerge()),
		}}
	}
	if value := command.GetPublish(); value != nil {
		modules := make([]string, len(value.GetModules()))
		for index, module := range value.GetModules() {
			modules[index] = base64.StdEncoding.EncodeToString(module)
		}
		return map[string]any{"Publish": []any{modules, value.GetDependencies()}}
	}
	if value := command.GetMakeMoveVector(); value != nil {
		var elementType any
		if value.ElementType != nil {
			elementType = canonicalMoveType(value.GetElementType())
		}
		return map[string]any{"MakeMoveVec": []any{
			elementType, legacyArguments(value.GetElements()),
		}}
	}
	if value := command.GetUpgrade(); value != nil {
		modules := make([]string, len(value.GetModules()))
		for index, module := range value.GetModules() {
			modules[index] = base64.StdEncoding.EncodeToString(module)
		}
		return map[string]any{"Upgrade": []any{
			modules, value.GetDependencies(), value.GetPackage(), legacyArgument(value.GetTicket()),
		}}
	}
	return map[string]any{}
}

func legacyArguments(arguments []*rpcv2.Argument) []any {
	result := make([]any, 0, len(arguments))
	for _, argument := range arguments {
		result = append(result, legacyArgument(argument))
	}
	return result
}

func legacyArgument(argument *rpcv2.Argument) any {
	if argument == nil {
		return nil
	}
	switch argument.GetKind() {
	case rpcv2.Argument_GAS:
		return "GasCoin"
	case rpcv2.Argument_INPUT:
		return map[string]uint32{"Input": argument.GetInput()}
	case rpcv2.Argument_RESULT:
		if argument.Subresult != nil {
			return map[string][]uint32{
				"NestedResult": {argument.GetResult(), argument.GetSubresult()},
			}
		}
		return map[string]uint32{"Result": argument.GetResult()}
	default:
		return nil
	}
}

func legacyProtoGasData(gas *rpcv2.GasPayment) map[string]any {
	if gas == nil {
		return map[string]any{}
	}
	payment := make([]map[string]any, 0, len(gas.GetObjects()))
	for _, object := range gas.GetObjects() {
		payment = append(payment, map[string]any{
			"objectId": object.GetObjectId(),
			"version":  object.GetVersion(),
			"digest":   object.GetDigest(),
		})
	}
	return map[string]any{
		"payment": payment,
		"owner":   gas.GetOwner(),
		"price":   strconv.FormatUint(gas.GetPrice(), 10),
		"budget":  strconv.FormatUint(gas.GetBudget(), 10),
	}
}

func legacyEffects(effects *rpcv2.TransactionEffects, transaction *rpcv2.Transaction) map[string]any {
	status := map[string]any{"status": "success"}
	if effects.GetStatus() == nil || !effects.GetStatus().GetSuccess() {
		status["status"] = "failure"
		if effects.GetStatus().GetError() != nil {
			status["error"] = effects.GetStatus().GetError().GetDescription()
		}
	}

	created := make([]any, 0)
	mutated := make([]any, 0)
	unwrapped := make([]any, 0)
	deleted := make([]any, 0)
	unwrappedThenDeleted := make([]any, 0)
	wrapped := make([]any, 0)
	modified := make([]any, 0)
	shared := make([]any, 0)
	for _, object := range effects.GetChangedObjects() {
		if object.InputVersion != nil {
			modified = append(modified, map[string]any{
				"objectId":       object.GetObjectId(),
				"sequenceNumber": strconv.FormatUint(object.GetInputVersion(), 10),
			})
		}
		if object.GetInputOwner().GetKind() == rpcv2.Owner_SHARED {
			shared = append(shared, legacyObjectReference(
				object.GetObjectId(), object.GetInputVersion(), object.GetInputDigest(),
			))
		}
		switch object.GetIdOperation() {
		case rpcv2.ChangedObject_CREATED:
			created = append(created, legacyOwnedObject(object))
		case rpcv2.ChangedObject_DELETED:
			deletedRef := legacyObjectReference(
				object.GetObjectId(), object.GetInputVersion(), object.GetInputDigest(),
			)
			if object.GetInputState() == rpcv2.ChangedObject_INPUT_OBJECT_STATE_DOES_NOT_EXIST {
				unwrappedThenDeleted = append(unwrappedThenDeleted, deletedRef)
			} else {
				deleted = append(deleted, deletedRef)
			}
		default:
			switch object.GetOutputState() {
			case rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_OBJECT_WRITE:
				if object.GetInputState() == rpcv2.ChangedObject_INPUT_OBJECT_STATE_DOES_NOT_EXIST {
					unwrapped = append(unwrapped, legacyOwnedObject(object))
				} else {
					mutated = append(mutated, legacyOwnedObject(object))
				}
			case rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_DOES_NOT_EXIST:
				wrapped = append(wrapped, legacyObjectReference(
					object.GetObjectId(), object.GetInputVersion(), object.GetInputDigest(),
				))
			}
		}
	}
	for _, object := range effects.GetUnchangedConsensusObjects() {
		if object.Version != nil && object.Digest != nil {
			shared = append(shared, legacyObjectReference(
				object.GetObjectId(), object.GetVersion(), object.GetDigest(),
			))
		}
	}

	result := map[string]any{
		"messageVersion":     "v1",
		"status":             status,
		"executedEpoch":      strconv.FormatUint(effects.GetEpoch(), 10),
		"gasUsed":            legacyGasSummary(effects.GetGasUsed()),
		"modifiedAtVersions": modified,
		"sharedObjects":      shared,
		"transactionDigest":  effects.GetTransactionDigest(),
		"dependencies":       effects.GetDependencies(),
	}
	if len(created) > 0 {
		result["created"] = created
	}
	if len(mutated) > 0 {
		result["mutated"] = mutated
	}
	if len(unwrapped) > 0 {
		result["unwrapped"] = unwrapped
	}
	if len(deleted) > 0 {
		result["deleted"] = deleted
	}
	if len(unwrappedThenDeleted) > 0 {
		result["unwrapped_then_deleted"] = unwrappedThenDeleted
	}
	if len(wrapped) > 0 {
		result["wrapped"] = wrapped
	}
	if effects.GetGasObject() != nil {
		result["gasObject"] = legacyOwnedObject(effects.GetGasObject())
	} else if transaction.GetGasPayment() != nil && len(transaction.GetGasPayment().GetObjects()) > 0 {
		payment := transaction.GetGasPayment().GetObjects()[0]
		result["gasObject"] = map[string]any{
			"owner": map[string]string{"AddressOwner": transaction.GetGasPayment().GetOwner()},
			"reference": map[string]any{
				"objectId": payment.GetObjectId(),
				"version":  payment.GetVersion(),
				"digest":   payment.GetDigest(),
			},
		}
	}
	if effects.EventsDigest != nil {
		result["eventsDigest"] = effects.GetEventsDigest()
	}
	return result
}

func legacyOwnedObject(object *rpcv2.ChangedObject) map[string]any {
	return map[string]any{
		"owner": legacyOwner(object.GetOutputOwner()),
		"reference": legacyObjectReference(
			object.GetObjectId(), object.GetOutputVersion(), object.GetOutputDigest(),
		),
	}
}

func legacyObjectReference(objectID string, version uint64, digest string) map[string]any {
	return map[string]any{"objectId": objectID, "version": version, "digest": digest}
}

func legacyOwner(owner *rpcv2.Owner) any {
	if owner == nil {
		return nil
	}
	switch owner.GetKind() {
	case rpcv2.Owner_ADDRESS:
		return map[string]string{"AddressOwner": owner.GetAddress()}
	case rpcv2.Owner_OBJECT:
		return map[string]string{"ObjectOwner": owner.GetAddress()}
	case rpcv2.Owner_SHARED:
		return map[string]any{"Shared": map[string]any{
			"initial_shared_version": owner.GetVersion(),
		}}
	case rpcv2.Owner_IMMUTABLE:
		return "Immutable"
	case rpcv2.Owner_CONSENSUS_ADDRESS:
		return map[string]any{"ConsensusAddressOwner": map[string]any{
			"owner": owner.GetAddress(), "start_version": owner.GetVersion(),
		}}
	default:
		return nil
	}
}

func legacyEvents(transaction *rpcv2.ExecutedTransaction) []any {
	events := transaction.GetEvents().GetEvents()
	result := make([]any, 0, len(events))
	for index, event := range events {
		var parsedJSON any
		if event.GetJson() != nil {
			parsedJSON = event.GetJson().AsInterface()
		}
		result = append(result, map[string]any{
			"id": map[string]string{
				"txDigest": transaction.GetDigest(),
				"eventSeq": strconv.Itoa(index),
			},
			"packageId":         event.GetPackageId(),
			"transactionModule": event.GetModule(),
			"sender":            event.GetSender(),
			"type":              canonicalMoveType(event.GetEventType()),
			"parsedJson":        parsedJSON,
			"bcsEncoding":       "base64",
			"bcs":               base64.StdEncoding.EncodeToString(event.GetContents().GetValue()),
		})
	}
	return result
}

func legacyBalanceChanges(transaction *rpcv2.ExecutedTransaction) ([]any, error) {
	changes := transaction.GetBalanceChanges()
	result := make([]any, 0, len(changes))
	for _, change := range changes {
		owner, err := legacyBalanceChangeOwner(change)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]any{
			"owner":    owner,
			"coinType": canonicalMoveType(change.GetCoinType()),
			"amount":   change.GetAmount(),
		})
	}
	return result, nil
}

// gRPC BalanceChange is an address-level delta, including accumulator
// balances. Its address is authoritative; changed Coin objects may not exist.
// Object custody and consensus custody must not change this account-level view.
func legacyBalanceChangeOwner(change *rpcv2.BalanceChange) (any, error) {
	if change.GetAddress() == "" {
		return nil, legacyIncompatibleError("balanceChanges.owner cannot be mapped because gRPC balance change address is missing")
	}
	return map[string]string{"AddressOwner": change.GetAddress()}, nil
}

func legacyObjectChanges(transaction *rpcv2.ExecutedTransaction, publishedModules map[string][]string) []any {
	if transaction.GetEffects() == nil {
		return []any{}
	}
	sender := transaction.GetTransaction().GetSender()
	mutated := make([]any, 0)
	created := make([]any, 0)
	transferred := make([]any, 0)
	deleted := make([]any, 0)
	wrapped := make([]any, 0)
	published := make([]any, 0)
	for _, object := range transaction.GetEffects().GetChangedObjects() {
		if object.GetOutputState() == rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_PACKAGE_WRITE {
			change := map[string]any{
				"type":      "published",
				"packageId": object.GetObjectId(),
				"version":   strconv.FormatUint(object.GetOutputVersion(), 10),
				"digest":    object.GetOutputDigest(),
				"modules":   publishedModules[object.GetObjectId()],
			}
			published = append(published, change)
			continue
		}

		if object.GetOutputState() == rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_DOES_NOT_EXIST {
			change := map[string]any{
				"type":       "wrapped",
				"sender":     sender,
				"objectType": canonicalMoveType(object.GetObjectType()),
				"objectId":   object.GetObjectId(),
				"version":    strconv.FormatUint(object.GetInputVersion(), 10),
			}
			if object.GetIdOperation() == rpcv2.ChangedObject_DELETED {
				change["type"] = "deleted"
				deleted = append(deleted, change)
			} else {
				wrapped = append(wrapped, change)
			}
			continue
		}

		change := map[string]any{
			"sender": sender, "objectId": object.GetObjectId(),
			"objectType": canonicalMoveType(object.GetObjectType()),
		}
		switch object.GetIdOperation() {
		case rpcv2.ChangedObject_CREATED:
			change["type"] = "created"
		default:
			change["type"] = "mutated"
		}
		if object.OutputVersion != nil {
			change["version"] = strconv.FormatUint(object.GetOutputVersion(), 10)
		}
		if object.InputVersion != nil {
			change["previousVersion"] = strconv.FormatUint(object.GetInputVersion(), 10)
		}
		if object.OutputDigest != nil {
			change["digest"] = object.GetOutputDigest()
		}
		if object.OutputOwner != nil {
			change["owner"] = legacyOwner(object.GetOutputOwner())
		}
		if object.GetIdOperation() == rpcv2.ChangedObject_CREATED {
			created = append(created, change)
		} else if object.GetOutputState() == rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_OBJECT_WRITE &&
			!sameOwner(object.GetInputOwner(), object.GetOutputOwner()) {
			change["type"] = "transferred"
			change["recipient"] = legacyOwner(object.GetOutputOwner())
			delete(change, "owner")
			transferred = append(transferred, change)
		} else {
			mutated = append(mutated, change)
		}
	}
	result := append(mutated, created...)
	result = append(result, transferred...)
	result = append(result, deleted...)
	result = append(result, wrapped...)
	return append(result, published...)
}

func sameOwner(left, right *rpcv2.Owner) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.GetKind() == right.GetKind() &&
		left.GetAddress() == right.GetAddress() &&
		left.GetVersion() == right.GetVersion()
}

func enumName(value string) string {
	parts := strings.Split(strings.ToLower(value), "_")
	for index := range parts {
		if parts[index] != "" {
			parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
		}
	}
	return strings.Join(parts, "")
}
