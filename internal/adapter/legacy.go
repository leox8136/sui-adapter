package adapter

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"strconv"
	"strings"

	rpcv2 "sui-adapter/internal/suiv2"
)

const deletedObjectDigest = "7gyGAp71YXQRoxmFBaHxofQXAipvgHyBKPyxmdSJxyvz"

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

func legacyTransaction(transaction *rpcv2.ExecutedTransaction, options transactionOptions) map[string]any {
	result := map[string]any{"digest": transaction.GetDigest()}
	if transaction.Checkpoint != nil {
		result["checkpoint"] = strconv.FormatUint(transaction.GetCheckpoint(), 10)
	}
	if transaction.Timestamp != nil {
		result["timestampMs"] = timestampMillis(transaction.GetTimestamp())
	}
	signatures := legacySignatures(transaction.GetSignatures())
	if options.ShowInput && transaction.GetTransaction() != nil {
		result["transaction"] = legacyTransactionInput(transaction.GetTransaction(), signatures)
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
		result["objectChanges"] = legacyObjectChanges(transaction)
	}
	if options.ShowBalanceChanges {
		result["balanceChanges"] = legacyBalanceChanges(transaction.GetBalanceChanges())
	}
	return result
}

func legacyDryRunTransaction(transaction *rpcv2.ExecutedTransaction) map[string]any {
	options := transactionOptions{
		ShowInput: true, ShowEffects: true, ShowEvents: true,
		ShowObjectChanges: true, ShowBalanceChanges: true,
	}
	result := legacyTransaction(transaction, options)
	delete(result, "digest")
	delete(result, "checkpoint")
	delete(result, "timestampMs")
	result["input"] = result["transaction"]
	delete(result, "transaction")
	if status := transaction.GetEffects().GetStatus(); status != nil && !status.GetSuccess() {
		result["executionError"] = status.GetError().GetDescription()
	}
	return result
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

func legacyTransactionInput(transaction *rpcv2.Transaction, signatures []string) map[string]any {
	kind := legacyTransactionKind(transaction.GetKind())
	return map[string]any{
		"data": map[string]any{
			"messageVersion": "v1",
			"transaction":    kind,
			"sender":         transaction.GetSender(),
			"gasData":        legacyProtoGasData(transaction.GetGasPayment()),
		},
		"txSignatures": signatures,
	}
}

func legacyTransactionKind(kind *rpcv2.TransactionKind) map[string]any {
	if kind == nil {
		return map[string]any{}
	}
	if programmable := kind.GetProgrammableTransaction(); programmable != nil {
		inputs := make([]any, 0, len(programmable.GetInputs()))
		for _, input := range programmable.GetInputs() {
			inputs = append(inputs, legacyInput(input))
		}
		commands := make([]any, 0, len(programmable.GetCommands()))
		for _, command := range programmable.GetCommands() {
			commands = append(commands, legacyCommand(command))
		}
		return map[string]any{
			"kind": "ProgrammableTransaction", "inputs": inputs, "transactions": commands,
		}
	}
	if prologue := kind.GetConsensusCommitPrologue(); prologue != nil {
		return legacyConsensusCommitPrologue(kind.GetKind(), prologue)
	}
	return map[string]any{"kind": enumName(kind.GetKind().String())}
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

func legacyInput(input *rpcv2.Input) any {
	switch input.GetKind() {
	case rpcv2.Input_PURE:
		return legacyPureInput(input)
	case rpcv2.Input_IMMUTABLE_OR_OWNED:
		return map[string]any{
			"type": "object", "objectType": "immOrOwnedObject",
			"objectId": input.GetObjectId(),
			"version":  strconv.FormatUint(input.GetVersion(), 10),
			"digest":   input.GetDigest(),
		}
	case rpcv2.Input_SHARED:
		return map[string]any{
			"type": "object", "objectType": "sharedObject",
			"objectId":             input.GetObjectId(),
			"initialSharedVersion": strconv.FormatUint(input.GetVersion(), 10),
			"mutable":              input.GetMutable(),
		}
	case rpcv2.Input_RECEIVING:
		return map[string]any{
			"type": "object", "objectType": "receiving",
			"objectId": input.GetObjectId(),
			"version":  strconv.FormatUint(input.GetVersion(), 10),
			"digest":   input.GetDigest(),
		}
	default:
		return map[string]any{}
	}
}

func legacyPureInput(input *rpcv2.Input) map[string]any {
	result := map[string]any{"type": "pure"}
	if literal := input.GetLiteral(); literal != nil {
		value := literal.AsInterface()
		result["value"] = value
		switch value.(type) {
		case bool:
			result["valueType"] = "bool"
		case string:
			if strings.HasPrefix(value.(string), "0x") {
				result["valueType"] = "address"
			} else {
				result["valueType"] = "string"
			}
		case float64:
			result["valueType"] = uintTypeForSize(len(input.GetPure()))
			if len(input.GetPure()) >= 8 {
				result["value"] = strconv.FormatUint(uintFromLittleEndian(input.GetPure()), 10)
			}
		default:
			result["valueType"] = "vector"
		}
		return result
	}

	bytes := input.GetPure()
	switch len(bytes) {
	case 1:
		if bytes[0] <= 1 {
			result["valueType"] = "bool"
			result["value"] = bytes[0] == 1
		} else {
			result["valueType"] = "u8"
			result["value"] = uint64(bytes[0])
		}
	case 2, 4:
		result["valueType"] = uintTypeForSize(len(bytes))
		result["value"] = uintFromLittleEndian(bytes)
	case 8:
		result["valueType"] = "u64"
		result["value"] = strconv.FormatUint(binary.LittleEndian.Uint64(bytes), 10)
	case 16:
		result["valueType"] = "u128"
		result["value"] = littleEndianBigInt(bytes).String()
	case 32:
		result["valueType"] = "address"
		result["value"] = "0x" + hex.EncodeToString(bytes)
	default:
		result["valueType"] = "unknown"
		result["value"] = base64.StdEncoding.EncodeToString(bytes)
	}
	return result
}

func uintTypeForSize(size int) string {
	switch size {
	case 1:
		return "u8"
	case 2:
		return "u16"
	case 4:
		return "u32"
	default:
		return "u64"
	}
}

func uintFromLittleEndian(value []byte) uint64 {
	var padded [8]byte
	copy(padded[:], value)
	return binary.LittleEndian.Uint64(padded[:])
}

func littleEndianBigInt(value []byte) *big.Int {
	return new(big.Int).SetBytes(reverseCopy(value))
}

func reverseCopy(value []byte) []byte {
	result := append([]byte(nil), value...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
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
	deleted := make([]any, 0)
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
			deleted = append(deleted, map[string]any{
				"objectId": object.GetObjectId(),
				"version":  effects.GetLamportVersion(),
				"digest":   deletedObjectDigest,
			})
		default:
			if object.GetOutputState() == rpcv2.ChangedObject_OUTPUT_OBJECT_STATE_OBJECT_WRITE {
				mutated = append(mutated, legacyOwnedObject(object))
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
	if len(deleted) > 0 {
		result["deleted"] = deleted
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

func legacyBalanceChanges(changes []*rpcv2.BalanceChange) []any {
	result := make([]any, 0, len(changes))
	for _, change := range changes {
		result = append(result, map[string]any{
			"owner":    map[string]string{"AddressOwner": change.GetAddress()},
			"coinType": canonicalMoveType(change.GetCoinType()),
			"amount":   change.GetAmount(),
		})
	}
	return result
}

func legacyObjectChanges(transaction *rpcv2.ExecutedTransaction) []any {
	if transaction.GetEffects() == nil {
		return []any{}
	}
	sender := transaction.GetTransaction().GetSender()
	mutated := make([]any, 0)
	created := make([]any, 0)
	for _, object := range transaction.GetEffects().GetChangedObjects() {
		if object.GetObjectType() == "" || object.GetIdOperation() == rpcv2.ChangedObject_DELETED {
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
		} else {
			mutated = append(mutated, change)
		}
	}
	return append(mutated, created...)
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
