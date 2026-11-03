package provider

import (
	"context"
	"net/http"
)

// InferenceBackend supplies account-specific model lookup and authorized HTTP
// requests. The shared engine owns protocol encoding and response parsing only.
type InferenceBackend interface {
	LookupModel(context.Context, string) (Model, error)
	PrepareInference(context.Context, Request, Model, []byte, bool) (*http.Request, error)
	Do(*http.Request) (*http.Response, error)
}

// RequestPreparer optionally validates and normalizes a request before the
// engine looks up its model or encodes attachments.
type RequestPreparer interface {
	PrepareRequest(Request) (Request, error)
}

// Engine shares request encoding and streaming response handling across providers.
type Engine struct{ backend InferenceBackend }

func NewEngine(backend InferenceBackend) *Engine { return &Engine{backend: backend} }
