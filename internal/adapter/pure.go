package adapter

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	rpcv2 "sui-adapter/internal/suiv2"
)

// Values are request-local and keyed by the original protobuf input. Never put
// inferred types into Input.literal, which is an input-only gRPC field.
// Distinguishes value decoding from signature lookup and unsupported input errors.
type pureInputDecodeError struct{ error }

func (e *pureInputDecodeError) Unwrap() error { return e.error }

type resolvedPureInputs map[*rpcv2.Input]map[string]any

func (b *SuiBackend) resolvePureInputs(ctx context.Context, tx *rpcv2.Transaction) (resolvedPureInputs, error) {
	pt := tx.GetKind().GetProgrammableTransaction()
	referenced, err := referencedTransactionInputs(pt)
	if err != nil {
		return nil, err
	}
	types := make(map[uint32]string)
	isPure := func(arg *rpcv2.Argument) bool {
		return arg.GetKind() == rpcv2.Argument_INPUT && arg.Input != nil && int(arg.GetInput()) < len(pt.GetInputs()) && pt.GetInputs()[arg.GetInput()].GetKind() == rpcv2.Input_PURE
	}
	assign := func(arg *rpcv2.Argument, typ string) error {
		if !isPure(arg) {
			return nil
		}
		typ = normalizeMoveType(strings.ReplaceAll(typ, " ", ""))
		index := arg.GetInput()
		// A pure byte string can be reused with different Move layouts. Like
		// legacy JSON-RPC's resolve_input_type, render using the last resolved
		// use in command/argument order; this is not transaction validation.
		types[index] = typ
		return nil
	}
	functions := make(map[string]*rpcv2.FunctionDescriptor)
	for _, command := range pt.GetCommands() {
		if transfer := command.GetTransferObjects(); transfer != nil {
			if err := assign(transfer.GetAddress(), "address"); err != nil {
				return nil, err
			}
		}
		if split := command.GetSplitCoins(); split != nil {
			for _, arg := range split.GetAmounts() {
				if err := assign(arg, "u64"); err != nil {
					return nil, err
				}
			}
		}
		if vector := command.GetMakeMoveVector(); vector != nil {
			for _, arg := range vector.GetElements() {
				if isPure(arg) && vector.GetElementType() == "" {
					return nil, legacyIncompatibleError("pure MakeMoveVector element type is missing")
				}
				if err := assign(arg, vector.GetElementType()); err != nil {
					return nil, err
				}
			}
		}
		call := command.GetMoveCall()
		if call == nil {
			continue
		}
		needed := false
		for _, arg := range call.GetArguments() {
			needed = needed || isPure(arg)
		}
		if !needed {
			continue
		}
		key := call.GetPackage() + "::" + call.GetModule() + "::" + call.GetFunction()
		function := functions[key]
		if function == nil {
			if b.packages == nil {
				return nil, legacyIncompatibleError("Move function service unavailable for pure input resolution")
			}
			response, err := b.packages.GetFunction(ctx, &rpcv2.GetFunctionRequest{PackageId: call.Package, ModuleName: call.Module, Name: call.Function})
			if err != nil {
				return nil, err
			}
			function = response.GetFunction()
			if function == nil {
				return nil, legacyIncompatibleError("Move function signature missing: " + key)
			}
			functions[key] = function
		}
		for i, arg := range call.GetArguments() {
			if !isPure(arg) {
				continue
			}
			if i >= len(function.GetParameters()) {
				return nil, legacyIncompatibleError("Move function parameter missing: " + key)
			}
			typ, err := pureSignatureType(function.GetParameters()[i].GetBody(), call.GetTypeArguments(), 0)
			if err != nil {
				return nil, err
			}
			if err := assign(arg, typ); err != nil {
				return nil, err
			}
		}
	}
	result := make(resolvedPureInputs)
	for i, input := range pt.GetInputs() {
		if input.GetKind() != rpcv2.Input_PURE {
			continue
		}
		typ, ok := types[uint32(i)]
		if !ok {
			if referenced[uint32(i)] {
				return nil, legacyIncompatibleError(fmt.Sprintf("referenced pure input %d type could not be resolved", i))
			}
			// Unused pure inputs have no command-derived layout. Preserve their
			// bytes using the legacy untyped representation, without guessing.
			result[input] = map[string]any{"type": "pure", "valueType": nil, "value": bytesToNumbers(input.GetPure())}
			continue
		}
		value, err := decodePureValue(typ, input.GetPure())
		if err != nil {
			return nil, &pureInputDecodeError{legacyIncompatibleError(fmt.Sprintf("pure input %d (%s): %v", i, typ, err))}
		}
		result[input] = map[string]any{"type": "pure", "valueType": typ, "value": value}
	}
	return result, nil
}

func pureSignatureType(body *rpcv2.OpenSignatureBody, args []string, depth int) (string, error) {
	if depth > 64 {
		return "", legacyIncompatibleError("Move type nesting limit exceeded")
	}
	switch body.GetType() {
	case rpcv2.OpenSignatureBody_ADDRESS, rpcv2.OpenSignatureBody_BOOL, rpcv2.OpenSignatureBody_U8, rpcv2.OpenSignatureBody_U16, rpcv2.OpenSignatureBody_U32, rpcv2.OpenSignatureBody_U64, rpcv2.OpenSignatureBody_U128, rpcv2.OpenSignatureBody_U256:
		return strings.ToLower(body.GetType().String()), nil
	case rpcv2.OpenSignatureBody_TYPE_PARAMETER:
		if body.TypeParameter != nil && int(body.GetTypeParameter()) < len(args) {
			return args[body.GetTypeParameter()], nil
		}
	case rpcv2.OpenSignatureBody_VECTOR, rpcv2.OpenSignatureBody_DATATYPE:
		name := body.GetTypeName()
		if body.GetType() == rpcv2.OpenSignatureBody_VECTOR {
			name = "vector"
			if len(body.GetTypeParameterInstantiation()) != 1 {
				break
			}
		}
		if name == "" {
			break
		}
		parameters := []string{}
		for _, param := range body.GetTypeParameterInstantiation() {
			typ, err := pureSignatureType(param, args, depth+1)
			if err != nil {
				return "", err
			}
			parameters = append(parameters, typ)
		}
		if len(parameters) > 0 {
			name += "<" + strings.Join(parameters, ",") + ">"
		}
		return name, nil
	}
	return "", legacyIncompatibleError("Move pure input signature is incomplete or unsupported")
}

// Only primitive and standard pure layouts are allowed. Bound both type
// nesting and vector lengths before allocation; require canonical BCS.
func decodePureValue(typ string, data []byte) (any, error) {
	if err := validatePureType(typ, 0); err != nil {
		return nil, err
	}
	decoder := pureDecoder{data: data}
	value, err := decoder.value(typ, 0)
	if err == nil && len(decoder.data) != 0 {
		err = fmt.Errorf("trailing BCS bytes")
	}
	return value, err
}

type pureDecoder struct{ data []byte }

func (d *pureDecoder) take(n int) ([]byte, error) {
	if n > len(d.data) {
		return nil, fmt.Errorf("truncated BCS value")
	}
	v := d.data[:n]
	d.data = d.data[n:]
	return v, nil
}
func (d *pureDecoder) length() (int, error) {
	var n uint32
	for i := 0; i < 5; i++ {
		b, err := d.take(1)
		if err != nil {
			return 0, err
		}
		if i == 4 && b[0] > 15 {
			return 0, fmt.Errorf("BCS length overflow")
		}
		n |= uint32(b[0]&127) << (7 * i)
		if b[0] < 128 {
			if i > 0 && b[0] == 0 {
				return 0, fmt.Errorf("non-canonical BCS length")
			}
			if uint64(n) > uint64(len(d.data)) {
				return 0, fmt.Errorf("BCS vector exceeds remaining bytes")
			}
			return int(n), nil
		}
	}
	return 0, fmt.Errorf("invalid BCS length")
}
func (d *pureDecoder) value(typ string, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("Move type nesting limit exceeded")
	}
	if strings.HasPrefix(typ, "vector<") && strings.HasSuffix(typ, ">") {
		return d.vector(typ[7:len(typ)-1], depth, false)
	}
	if strings.HasPrefix(typ, "0x1::option::Option<") && strings.HasSuffix(typ, ">") {
		return d.vector(typ[len("0x1::option::Option<"):len(typ)-1], depth, true)
	}
	switch typ {
	case "0x1::string::String", "0x1::ascii::String":
		n, err := d.length()
		if err != nil {
			return nil, err
		}
		b, err := d.take(n)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(b) {
			return nil, fmt.Errorf("invalid UTF-8 string")
		}
		if typ == "0x1::ascii::String" {
			for _, c := range b {
				if c > 127 {
					return nil, fmt.Errorf("invalid ASCII string")
				}
			}
		}
		return string(b), nil
	case "address", "0x2::object::ID":
		b, err := d.take(32)
		if err != nil {
			return nil, err
		}
		return "0x" + hex.EncodeToString(b), nil
	case "bool":
		b, err := d.take(1)
		if err != nil {
			return nil, err
		}
		if b[0] > 1 {
			return nil, fmt.Errorf("invalid BCS bool")
		}
		return b[0] == 1, nil
	}
	size := map[string]int{"u8": 1, "u16": 2, "u32": 4, "u64": 8, "u128": 16, "u256": 32}[typ]
	if size == 0 {
		return nil, fmt.Errorf("unsupported pure type %s", typ)
	}
	b, err := d.take(size)
	if err != nil {
		return nil, err
	}
	reversed := make([]byte, size)
	for i := range b {
		reversed[size-1-i] = b[i]
	}
	n := new(big.Int).SetBytes(reversed)
	if size <= 4 {
		return n.Uint64(), nil
	}
	return n.String(), nil
}
func (d *pureDecoder) vector(element string, depth int, option bool) (any, error) {
	n, err := d.length()
	if err != nil {
		return nil, err
	}
	if option && n > 1 {
		return nil, fmt.Errorf("invalid Move option length")
	}
	// Legacy JSON-RPC renders a top-level UTF-8 vector<u8> as a string.
	if !option && depth == 0 && element == "u8" && utf8.Valid(d.data[:n]) {
		b, _ := d.take(n)
		return string(b), nil
	}
	result := make([]any, 0, n)
	for i := 0; i < n; i++ {
		v, err := d.value(element, depth+1)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, nil
}

func validatePureType(typ string, depth int) error {
	if depth > 64 {
		return fmt.Errorf("Move type nesting limit exceeded")
	}
	switch typ {
	case "address", "bool", "u8", "u16", "u32", "u64", "u128", "u256", "0x1::string::String", "0x1::ascii::String", "0x2::object::ID":
		return nil
	}
	for _, prefix := range []string{"vector<", "0x1::option::Option<"} {
		if strings.HasPrefix(typ, prefix) && strings.HasSuffix(typ, ">") {
			return validatePureType(typ[len(prefix):len(typ)-1], depth+1)
		}
	}
	return fmt.Errorf("unsupported pure type %s", typ)
}

// Enumerate every supported command's arguments before allowing untyped bytes.
// Unknown commands cannot prove an input is unused and must fail closed.
func referencedTransactionInputs(pt *rpcv2.ProgrammableTransaction) (map[uint32]bool, error) {
	referenced := make(map[uint32]bool)
	for _, command := range pt.GetCommands() {
		var args []*rpcv2.Argument
		switch c := command.GetCommand().(type) {
		case *rpcv2.Command_MoveCall:
			args = c.MoveCall.GetArguments()
		case *rpcv2.Command_TransferObjects:
			args = append(args, c.TransferObjects.GetObjects()...)
			args = append(args, c.TransferObjects.GetAddress())
		case *rpcv2.Command_SplitCoins:
			args = append(args, c.SplitCoins.GetCoin())
			args = append(args, c.SplitCoins.GetAmounts()...)
		case *rpcv2.Command_MergeCoins:
			args = append(args, c.MergeCoins.GetCoin())
			args = append(args, c.MergeCoins.GetCoinsToMerge()...)
		case *rpcv2.Command_MakeMoveVector:
			args = c.MakeMoveVector.GetElements()
		case *rpcv2.Command_Upgrade:
			args = append(args, c.Upgrade.GetTicket())
		case *rpcv2.Command_Publish:
			// Publish contains bytecode and dependency IDs, not input references.
		default:
			return nil, legacyIncompatibleError("unknown transaction command; cannot resolve input usage")
		}
		for _, arg := range args {
			if arg.GetKind() != rpcv2.Argument_INPUT {
				continue
			}
			if arg.Input == nil || uint64(arg.GetInput()) >= uint64(len(pt.GetInputs())) {
				return nil, legacyIncompatibleError("transaction command input reference is missing or out of bounds")
			}
			referenced[arg.GetInput()] = true
		}
	}
	return referenced, nil
}
