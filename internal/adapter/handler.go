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

	requests, batch, rpcErr := decodeRequests(body)
	if rpcErr != nil {
		h.writeResponse(w, errorResponse(nil, rpcErr.Code, rpcErr.Message, nil))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	if batch {
		responses := make([]response, 0, len(requests))
		for _, req := range requests {
			responses = append(responses, h.handleRequest(ctx, req))
		}
		h.writeResponses(w, responses)
		return
	}

	h.writeResponse(w, h.handleRequest(ctx, requests[0]))
}

func (h *Handler) handleRequest(ctx context.Context, req request) response {
	if req.invalid != nil {
		return errorResponse(req.ID, req.invalid.Code, req.invalid.Message, nil)
	}
	result, err := h.backend.Call(ctx, req.Method, req.Params)
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			return errorResponse(req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
		}
		code := status.Code(err)
		h.logger.Error("Sui gRPC request failed", "method", req.Method, "grpc_code", code.String(), "error", err)
		return errorResponse(req.ID, upstreamError, "Upstream gRPC request failed", map[string]string{
			"grpc_code": publicGRPCCode(code),
		})
	}

	return response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

func (h *Handler) writeResponse(w http.ResponseWriter, value response) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		h.logger.Error("Failed to encode JSON-RPC response", "error", err)
	}
}

func (h *Handler) writeResponses(w http.ResponseWriter, value []response) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		h.logger.Error("Failed to encode JSON-RPC batch response", "error", err)
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
