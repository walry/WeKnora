package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegister_Success exercises the happy path: the server responds 201 with
// a principal but no tokens, and the SDK decodes it.
func TestRegister_Success(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody RegisterRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"message":"Registration successful","user":{"id":"u1","username":"alice","email":"alice@example.com","tenant_id":7}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	out, err := c.Register(context.Background(), RegisterRequest{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "secret1",
	})
	if err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != PathAuthRegister {
		t.Errorf("path = %q, want %q", gotPath, PathAuthRegister)
	}
	if gotBody.Email != "alice@example.com" || gotBody.Password != "secret1" || gotBody.Username != "alice" {
		t.Errorf("unexpected request body: %+v", gotBody)
	}
	if out == nil || !out.Success || out.User == nil {
		t.Fatalf("unexpected response: %+v", out)
	}
	if out.User.Email != "alice@example.com" {
		t.Errorf("user email = %q, want alice@example.com", out.User.Email)
	}
}

// TestRegister_Errors verifies non-2xx responses surface as the canonical
// "HTTP error <status>: <body>" string the CLI's ClassifyHTTPError parses.
func TestRegister_Errors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{"invite_only", http.StatusForbidden, `{"code":1002,"message":"Registration is invite-only"}`, "HTTP error 403"},
		{"email_exists", http.StatusBadRequest, `{"code":1000,"message":"email already exists"}`, "HTTP error 400"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewClient(srv.URL)
			_, err := c.Register(context.Background(), RegisterRequest{Email: "a@b.c", Password: "secret1"})
			if err == nil {
				t.Fatal("Register: want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}
