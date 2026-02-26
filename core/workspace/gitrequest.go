package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/uber/tango/core/git"
)

type gitRequest struct {
	git       git.Interface
	requestID string
	baseRef   string
	commit    string
}

func NewGitRequest(git git.Interface, requestPath string, baseRef string, commit string) Request {
	// get the last part of the request path
	requestID := filepath.Base(requestPath)
	return &gitRequest{
		git:       git,
		requestID: requestID,
		baseRef:   baseRef,
		commit:    commit,
	}
}

// Apply applies the change request to the workspace.
func (r *gitRequest) Apply(ctx context.Context) error {
	ref := fmt.Sprintf("+pull/%s/head:pull/%s/head", r.requestID, r.requestID)
	err := r.git.Fetch(ctx, "origin", ref, "--force", "--no-tags")
	if err != nil {
		return err
	}
	if r.commit != "" {
		actual, err := r.git.RevParse(ctx, fmt.Sprintf("pull/%s/head", r.requestID))
		if err != nil {
			return fmt.Errorf("failed to resolve PR head: %w", err)
		}
		if strings.TrimSpace(actual) != r.commit {
			return fmt.Errorf("PR %s HEAD is %q, expected commit %q", r.requestID, strings.TrimSpace(actual), r.commit)
		}
	}
	patch, err := r.git.Diff(ctx, r.baseRef, fmt.Sprintf("pull/%s/head", r.requestID), "--binary", "--merge-base")
	if err != nil {
		return err
	}
	err = r.git.ApplyPatch(ctx, patch)
	if err != nil {
		return err
	}
	err = r.git.Commit(ctx, fmt.Sprintf("Applied PR: %s", r.requestID), "--allow-empty")
	if err != nil {
		return err
	}
	err = r.git.SubmoduleUpdate(ctx)
	if err != nil {
		return err
	}
	return nil
}
