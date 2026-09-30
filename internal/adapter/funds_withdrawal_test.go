package adapter

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	rpcv2 "sui-adapter/internal/suiv2"
)

func TestFundsWithdrawalInput(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		amount                 uint64
		source                 rpcv2.FundsWithdrawal_Source
		wantAmount, wantSource string
	}{
		{"sender zero", 0, rpcv2.FundsWithdrawal_SENDER, "0", "sender"},
		{"sponsor max", ^uint64(0), rpcv2.FundsWithdrawal_SPONSOR, "18446744073709551615", "sponsor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := legacyInput(&rpcv2.Input{Kind: rpcv2.Input_FUNDS_WITHDRAWAL.Enum(), FundsWithdrawal: &rpcv2.FundsWithdrawal{
				Amount: proto.Uint64(tc.amount), CoinType: proto.String("0x0000000000000000000000000000000000000000000000000000000000000002::sui::SUI"), Source: tc.source.Enum(),
			}})
			want := map[string]any{"type": "fundsWithdrawal", "reservation": map[string]any{"maxAmountU64": tc.wantAmount}, "typeArg": map[string]any{"balance": "0x2::sui::SUI"}, "withdrawFrom": tc.wantSource}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
}

func TestFundsWithdrawalRejectsIncompleteInput(t *testing.T) {
	for _, name := range []string{"payload", "amount", "coin type", "empty coin type", "source", "unknown source", "future source", "future kind"} {
		t.Run(name, func(t *testing.T) {
			value := &rpcv2.FundsWithdrawal{Amount: proto.Uint64(10), CoinType: proto.String("0x2::sui::SUI"), Source: rpcv2.FundsWithdrawal_SENDER.Enum()}
			input := &rpcv2.Input{Kind: rpcv2.Input_FUNDS_WITHDRAWAL.Enum(), FundsWithdrawal: value}
			switch name {
			case "payload":
				input.FundsWithdrawal = nil
			case "amount":
				value.Amount = nil
			case "coin type":
				value.CoinType = nil
			case "empty coin type":
				value.CoinType = proto.String(" ")
			case "source":
				value.Source = nil
			case "unknown source":
				value.Source = rpcv2.FundsWithdrawal_SOURCE_UNKNOWN.Enum()
			case "future source":
				value.Source = rpcv2.FundsWithdrawal_Source(99).Enum()
			case "future kind":
				input.Kind = rpcv2.Input_InputKind(99).Enum()
			}
			if got, err := legacyInput(input); err == nil || got != nil {
				t.Fatalf("accepted incomplete input: %#v, %v", got, err)
			}
		})
	}
}
