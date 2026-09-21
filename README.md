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

Currently supported JSON-RPC methods:

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

Unsupported methods return JSON-RPC error `-32601`. Implemented methods return
JSON-RPC error `-32001` when a requested legacy field cannot be represented
strictly from Sui gRPC v2 data.

The adapter accepts legacy positional parameters and uses legacy JSON-RPC
field names, with the compatibility exceptions documented below. Batch requests
return JSON-RPC response arrays, and successful methods with no value return `"result": null`. Pagination cursors
returned by `suix_getCoins` and `suix_getAllCoins` use the legacy coin object id
shape; cursors returned by an earlier adapter build are still accepted as a
transition path.

`suix_getBalance` and `suix_getAllBalances` return `coinType` and `totalBalance`.
They omit `coinObjectCount` and `lockedBalance`, which are not supplied by the
gRPC balance response. Clients requiring those legacy fields need adaptation.
`suix_getAllBalances` follows every gRPC page and fails the entire request if
any page fails; it never returns a partial list as a complete balance list.

Sui gRPC does not expose every legacy field with the same response structure.
The adapter converts supported parsed transaction input and effects, and wraps the
gRPC transaction BCS with its intent and signatures to restore the legacy
`rawTransaction` value. Clients that require exact binary transaction data
should prefer `rawTransaction` and `rawEffects` over the parsed fields.
Dry-run parsed pure inputs are resolved from command semantics (`SplitCoins`,
`TransferObjects`, and explicitly typed `MakeMoveVector`) or gRPC `GetFunction`
parameter signatures, including generic type arguments. Supported layouts are
addresses, booleans, unsigned integers, vectors, and the standard Move string,
option, and object ID types. Large integers use decimal strings in JSON.
Function signatures are cached within each request. Missing or unsupported types,
conflicting uses of an input, and invalid BCS return `-32001`; types are never
inferred from byte length. MoveCall resolution requires the upstream
`MovePackageService.GetFunction` service.

This resolver currently applies to dry-run calls. `showInput` reads containing
pure inputs still return `-32001`; raw input remains available with
`showRawInput: true` and `showInput: false`. Execution requests with
`showInput: true` are rejected **before submission**, since the adapter cannot
promise a correctly typed parsed response. Submit with `showInput: false`.
Unknown input kinds also return `-32001` rather than an empty object.

When `showBalanceChanges` is requested, `balanceChanges.owner` is returned only
when it can be mapped uniquely from transaction effects; otherwise the adapter
returns `-32001` instead of guessing an owner.

When `requestType` is omitted or null, effects, events, balance changes, object
changes, or raw effects default to `WaitForLocalExecution`; other options default
to `WaitForEffectsCert`. An explicit `requestType` overrides that default.
For `WaitForLocalExecution`, the adapter executes the transaction and then confirms that its digest is available
through the same gRPC ledger service. It returns `confirmedLocalExecution: true`
only after that lookup succeeds, or `false` when confirmation cannot be obtained
within the bounded wait. A false confirmation does not mean transaction failure;
clients must not blindly resubmit the transaction.

`SimulateTransaction` can evaluate a transaction against current object state
in cases where the retired JSON-RPC dry-run rejected stale input references.
That upstream semantic difference is preserved rather than converted into a
synthetic JSON-RPC error.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `SUI_GRPC_TARGET` | required | Sui gRPC authority, such as `127.0.0.1:9000` |
| `SUI_GRPC_TLS` | `false` | Enable TLS for the gRPC connection |
| `SUI_GRPC_SERVER_NAME` | empty | Optional TLS server name override |
| `LISTEN_ADDRESS` | `:8080` | Adapter HTTP listen address |
| `REQUEST_TIMEOUT` | `10s` | Per-request upstream timeout |
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
