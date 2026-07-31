package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	parseError     = -32700
	invalidRequest = -32600
	methodNotFound = -32601
	invalidParams  = -32602
	internalError  = -32603
	upstreamError  = -32000
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *responseError  `json:"error,omitempty"`
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func decodeRequest(data []byte) (request, *responseError) {
	var req request
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&req); err != nil {
		return request{}, &responseError{Code: parseError, Message: "Parse error"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return request{}, &responseError{Code: parseError, Message: "Parse error"}
	}
	if req.JSONRPC != "2.0" || req.Method == "" || len(req.ID) == 0 {
		return req, &responseError{Code: invalidRequest, Message: "Invalid Request"}
	}
	return req, nil
}

func validEmptyParams(params json.RawMessage) bool {
	if len(params) == 0 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		return true
	}

	var positional []json.RawMessage
	return json.Unmarshal(params, &positional) == nil && len(positional) == 0
}

func errorResponse(id json.RawMessage, code int, message string, data any) response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &responseError{Code: code, Message: message, Data: data},
	}
}
