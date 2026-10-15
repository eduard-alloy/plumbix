// Package client calls a Plumbix server's public API with the local token.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// RequestTimeout bounds each API call.
const RequestTimeout = 30 * time.Second

// Client talks to one server.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New returns a client for the server at addr (host:port).
func New(addr, token string) *Client {
	return &Client{base: "http://" + addr, token: token, http: &http.Client{Timeout: RequestTimeout}}
}

// Response is a raw API response.
type Response struct {
	Status int
	Body   []byte
}

// OK reports whether the status is 2xx.
func (r Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Problem is one invalid field reported by the server.
type Problem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// APIError is the shared error shape, flattened.
type APIError struct {
	Code     string
	Message  string
	Problems []Problem
}

// Error decodes the response's error body. It falls back to the status text.
func (r Response) Error() APIError {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Problems []Problem `json:"problems"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil || body.Error.Code == "" {
		return APIError{Code: strings.ToLower(strings.ReplaceAll(http.StatusText(r.Status), " ", "_")), Message: strings.TrimSpace(string(r.Body))}
	}
	return APIError{Code: body.Error.Code, Message: body.Error.Message, Problems: body.Error.Details.Problems}
}

// Do sends a request with the bearer token. body may be nil.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return Response{}, fmt.Errorf("cannot reach the Plumbix server at %s; is plumbix serve running?", c.base)
		}
		return Response{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	return Response{Status: resp.StatusCode, Body: data}, nil
}
