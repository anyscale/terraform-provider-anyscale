package provider

import "fmt"

// PageTokenGuard stops a hand-rolled pagination loop when the API hands back
// a paging token it has already returned. Following it again would request
// the same pages forever, and the Terraform operation that called the loop
// would hang with no error.
//
// Loops over GET list endpoints use PaginatedRequest; this guard is for the
// loops that cannot, such as the POST search endpoints. Call Next with every
// non-empty next_paging_token before following it.
type PageTokenGuard struct {
	method string
	path   string
	seen   map[string]struct{}
	pages  int
}

// NewPageTokenGuard returns a guard for one walk of one endpoint. method and
// path name the endpoint in the error; pass the path without its query
// string.
func NewPageTokenGuard(method, path string) *PageTokenGuard {
	return &PageTokenGuard{method: method, path: path, seen: map[string]struct{}{}}
}

// Next records a page whose response carried token as its next paging token.
// It returns an error if an earlier page already returned the same token.
func (g *PageTokenGuard) Next(token string) error {
	g.pages++
	if _, repeated := g.seen[token]; repeated {
		return fmt.Errorf("%s %s: the API returned paging token %q again after %d pages; stopping instead of requesting the same pages forever",
			g.method, g.path, token, g.pages)
	}
	g.seen[token] = struct{}{}
	return nil
}
