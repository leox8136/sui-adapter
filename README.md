# Sui JSON-RPC Adapter

The adapter keeps the existing HTTP JSON-RPC endpoint available while a Sui
node is migrated to gRPC. Each instance connects to exactly one gRPC target.
Node selection remains in OpenResty.

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
- `suix_getAllCoins`
- `suix_getReferenceGasPrice`
- `sui_dryRunTransactionBlock`

Unsupported methods return JSON-RPC error `-32601`.

The adapter accepts the legacy positional parameters and returns the legacy
JSON-RPC field names. Pagination cursors returned by `suix_getAllCoins` are
adapter-generated opaque cursors and must be passed back unchanged. A cursor
created by an old JSON-RPC node cannot be resumed through the adapter.

Sui gRPC does not expose every legacy field with the same response structure.
The adapter reconstructs parsed transaction input and effects, and wraps the
gRPC transaction BCS with its intent and signatures to restore the legacy
`rawTransaction` value. Clients that require exact binary transaction data
should prefer `rawTransaction` and `rawEffects` over the parsed fields.

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

## Deploy on each node

```sh
docker build -t sui-jsonrpc-adapter .
docker run -d --restart unless-stopped \
  --name sui-jsonrpc-adapter \
  --network host \
  -e SUI_GRPC_TARGET=127.0.0.1:9000 \
  -e SUI_GRPC_TLS=false \
  -e LISTEN_ADDRESS=127.0.0.1:18080 \
  sui-jsonrpc-adapter
```

Keep the node's existing port `10001` and route only `/sui` to the adapter:

```nginx
location /sui {
    proxy_pass http://127.0.0.1:18080;
}
```

The existing OpenResty targets remain unchanged:

```text
http://203.117.22.213:10001/sui
http://128.106.101.254:10001/sui
```

## Deploy beside OpenResty for the official node

Use the same image with TLS enabled:

```sh
docker run -d --restart unless-stopped \
  --name sui-official-adapter \
  -e SUI_GRPC_TARGET=fullnode.mainnet.sui.io:443 \
  -e SUI_GRPC_TLS=true \
  -p 127.0.0.1:18081:8080 \
  sui-jsonrpc-adapter
```

Check the compatibility endpoint:

```sh
curl -s http://127.0.0.1:18081/sui \
  -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"sui_getLatestCheckpointSequenceNumber","params":[]}'
```
