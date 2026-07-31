#!/bin/sh
set -eu

PROTO_ROOT=proto
PACKAGE=sui-adapter/internal/suiv2

set -- "$PROTO_ROOT"/sui/rpc/v2/*.proto
mapping_args=
for file in "$@"; do
    relative=${file#"$PROTO_ROOT"/}
    mapping_args="$mapping_args --go_opt=M$relative=$PACKAGE;suiv2"
    mapping_args="$mapping_args --go-grpc_opt=M$relative=$PACKAGE;suiv2"
done

# shellcheck disable=SC2086
protoc \
    -I "$PROTO_ROOT" \
    --go_out=. \
    --go_opt=module=sui-adapter \
    --go-grpc_out=. \
    --go-grpc_opt=module=sui-adapter \
    $mapping_args \
    "$@"
