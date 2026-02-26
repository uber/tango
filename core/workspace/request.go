package workspace

import (
	"context"
	"net/url"

	"github.com/uber/tango/core/git"
)

// Request represents a change request type, like Phabricator diff or Github pull request
type Request interface {
	Apply(ctx context.Context) error
}

// NewRequest creates a new request based on the request URL.
func NewRequest(rawURL string, g git.Interface, baseRef string, commit string) (Request, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "github":
		return NewGitRequest(g, u.Path, baseRef, commit), nil
	}
	return nil, nil
}
