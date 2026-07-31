package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Backend interface {
	Call(context.Context, string, json.RawMessage) (any, error)
}

type Handler struct {
	backend Backend
	logger  *slog.Logger
	timeout time.Duration
	maxBody int64
}

func NewHandler(backend Backend, logger *slog.Logger, timeout time.Duration, maxBody int64) *Handler {
	return &Handler{backend: backend, logger: logger, timeout: timeout, maxBody: maxBody}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		h.writeResponse(w, errorResponse(nil, invalidRequest, "Invalid Request", nil))
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		h.writeResponse(w, errorResponse(nil, invalidRequest, "Request body too large", nil))
		return
	}

	req, rpcErr := decodeRequest(body)
	if rpcErr != nil {
		h.writeResponse(w, errorResponse(req.ID, rpcErr.Code, rpcErr.Message, nil))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	result, err := h.backend.Call(ctx, req.Method, req.Params)
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			h.writeResponse(w, errorResponse(req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data))
			return
		}
		code := status.Code(err)
		h.logger.Error("Sui gRPC request failed", "method", req.Method, "grpc_code", code.String(), "error", err)
		h.writeResponse(w, errorResponse(req.ID, upstreamError, "Upstream gRPC request failed", map[string]string{
			"grpc_code": publicGRPCCode(code),
		}))
		return
	}

	h.writeResponse(w, response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
}

func (h *Handler) writeResponse(w http.ResponseWriter, value response) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		h.logger.Error("Failed to encode JSON-RPC response", "error", err)
	}
}

func publicGRPCCode(code codes.Code) string {
	if code == codes.OK {
		return codes.Unknown.String()
	}
	return code.String()
}

type RPCError struct {
	Code    int
	Message string
	Data    any
}

func (e *RPCError) Error() string {
	return e.Message
}
