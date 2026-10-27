// Copyright (c) 2026 Uber Technologies, Inc.
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

package integration_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	pb "github.com/uber/tango/tangopb"
)

func skipUnlessBzlmod(t testing.TB) {
	t.Helper()
	if os.Getenv("TANGO_BZLMOD_BASE_SHA") == "" {
		t.Skip("TANGO_BZLMOD_* env vars not set, skipping Bzlmod upgrade tests")
	}
}

func bzlmodRemote(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_BZLMOD_REMOTE")
}

func bzlmodBaseSHA(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_BZLMOD_BASE_SHA")
}

func bzlmodHeadSHA(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_BZLMOD_HEAD_SHA")
}

func bzlmodPRURL(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_BZLMOD_PR_URL")
}

// bzlmodTestifyRepo is the apparent-name prefix of the upgraded Bzlmod repo.
const bzlmodTestifyRepo = "@com_github_stretchr_testify//"

// bzlmodDependentTests are the first-party targets that depend on testify and
// so are one hop from the upgraded repo.
var bzlmodDependentTests = []string{
	"//pkg/errors:errors_test",
	"//pkg/strutil:strutil_test",
	"//service/api:api_test",
	"//service/config:config_test",
	"//service/handlers:handlers_test",
}

// TestIntegration_BzlmodUpgrade checks that upgrading a Bzlmod dependency
// (testify, via go.mod and go_deps) reports the targets that reference it by
// apparent name (@com_github_stretchr_testify//...) as changed: the repo's own
// targets at distance 0, their dependents at distance 1, and nothing else.
func TestIntegration_BzlmodUpgrade(t *testing.T) {
	skipUnlessBzlmod(t)
	remote := bzlmodRemote(t)
	addr := startServer(t, remote)
	client := newClient(t, addr)

	t.Run("sha_comparison", func(t *testing.T) {
		ct := getChangedTargets(t, client,
			buildDesc(remote, bzlmodBaseSHA(t)),
			buildDesc(remote, bzlmodHeadSHA(t)),
		)
		assertBzlmodUpgrade(t, ct)
	})

	t.Run("pr_change_request", func(t *testing.T) {
		ct := getChangedTargets(t, client,
			buildDesc(remote, bzlmodBaseSHA(t)),
			&pb.BuildDescription{
				Strategy: pb.COMPUTATION_STRATEGY_UNSET,
				Remote:   remote,
				BaseSha:  bzlmodBaseSHA(t),
				Requests: []*pb.Request{{Url: bzlmodPRURL(t)}},
			},
		)
		assertBzlmodUpgrade(t, ct)
	})
}

func assertBzlmodUpgrade(t *testing.T, ct parsedChangedTargets) {
	t.Helper()

	assert.Empty(t, ct.ByType[pb.CHANGE_TYPE_NEW], "upgrade must not add targets")
	assert.Empty(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "upgrade must not delete targets")

	var external, firstParty []string
	for _, name := range ct.ByType[pb.CHANGE_TYPE_CHANGED] {
		if strings.HasPrefix(name, bzlmodTestifyRepo) {
			external = append(external, name)
		} else {
			firstParty = append(firstParty, name)
		}
	}

	assert.NotEmpty(t, external, "expected targets of the upgraded repo to be CHANGED")
	for _, name := range external {
		assert.Equal(t, int32(0), ct.Distances[name], "upgraded repo target %q must be distance 0", name)
	}

	assert.ElementsMatch(t, bzlmodDependentTests, firstParty,
		"only first-party targets depending on the upgraded repo may be CHANGED")
	for _, name := range bzlmodDependentTests {
		assert.Equal(t, int32(1), ct.Distances[name], "%q depends directly on the upgraded repo", name)
	}
}
