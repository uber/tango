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
	"testing"

	"github.com/stretchr/testify/assert"
	pb "github.com/uber/tango/tangopb"
)

func skipUnlessATF(t testing.TB) {
	t.Helper()
	if os.Getenv("TANGO_ATF_BASE_SHA") == "" {
		t.Skip("TANGO_ATF_* env vars not set, skipping AllTargetsFiles tests")
	}
}

func atfBaseSHA(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_ATF_BASE_SHA")
}

func atfHeadSHA(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_ATF_HEAD_SHA")
}

func atfPRURL(t testing.TB) string {
	t.Helper()
	return requiredEnv(t, "TANGO_ATF_PR_URL")
}

// TestIntegration_AllTargetsFiles verifies BUG-015: when an all_targets_files
// trigger fires (.bazelrc changes), NEW and DELETED change types are preserved
// while targets present in both revisions are promoted to CHANGED at distance 0.
func TestIntegration_AllTargetsFiles(t *testing.T) {
	skipUnlessATF(t)
	remote := repoRemote(t)
	addr := startServer(t, remote)
	client := newClient(t, addr)

	t.Run("sha_comparison", func(t *testing.T) {
		ct := getChangedTargets(t, client,
			buildDesc(remote, atfBaseSHA(t)),
			buildDesc(remote, atfHeadSHA(t)),
		)

		t.Logf("NEW: %v", ct.ByType[pb.CHANGE_TYPE_NEW])
		t.Logf("DELETED: %v", ct.ByType[pb.CHANGE_TYPE_DELETED])
		t.Logf("CHANGED: %v", ct.ByType[pb.CHANGE_TYPE_CHANGED])
		t.Logf("Distances: %v", ct.Distances)

		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_NEW], "expected NEW targets")
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "expected DELETED targets")
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_CHANGED], "expected CHANGED targets")

		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_NEW], "//pkg/printer:printer", "NEW")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "//pkg/version:version", "DELETED")

		for _, name := range ct.ByType[pb.CHANGE_TYPE_CHANGED] {
			assert.Equal(t, int32(0), ct.Distances[name],
				"all CHANGED targets must be distance 0 under AllTargetsFiles trigger, but %q has distance %d", name, ct.Distances[name])
		}
	})

	t.Run("pr_change_request", func(t *testing.T) {
		ct := getChangedTargets(t, client,
			buildDesc(remote, atfBaseSHA(t)),
			&pb.BuildDescription{
				Strategy: pb.COMPUTATION_STRATEGY_UNSET,
				Remote:   remote,
				BaseSha:  atfBaseSHA(t),
				Requests: []*pb.Request{{Url: atfPRURL(t)}},
			},
		)

		t.Logf("NEW: %v", ct.ByType[pb.CHANGE_TYPE_NEW])
		t.Logf("DELETED: %v", ct.ByType[pb.CHANGE_TYPE_DELETED])
		t.Logf("CHANGED: %v", ct.ByType[pb.CHANGE_TYPE_CHANGED])
		t.Logf("Distances: %v", ct.Distances)

		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_NEW], "expected NEW targets")
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "expected DELETED targets")
		assert.NotEmpty(t, ct.ByType[pb.CHANGE_TYPE_CHANGED], "expected CHANGED targets")

		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_NEW], "//pkg/printer:printer", "NEW")
		assertContainsTarget(t, ct.ByType[pb.CHANGE_TYPE_DELETED], "//pkg/version:version", "DELETED")

		for _, name := range ct.ByType[pb.CHANGE_TYPE_CHANGED] {
			assert.Equal(t, int32(0), ct.Distances[name],
				"all CHANGED targets must be distance 0 under AllTargetsFiles trigger, but %q has distance %d", name, ct.Distances[name])
		}
	})
}
