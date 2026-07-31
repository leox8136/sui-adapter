package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	parseError         = -32700
	invalidRequest     = -32600
	methodNotFound     = -32601
	invalidParams      = -32602
	internalError      = -32603
	upstreamError      = -32000
	legacyIncompatible = -32001
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	invalid *responseError
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

func decodeRequests(data []byte) ([]request, bool, *responseError) {
	var raw any
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return nil, false, &responseError{Code: parseError, Message: "Parse error"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false, &responseError{Code: parseError, Message: "Parse error"}
	}

	if _, ok := raw.([]any); !ok {
		req, err := validateRequest(data)
		return []request{req}, false, err
	}

	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil || len(items) == 0 {
		return nil, true, &responseError{Code: invalidRequest, Message: "Invalid Request"}
	}
	requests := make([]request, 0, len(items))
	for _, item := range items {
		req, err := validateRequest(item)
		if err != nil {
			req.invalid = err
		}
		requests = append(requests, req)
	}
	return requests, true, nil
}

func validateRequest(data []byte) (request, *responseError) {
	var req request
	if err := json.Unmarshal(data, &req); err != nil {
		return request{}, &responseError{Code: invalidRequest, Message: "Invalid Request"}
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

func legacyIncompatibleError(message string) *RPCError {
	return &RPCError{Code: legacyIncompatible, Message: message}
}

func (r response) MarshalJSON() ([]byte, error) {
	value := map[string]any{
		"jsonrpc": r.JSONRPC,
		"id":      r.ID,
	}
	if len(r.ID) == 0 {
		value["id"] = nil
	}
	if r.Error != nil {
		value["error"] = r.Error
	} else {
		value["result"] = r.Result
	}
	return json.Marshal(value)
}
