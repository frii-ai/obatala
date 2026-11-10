package httpwebhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/transport"
	"github.com/frii-ai/obatala/pkg/obatala/transport/httpwebhook"
)

func TestSendBuildsBodyAndHeaders(t *testing.T) {
	var gotBody map[string]string
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
	}))
	defer srv.Close()

	w := &httpwebhook.Sender{
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer t"},
		BuildBody: func(_ context.Context, s transport.Session, text string) ([]byte, error) {
			return json.Marshal(map[string]string{"user": s.Identity, "text": text})
		},
	}
	if _, err := w.Send(context.Background(), transport.Session{ID: "1", Identity: "u1"}, "hi"); err != nil {
		t.Fatal(err)
	}
	if gotBody["user"] != "u1" || gotBody["text"] != "hi" {
		t.Fatalf("body = %v", gotBody)
	}
	if gotHeader.Get("Content-Type") != "application/json" || gotHeader.Get("Authorization") != "Bearer t" {
		t.Fatalf("headers = %v", gotHeader)
	}
}

func TestPostRejectsNon2xxWithHint(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("secret backend detail"))
		}))
		w := &httpwebhook.Sender{
			URL:       srv.URL,
			ErrPrefix: "mybot",
			StatusHint: func(s int) string {
				if s == http.StatusUnauthorized {
					return " (bad token)"
				}
				return ""
			},
		}
		_, err := w.Post(context.Background(), []byte(`{}`))
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected error", status)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("error leaks body: %v", err)
		}
		if status == 401 && err.Error() != "mybot: webhook rejected: HTTP 401 (bad token)" {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestSendReturnsBuildBodyErrorUnwrapped(t *testing.T) {
	boom := errors.New("boom")
	w := &httpwebhook.Sender{
		URL:       "http://127.0.0.1:1",
		BuildBody: func(context.Context, transport.Session, string) ([]byte, error) { return nil, boom },
	}
	if _, err := w.Send(context.Background(), transport.Session{}, "x"); err != boom {
		t.Fatalf("err = %v", err)
	}
}

func TestSendWithoutBuildBodyIsError(t *testing.T) {
	w := &httpwebhook.Sender{URL: "http://127.0.0.1:1"}
	if _, err := w.Send(context.Background(), transport.Session{}, "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestPostUnreachableIsError(t *testing.T) {
	w := &httpwebhook.Sender{URL: "http://127.0.0.1:1"}
	_, err := w.Post(context.Background(), []byte(`{}`))
	if err == nil || !strings.HasPrefix(err.Error(), "httpwebhook: webhook send:") {
		t.Fatalf("err = %v", err)
	}
}
