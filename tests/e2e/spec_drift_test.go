//go:build e2e

package e2e

// Drift check: every response the Wallet Service gives to an e2e request is validated
// against docs/api/wallet-service.openapi.yaml. A response whose status, headers or body the spec does
// not describe fails the run, so the spec cannot silently fall behind the code. Only the shapes the
// suite already provokes are covered; statuses it never triggers stay unchecked.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

const specPath = "../../docs/api/wallet-service.openapi.yaml"

type specValidator struct {
	next   http.RoundTripper
	router routers.Router

	mu         sync.Mutex
	violations []string
}

func newSpecValidator(next http.RoundTripper) (*specValidator, error) {
	doc, err := openapi3.NewLoader().LoadFromFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", specPath, err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}
	return &specValidator{next: next, router: router}, nil
}

func (v *specValidator) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := v.next.RoundTrip(req)
	if err != nil || !strings.HasPrefix(req.URL.String(), walletBaseURL) {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	route, params, rerr := v.router.FindRoute(req)
	if rerr != nil {
		v.record(req, resp.StatusCode, fmt.Sprintf("no operation in the spec: %v", rerr))
		return resp, nil
	}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request: req, PathParams: params, Route: route,
		},
		Status:  resp.StatusCode,
		Header:  resp.Header,
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	in.SetBodyBytes(body)
	if verr := openapi3filter.ValidateResponse(context.Background(), in); verr != nil {
		v.record(req, resp.StatusCode, verr.Error())
	}
	return resp, nil
}

func (v *specValidator) record(req *http.Request, status int, msg string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.violations = append(v.violations, fmt.Sprintf("%s %s -> %d: %s", req.Method, req.URL.Path, status, msg))
}

// TestMain wires the validator into http.DefaultClient (every helper uses it) and fails the run
// if any response drifted from the spec, even when all the tests themselves passed.
func TestMain(m *testing.M) {
	v, err := newSpecValidator(http.DefaultTransport)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spec drift check:", err)
		os.Exit(1)
	}
	http.DefaultClient.Transport = v
	code := m.Run()
	seen := map[string]bool{}
	for _, msg := range v.violations {
		if !seen[msg] {
			seen[msg] = true
			fmt.Fprintln(os.Stderr, "SPEC DRIFT:", msg)
		}
	}
	if len(seen) > 0 && code == 0 {
		code = 1
	}
	os.Exit(code)
}
