// Copyright (c) 2025 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package changeuri parses and validates change URIs — the system-wide
// identity of a code change, per the submitqueue change-URI RFC
// (https://github.com/uber/submitqueue/blob/main/doc/rfc/change-uri.md).
//
// A change URI is an RFC 3986 URI of the form scheme://{host[:port]}/{path}
// whose path pins the change to an exact code state. Only the GitHub pull
// request scheme is supported:
//
//	github://{host[:port]}/{org}/{repo}/pull/{pr}/{head_sha}
//
// URIs are compared as opaque strings everywhere (cache keys, correlation
// keys), so exactly one spelling per change is valid. Parse validates the
// canonical form and rejects everything else — it never normalizes, because
// normalization applied at one entry point and skipped at another lets two
// spellings of one change into the system.
package changeuri

import (
	"fmt"
	"net/url"
	"strings"
)

// Scheme is the URI scheme for GitHub pull requests.
const Scheme = "github"

// PullRequest is the parsed form of a canonical GitHub pull request URI.
// Re-serializing with String yields the original URI byte-for-byte.
type PullRequest struct {
	// Host is the provider instance as host[:port]. The hostname is
	// lowercase; the port, when present, is digits only and kept verbatim.
	Host string
	// Org is the organization path within the instance. It may span
	// multiple path segments (e.g. "uber/frontend") and is kept verbatim.
	Org string
	// Repo is the repository name, kept verbatim.
	Repo string
	// Number is the pull request number: a positive integer without
	// leading zeros, kept as a string to preserve the canonical spelling.
	Number string
	// HeadSHA is the PR's head commit at submission time: the full
	// 40-character lowercase hex form. It pins the exact code state the
	// URI identifies.
	HeadSHA string
}

// String serializes the pull request back to its canonical URI form.
func (p PullRequest) String() string {
	return Scheme + "://" + p.Host + "/" + p.Org + "/" + p.Repo + "/pull/" + p.Number + "/" + p.HeadSHA
}

// Parse validates raw as a canonical GitHub pull request change URI and
// returns its parsed form. Non-canonical spellings (uppercase host,
// abbreviated or uppercase SHA, leading-zero PR number, query, fragment,
// userinfo, empty path segments) are rejected, never normalized.
func Parse(raw string) (PullRequest, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return PullRequest{}, fmt.Errorf("parse change URI: %w", err)
	}
	if err := validateURL(raw, u); err != nil {
		return PullRequest{}, err
	}

	// {org…}/{repo}/pull/{pr}/{head_sha}: org spans one or more segments,
	// so the layout is anchored from the end of the path.
	segments := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(segments) < 5 {
		return PullRequest{}, fmt.Errorf("change URI path must be {org}/{repo}/pull/{pr}/{head_sha}: %q", raw)
	}
	for _, s := range segments {
		if s == "" {
			return PullRequest{}, fmt.Errorf("change URI path must not contain empty segments: %q", raw)
		}
	}
	n := len(segments)
	if segments[n-3] != "pull" {
		return PullRequest{}, fmt.Errorf("change URI path must be {org}/{repo}/pull/{pr}/{head_sha}: %q", raw)
	}
	number, headSHA := segments[n-2], segments[n-1]
	if !isCanonicalNumber(number) {
		return PullRequest{}, fmt.Errorf("pull request number must be a positive integer without leading zeros, got %q", number)
	}
	if !isCanonicalSHA(headSHA) {
		return PullRequest{}, fmt.Errorf("head SHA must be the full 40-character lowercase hex form, got %q", headSHA)
	}

	p := PullRequest{
		Host:    u.Host,
		Org:     strings.Join(segments[:n-4], "/"),
		Repo:    segments[n-4],
		Number:  number,
		HeadSHA: headSHA,
	}
	// Canonical form round-trips byte-for-byte. This catches every
	// non-canonical spelling net/url silently tolerates (uppercase scheme,
	// percent-encoding variants, default-port cosmetics).
	if p.String() != raw {
		return PullRequest{}, fmt.Errorf("change URI is not in canonical form: %q", raw)
	}
	return p, nil
}

// validateURL enforces the URI-level canonical-form rules: the github
// scheme, an authority with a lowercase hostname and an optional
// digits-only port, and no userinfo, query, or fragment.
func validateURL(raw string, u *url.URL) error {
	if u.Scheme != Scheme {
		return fmt.Errorf("unsupported scheme %q: only %q is supported", u.Scheme, Scheme)
	}
	if u.Opaque != "" {
		return fmt.Errorf("change URI must have an authority: %q", raw)
	}
	if u.User != nil {
		return fmt.Errorf("change URI must not contain userinfo: %q", raw)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("change URI must not contain a query or fragment: %q", raw)
	}
	return validateHost(u)
}

// validateHost enforces the RFC host rules: required, lowercase hostname
// (rejected, not folded), and an optional digits-only port kept verbatim.
func validateHost(u *url.URL) error {
	if u.Hostname() == "" {
		return fmt.Errorf("change URI host is required: %q", u.String())
	}
	if strings.ToLower(u.Hostname()) != u.Hostname() {
		return fmt.Errorf("change URI host must be lowercase: %q", u.Host)
	}
	// net/url validates that a present port is numeric, but tolerates a
	// trailing colon with an empty port, which would round-trip.
	if strings.HasSuffix(u.Host, ":") {
		return fmt.Errorf("change URI port must not be empty: %q", u.Host)
	}
	return nil
}

// isCanonicalNumber reports whether s is a positive integer without leading
// zeros.
func isCanonicalNumber(s string) bool {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isCanonicalSHA reports whether s is a full 40-character lowercase hex
// commit SHA.
func isCanonicalSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
