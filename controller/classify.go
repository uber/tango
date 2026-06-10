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

package controller

import (
	"fmt"

	pb "github.com/uber/tango/tangopb"
)

// promoteToDirectIfNeeded inspects an INDIRECT change and upgrades its type
// to DIRECT when any of the structural triggers fire: a dependency is a
// changed source file, the direct-dependency set differs, or attributes
// differ. NEW and already-DIRECT changes are left alone.
func promoteToDirectIfNeeded(
	ct *pb.ChangedTarget,
	oldT, newT *pb.OptimizedTarget,
	oldMeta, newMeta *pb.Metadata,
	changedSourceFileTargets map[string]struct{},
) error {
	if ct.GetChangeType() == pb.CHANGE_TYPE_DIRECT || ct.GetChangeType() == pb.CHANGE_TYPE_NEW {
		return nil
	}
	if hasDepInChangedSourceFileTargets(newT.GetDirectDependencies(), newMeta, changedSourceFileTargets) {
		ct.ChangeType = pb.CHANGE_TYPE_DIRECT
		return nil
	}
	depsChanged, err := dependenciesChanged(oldT, oldMeta, newT, newMeta)
	if err != nil {
		return fmt.Errorf("failed to check dependencies changed: %w", err)
	}
	if depsChanged {
		ct.ChangeType = pb.CHANGE_TYPE_DIRECT
		return nil
	}
	attrsChanged, err := attributesChanged(oldT, oldMeta, newT, newMeta)
	if err != nil {
		return fmt.Errorf("failed to check attributes changed: %w", err)
	}
	if attrsChanged {
		ct.ChangeType = pb.CHANGE_TYPE_DIRECT
	}
	return nil
}
