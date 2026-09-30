# Sui JSON-RPC Adapter

The adapter keeps the existing HTTP JSON-RPC endpoint available while a Sui
node is migrated to gRPC. Each instance connects to exactly one gRPC target.
Load balancing and node selection are the responsibility of the caller or an
upstream gateway.

The gRPC client is generated directly from MystenLabs'
[`sui-apis`](https://github.com/MystenLabs/sui-apis) protobuf definitions.
There is no Sui Go SDK dependency. The pinned source revision is recorded in
`proto/SOURCE`; run `./generate-proto.sh` after intentionally updating those
files.

## Supported JSON-RPC methods

- `sui_getLatestCheckpointSequenceNumber`
- `sui_getCheckpoint`
- `sui_getTransactionBlock`
- `sui_executeTransactionBlock`
- `suix_getBalance`
- `suix_getCoinMetadata`
- `suix_getCoins`
- `suix_getAllCoins`
- `suix_getAllBalances`
- `suix_getTotalSupply`
- `suix_getReferenceGasPrice`
- `sui_dryRunTransactionBlock`

## JSON-RPC compatibility

The adapter accepts legacy positional parameters and uses legacy JSON-RPC
field names, subject to the exceptions below. Batch requests return response
arrays, and successful methods with no value return `"result": null`.
Unsupported methods return `-32601`. Fields that cannot be represented strictly
from gRPC v2 data normally return `-32001`; input-rendering failures after
execution use `result.errors` instead, as described below.

### Transaction parameters

Transaction construction and signing do not change when using the adapter.
`sui_executeTransactionBlock` accepts two to four positional parameters:

| Position | Parameter | Adapter behavior |
| --- | --- | --- |
| `0` | Transaction bytes | Base64-encoded BCS TransactionData, using the existing JSON-RPC input format. |
| `1` | Signatures | Array of Base64-encoded signatures. A single signature string is also accepted. |
| `2` | Response options | Optional object or `null`; all flags default to `false`. |
| `3` | Request type | Optional `WaitForEffectsCert` or `WaitForLocalExecution`; omission or `null` selects the default described below. |

Example request (replace the placeholder transaction and signature):

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "sui_executeTransactionBlock",
  "params": [
    "<BASE64_TRANSACTION_DATA>",
    ["<BASE64_SIGNATURE>"],
    {
      "showInput": true,
      "showRawInput": true,
      "showEffects": true
    },
    "WaitForLocalExecution"
  ]
}
```

`sui_getTransactionBlock` accepts `[digest, options]`, with options optional.
`sui_dryRunTransactionBlock` accepts only `[transactionBytes]`, requires no
signatures, and returns parsed `input` automatically. It does not accept
`showInput`, `showRawInput`, or a request type.

### Parsed input and raw input

These flags control the response, not the submitted transaction or signature.
Both are supported by transaction reads and execution and can be enabled together.

| Option | Response field | Content |
| --- | --- | --- |
| `showInput` | `transaction` | Parsed transaction data: sender, gas payment, inputs, commands, and signatures. |
| `showRawInput` | `rawTransaction` | Base64-encoded BCS transaction wrapped with intent and signatures to restore the legacy response format. |
| `showEffects` | `effects` | Execution status, gas costs, and transaction effects. |

`rawTransaction` is not the same encoding as the execution request's transaction
bytes; do not pass it directly back as `params[0]`. Clients that need execution
status and gas costs can request only `showEffects`, avoiding parsed input work.
`showRawEffects`, `showEvents`, `showObjectChanges`, and `showBalanceChanges`
also retain their legacy option names.

Funds withdrawal inputs render as `type: "fundsWithdrawal"`, with
`reservation.maxAmountU64` as a decimal string, `typeArg.balance` as the coin
Move type, and `withdrawFrom` as `"sender"` or `"sponsor"`. The reservation is a
maximum withdrawal amount, not the actual balance change; `balanceChanges`
continues to use upstream deltas. Missing amounts/types and missing or unknown
sources fail input conversion instead of defaulting to zero or sender.

Parsed pure inputs are resolved from command semantics (`SplitCoins`,
`TransferObjects`, and explicitly typed `MakeMoveVector`) or gRPC `GetFunction`
parameter signatures, including generic type arguments. Supported layouts are
addresses, booleans, unsigned integers, vectors, and the standard Move string,
option, and object ID types. Large integers use decimal strings in JSON.
Types are never inferred from byte length.

MoveCall resolution requires `MovePackageService.GetFunction`. Function signatures
are cached within each request; these extra lookups can add latency and consume
the request timeout. Raw input does not need type resolution.
A pure input may be reused with different Move types. For its single legacy
`valueType`/`value` representation, the adapter uses the last resolved type in
command and argument order, matching the legacy JSON-RPC renderer. For example,
BCS `0x00` can represent both `false` and `Option<u64>::None`. The selected layout
is still validated when decoding; it is not inferred from byte length.
Only pure inputs confirmed to be unreferenced by all supported commands retain
all their BCS bytes as a JSON integer array with `"type": "pure"` and
`"valueType": null`. An empty byte sequence renders as `"value": []`, not null or
Base64. Known types continue to return their typed values. Referenced inputs
without a resolved type, unknown commands, and invalid input references fail
explicitly instead of falling back to untyped bytes.
Incomplete or unsupported referenced type signatures, invalid BCS for a known
type, and unknown input kinds still produce compatibility errors on reads and
dry-runs. A read of an explicitly failed on-chain transaction is the exception:
if a resolved pure value cannot be decoded, the response omits `transaction`
and explains the rendering failure in `result.errors`, retaining other requested
fields. It never substitutes an invented value or untyped bytes for the invalid
input. Successful transactions, missing/ambiguous execution status, signature
lookup failures, and unknown input kinds still fail the read. This partial read
behavior is an adapter compatibility policy; clients must check `result.errors`
and `effects.status`. `showRawInput` remains independently available.
Upstream lookup failures are reported as upstream errors.

For execution, input parsing happens **after** gRPC returns the executed
transaction. If input resolution fails, the adapter preserves the digest and
other successfully rendered requested fields, omits `transaction`, and puts the
reason in `result.errors`. `rawTransaction` remains available independently when
requested and provided by the upstream. No extra simulation or execution is
performed to render input. This partial-response behavior applies specifically
to input rendering; other response conversion failures can still return a
JSON-RPC error after submission.

### Execution confirmation and timeouts

`requestType` controls the adapter's additional confirmation step. It is not
forwarded as a gRPC execution mode: both values call `ExecuteTransaction`.

| Request type | Adapter behavior |
| --- | --- |
| `WaitForEffectsCert` | Return the execution response without an additional ledger lookup. `confirmedLocalExecution` is omitted. |
| `WaitForLocalExecution` | After execution and response conversion, query the same configured gRPC ledger service for the returned digest. Set `confirmedLocalExecution` to `true` only when that lookup succeeds, otherwise `false`. |

When the request type is omitted or `null`, requesting effects, raw effects,
events, balance changes, or object changes selects `WaitForLocalExecution`.
All other combinations select `WaitForEffectsCert`. An explicit value overrides
this default.

The legacy local-execution mode was intended to confirm execution on the local
node. The adapter instead confirms **query visibility through its configured
gRPC service**. If that service is load-balanced, execution and lookup can reach
different physical nodes. Even `confirmedLocalExecution: true` does not guarantee
that every node or downstream balance/indexing API is immediately up to date.

The additional confirmation step has a fixed maximum wait of **5 seconds**, with
retries at **200 ms** intervals. It can stop earlier on a non-retryable lookup
error or cancellation. This is not an extra 5 seconds beyond `REQUEST_TIMEOUT`:
execution, type lookups, and confirmation share the request deadline (default
**10 seconds**). Batch items also share the HTTP request deadline.

### Interpreting results and retrying

| Field or outcome | Meaning and client action |
| --- | --- |
| `effects.status.status` | Execution success or failure when effects are requested. Use this to determine the transaction outcome. |
| `confirmedLocalExecution: false` | The adapter could not confirm query visibility. This does not mean execution failed; query the original digest with bounded retries. |
| `result.errors` for input rendering | The execution response was received, but parsed input is unavailable. Inspect effects and query the digest if needed; do not classify this as a failed submission. |
| Timeout, lost response, or another JSON-RPC error | Do not assume the transaction was never submitted. Reconcile its digest and effects before deciding whether to retry. |

Do not construct and sign a new transfer solely because confirmation is false
or input parsing failed: the original transfer may already have executed, and a
new transaction can transfer funds a second time. Retry queries for the original
digest rather than immediately creating a replacement transaction.

### Other compatibility boundaries

- `suix_getBalance` and `suix_getAllBalances` return `coinType` and `totalBalance`,
  but omit `coinObjectCount` and `lockedBalance`, which gRPC does not supply.
- `suix_getAllBalances` follows every page and fails the entire request if any
  page fails; a partial list is never returned as a complete list.
- Coin pagination cursors use the legacy coin object ID shape. Cursors from an
  earlier adapter build are also accepted as a transition path.
- gRPC balance changes describe account-level deltas. Their `address` maps
  directly to `balanceChanges.owner.AddressOwner`, including address balances
  that have no changed Coin object. Coin object ownership is not used to infer
  this field; a missing address still returns `-32001`.
- `SimulateTransaction` can evaluate against current object state where legacy
  dry-run rejected stale object references. The adapter preserves that upstream
  difference rather than generating a synthetic JSON-RPC error.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `SUI_GRPC_TARGET` | required | Sui gRPC authority, such as `127.0.0.1:9000` |
| `SUI_GRPC_TLS` | `false` | Enable TLS for the gRPC connection |
| `SUI_GRPC_SERVER_NAME` | empty | Optional TLS server name override |
| `LISTEN_ADDRESS` | `:8080` | Adapter HTTP listen address |
| `REQUEST_TIMEOUT` | `10s` | Shared HTTP request deadline for upstream calls, type resolution, and confirmation; batch items share it |
| `MAX_BODY_BYTES` | `1048576` | Maximum JSON-RPC request size |

## Run beside a Sui node

When the Sui gRPC service is available on the same host, bind the Adapter to a
loopback address and connect it to the node's local gRPC listener:

```sh
docker build -t sui-jsonrpc-adapter .
docker run -d --restart unless-stopped \
  --name sui-jsonrpc-adapter \
  --network host \
  -e SUI_GRPC_TARGET=127.0.0.1:9000 \
  -e SUI_GRPC_TLS=false \
  -e LISTEN_ADDRESS=127.0.0.1:8080 \
  sui-jsonrpc-adapter
```

If a reverse proxy exposes the compatibility endpoint, forward `/sui` to the
Adapter's configured listener. Access control, TLS termination, and the public
listen address belong in the deployment environment rather than this project.

```nginx
location /sui {
    proxy_pass http://127.0.0.1:8080;
}
```

## Connect to a remote gRPC endpoint

For a remote gRPC service, enable TLS and provide its authority. This example
uses Sui's public mainnet gRPC endpoint:

```sh
docker run -d --restart unless-stopped \
  --name sui-jsonrpc-adapter \
  -e SUI_GRPC_TARGET=fullnode.mainnet.sui.io:443 \
  -e SUI_GRPC_TLS=true \
  -p 127.0.0.1:8080:8080 \
  sui-jsonrpc-adapter
```

Check the compatibility endpoint:

```sh
curl -s http://127.0.0.1:8080/sui \
  -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"sui_getLatestCheckpointSequenceNumber","params":[]}'
```

## Development

Requires Go 1.24.7 or newer.

```sh
go test ./...
go vet ./...
```

## License

Apache-2.0; see [LICENSE](LICENSE). Vendored protocol sources and generated
bindings retain their upstream notices; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Compatibility regression fixtures

`internal/adapter/testdata` contains official mainnet gRPC transaction responses
and Move function signatures captured read-only on 2026-09-30 for:

- `DYMKT6gAhBkT4W1tpsUm2dgYC1nHHYqb8NxxYokyfNE7`: an account balance delta
  without a matching changed Coin owner, and a pure input reused as both
  `Option<u64>` and `bool`.
- `CPyHbe9cUvK4x8bSqZHJ4LnqDXaf6371yWVnqXt9122U`: accumulator settlement
  with empty balance changes.
- `84CiBJfK18QQZPF1akkXTwnZFS32jZdLtzuBVim5fgxn`: unused pure input 14,
  preserved as 40 untyped bytes while retaining both balance changes.

- `B2j9QhAxs5qrvuW8HSG7VqeJQFiFN7qPcKJ6QWk1rvxP`: sender funds
  withdrawal input 10, preserving its reservation and both actual balance deltas.

- `5MykKR8prfJvjX9RFa8uVtBVi6Qot52y9koR3XBp4veL`: failed transaction with
  invalid `vector<u64>` bytes. Read tests retain the failure and exact balance
  delta, omit parsed input with an explicit error, and reject ambiguous status.

Tests replay the successful fixtures through the HTTP handler for reads, dry-run, and
execution response conversion. They make no network calls and do not broadcast
transactions. Mapping references are the upstream
[account balance derivation](https://github.com/MystenLabs/sui/blob/main/crates/sui-types/src/balance_change.rs)
and legacy
[pure input renderer](https://github.com/MystenLabs/sui/blob/main/crates/sui-json-rpc-types/src/sui_transaction.rs).
