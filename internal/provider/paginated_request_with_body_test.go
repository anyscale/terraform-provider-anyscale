package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// PaginatedRequestWithBody must send the same method and request body with
// every page, not only the first: an io.Reader is drained by the first
// request, and the POST search endpoints read their filters from the body,
// so a page 2 sent without it would silently return unfiltered results.
func TestPaginatedRequestWithBody_SendsBodyOnEveryPage(t *testing.T) {
	const want = `{"name":{"equals":"filtered"},"version":-2}`
	var (
		mu      sync.Mutex
		methods []string
		bodies  []string
		tokens  []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		token := r.URL.Query().Get("paging_token")
		mu.Lock()
		methods = append(methods, r.Method)
		bodies = append(bodies, string(b))
		tokens = append(tokens, token)
		n := len(bodies)
		mu.Unlock()
		if n > 5 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		next := any("page-2")
		if token == "page-2" {
			next = nil
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results":  []map[string]string{{"id": "item-" + token}},
			"metadata": map[string]any{"next_paging_token": next},
		})
	}))
	defer server.Close()

	type item struct {
		ID string `json:"id"`
	}
	got, err := PaginatedRequestWithBody(t.Context(), NewClientWithToken(server.URL, "test-token"),
		http.MethodPost, "/api/v2/search", nil, []byte(want),
		func(body []byte) ([]item, *string, error) {
			var resp struct {
				Results  []item `json:"results"`
				Metadata struct {
					NextPagingToken *string `json:"next_paging_token"`
				} `json:"metadata"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				return nil, nil, err
			}
			return resp.Results, resp.Metadata.NextPagingToken, nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d items, want one from each of 2 pages: %+v", len(got), got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("made %d requests, want 2", len(bodies))
	}
	for i := range bodies {
		if methods[i] != http.MethodPost {
			t.Errorf("page %d sent %s, want POST", i+1, methods[i])
		}
		if bodies[i] != want {
			t.Errorf("page %d body = %q, want %q", i+1, bodies[i], want)
		}
	}
	if tokens[0] != "" || tokens[1] != "page-2" {
		t.Errorf("paging_token per page = %q, want [\"\" \"page-2\"]", tokens)
	}
}
