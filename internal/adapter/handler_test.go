package adapter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeClient struct {
	result any
	err    error
	calls  []string
}

func (f *fakeClient) Call(_ context.Context, method string, _ json.RawMessage) (any, error) {
	f.calls = append(f.calls, method)
	return f.result, f.err
}

func TestHandlerLatestCheckpoint(t *testing.T) {
	handler := newTestHandler(&fakeClient{result: "123456789"})
	request := httptest.NewRequest(http.MethodPost, "/sui",
		strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"sui_getLatestCheckpointSequenceNumber","params":[]}`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var result struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  string          `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.JSONRPC != "2.0" || string(result.ID) != "7" || result.Result != "123456789" {
		t.Fatalf("unexpected response: %+v", result)
	}
}

func TestHandlerMethodNotFound(t *testing.T) {
	handler := newTestHandler(&fakeClient{err: &RPCError{Code: methodNotFound, Message: "Method not found"}})
	request := httptest.NewRequest(http.MethodPost, "/sui",
		strings.NewReader(`{"jsonrpc":"2.0","id":"request-1","method":"sui_unknown","params":[]}`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	assertRPCError(t, recorder, methodNotFound, `"request-1"`)
}

func TestHandlerRejectsParameters(t *testing.T) {
	handler := newTestHandler(&fakeClient{err: invalidParamsError()})
	request := httptest.NewRequest(http.MethodPost, "/sui",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"sui_getLatestCheckpointSequenceNumber","params":["unexpected"]}`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	assertRPCError(t, recorder, invalidParams, "1")
}

func TestHandlerHidesUpstreamErrorDetails(t *testing.T) {
	handler := newTestHandler(&fakeClient{
		err: status.Error(codes.Unavailable, "private upstream address failed"),
	})
	request := httptest.NewRequest(http.MethodPost, "/sui",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"sui_getLatestCheckpointSequenceNumber","params":[]}`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	assertRPCError(t, recorder, upstreamError, "1")
	if strings.Contains(recorder.Body.String(), "private upstream") {
		t.Fatalf("response leaked upstream details: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"grpc_code":"Unavailable"`) {
		t.Fatalf("response does not contain public gRPC code: %s", recorder.Body.String())
	}
}

func TestDecodeRequestRejectsTrailingJSON(t *testing.T) {
	_, rpcErr := decodeRequest([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"sui_getLatestCheckpointSequenceNumber"} {}`,
	))
	if rpcErr == nil || rpcErr.Code != parseError {
		t.Fatalf("error = %+v, want parse error", rpcErr)
	}
}

func TestHandlerBatchRequests(t *testing.T) {
	client := &fakeClient{result: "ok"}
	handler := newTestHandler(client)
	request := httptest.NewRequest(http.MethodPost, "/sui",
		strings.NewReader(`[`+
			`{"jsonrpc":"2.0","id":1,"method":"sui_getLatestCheckpointSequenceNumber","params":[]},`+
			`{"jsonrpc":"2.0","id":2,"method":"suix_getReferenceGasPrice","params":[]}`+
			`]`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	var result []response
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || string(result[0].ID) != "1" || string(result[1].ID) != "2" {
		t.Fatalf("unexpected batch response: %s", recorder.Body.String())
	}
	if !reflect.DeepEqual(client.calls, []string{"sui_getLatestCheckpointSequenceNumber", "suix_getReferenceGasPrice"}) {
		t.Fatalf("calls = %v", client.calls)
	}
}

func TestHandlerPreservesNullResult(t *testing.T) {
	handler := newTestHandler(&fakeClient{result: nil})
	request := httptest.NewRequest(http.MethodPost, "/sui",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"suix_getCoinMetadata","params":["0x2::x::X"]}`))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if !strings.Contains(recorder.Body.String(), `"result":null`) {
		t.Fatalf("response should contain null result: %s", recorder.Body.String())
	}
}

func newTestHandler(client Backend) *Handler {
	return NewHandler(
		client,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Second,
		1024,
	)
}

func assertRPCError(t *testing.T, recorder *httptest.ResponseRecorder, code int, id string) {
	t.Helper()
	var result response
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || result.Error.Code != code {
		t.Fatalf("response = %s, want error code %d", recorder.Body.String(), code)
	}
	if string(result.ID) != id {
		t.Fatalf("id = %s, want %s", result.ID, id)
	}
}
