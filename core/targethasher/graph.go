package targethasher

import (
	"context"
	"fmt"
	"sort"
	"strings"

	buildpb "github.com/bazelbuild/buildtools/build_proto"
)

const (

	// SourceFileType is a bazel query pattern for referring to source files in
	// queries. See: https://docs.bazel.build/versions/master/query.html#kind
	SourceFileType = "source file"

	// ExternalRuleType is a bazel repository rule gotten by //external: query prefix
	ExternalRuleType = "external rule"

	// PackageGroup is a bazel query pattern for referring to package group in
	// queries. See: https://docs.bazel.build/versions/master/query.html#kind
	PackageGroup = "package group"

	// GeneratedFileType is a bazel query pattern for referring to generated files
	// in queries. See: https://docs.bazel.build/versions/master/query.html#kind
	GeneratedFileType = "generated file"
)

// Result contains a graph of targethashes calculated based on a bazel query.
type Result struct {

	// TargetNames is a topoplogically ordered list of target names.
	TargetNames []string

	// Targets contains a map of target names to Target for each target
	Targets map[string]Target

	// ExternalTargets contains a map of external target names to Target
	ExternalTargets map[string]Target

	// Warnings contains lists of possible issues encountered while trying to
	// calculate target hashes. These can happen in a few cases:
	//
	//  - Bazel targets specify a dependency on a directory, rather than source
	//    files. In this case the build system can only track changes in the
	//    directory itself, not its contents. This could impact the accuracy of
	//    the targethash but is not considered fatal as the build system cannot
	//    track this correctly either.
	//
	//		For more info see https://docs.bazel.build/versions/master/build-ref.html#label_directory.
	Warnings map[string]error
}

// Target contains information about the hash for a single target
// and a list of target names for direct dependencies.
type Target struct {
	Name string
	// Hash is the hash of the target.
	// It is calculated based on the properties of the target itself and may include the hashes of its dependencies, depending on the target type.
	Hash []byte
	// HashWithoutDeps is the hash of the target without its dependencies.
	// It is calculated based on the properties of the target itself.
	HashWithoutDeps []byte
	RuleType        string
	Deps            []string
	Tags            []string
	Root            bool
	External        bool
	SourceFile      *buildpb.SourceFile
	Rule            *buildpb.Rule
	Attributes      []*buildpb.Attribute
}

// EmptyResult returns a result for no targets.
func EmptyResult() Result {
	return Result{
		Targets: make(map[string]Target),
	}
}

// FromProto calculates a target hash graph based on a query result and workspace root
// Because `bazel query --output=proto --order_output=full` is very expensive, we make all the
// required computations in this function, so Bazel can be executed with `--order_output=no`.
func FromProto(ctx context.Context, r *buildpb.QueryResult, workspaceroot string, hashConfig HashConfig, bzlmodEnabled bool) (Result, error) {
	// always calculate hash for individual files in the main repo.
	fullHashRepos := map[string]struct{}{"": struct{}{}}
	for _, repo := range hashConfig.FullHashRepos {
		fullHashRepos[repo] = struct{}{}
	}
	excludedFiles := map[string]struct{}{}
	for _, repo := range hashConfig.ExcludedFiles {
		excludedFiles[repo] = struct{}{}
	}
	// Create a m

	return fromProto(ctx, r, &diskHashHelper{
		workspaceroot:   workspaceroot,
		knownFileHashes: hashConfig.KnownSourceHashes,
		fullHashRepos:   fullHashRepos,
		excludedFiles:   excludedFiles,
		bzlmodEnabled:   bzlmodEnabled,
	})
}

// FromProtoNoHash calculates a DAG graph based on a query result. It does not calculate hashes for targets.
func FromProtoNoHash(ctx context.Context, r *buildpb.QueryResult) (Result, error) {
	return fromProto(ctx, r, &noOpHasher{})
}

// for external targets, url and urls attributes could cause non-deterministic hash values,
// depending on if port number is present or not
// as a short term solution, remove these two attributes only if attribute sha256 exists on the external target
var (
	urlAttrs      = map[string]struct{}{"url": struct{}{}, "urls": struct{}{}}
	sha256Attr    = "sha256"
	integrityAttr = "integrity"
)

func removeURLAttrs(t *buildpb.Target) {
	if t.Rule == nil {
		return
	}

	oldAttrs := t.GetRule().GetAttribute()
	var willCheckContent bool
	for _, attr := range oldAttrs {
		if attr.GetName() == sha256Attr || attr.GetName() == integrityAttr {
			willCheckContent = true
			break
		}
	}
	// no-op if sha256 is not present
	if !willCheckContent {
		return
	}

	newAttrs := make([]*buildpb.Attribute, 0, len(oldAttrs))
	for _, attr := range oldAttrs {
		if _, ok := urlAttrs[attr.GetName()]; !ok {
			newAttrs = append(newAttrs, attr)
		}
	}
	t.Rule.Attribute = newAttrs
}

func toTarget(t *buildpb.Target) (Target, bool, error) {
	switch *t.Type {
	case buildpb.Target_RULE:
		targetName := t.Rule.GetName()
		deps := t.Rule.GetRuleInput()
		// sorting dependencies of rules, just like we do when calculating hashes for these rules.
		sort.Strings(deps)
		h := newHash()
		if strings.HasPrefix(targetName, externalWorkspaceRulePrefix) {
			// if this is an external target, remove unwanted attributes from the rule, e.g. url and urls
			removeURLAttrs(t)
			// Workspace rule usually representing external repository or an HTTP file.
			// It is only used to hash external source files, so store it in a separate map.
			HashRuleCommon(t.Rule, h)
			return Target{
				Name:     targetName,
				Hash:     h.Sum(nil),
				RuleType: ExternalRuleType,
				Deps:     deps,
				Rule:     t.Rule,
			}, true, nil
		}
		HashRuleCommon(t.Rule, h)
		return Target{
			Name:            targetName,
			HashWithoutDeps: h.Sum(nil),
			RuleType:        t.Rule.GetRuleClass(),
			Deps:            deps,
			Tags:            TagsFromRule(t.GetRule()),
			External:        isExternalTarget(targetName),
			Rule:            t.Rule,
			Attributes:      t.Rule.GetAttribute(),
		}, false, nil
	case buildpb.Target_SOURCE_FILE:
		targetName := t.SourceFile.GetName()
		return Target{
			Name:       targetName,
			RuleType:   SourceFileType,
			External:   isExternalTarget(targetName),
			SourceFile: t.GetSourceFile(),
		}, false, nil
	case buildpb.Target_GENERATED_FILE:
		targetName := t.GeneratedFile.GetName()
		return Target{
			Name:     targetName,
			RuleType: GeneratedFileType,
			Deps:     []string{t.GeneratedFile.GetGeneratingRule()},
			External: isExternalTarget(targetName),
		}, false, nil
	case buildpb.Target_PACKAGE_GROUP:
		targetName := t.PackageGroup.GetName()
		return Target{
			Name:     targetName,
			RuleType: PackageGroup,
			External: isExternalTarget(targetName),
		}, false, nil
	default:
		return Target{}, false, fmt.Errorf("cannot handle target type %q", buildpb.Target_Discriminator_name[int32(*t.Type)])
	}
}

// GetTargetsWithoutHashAndRootInfo translate raw query result to a map of targets and a map of external targets without hash and root information
func GetTargetsWithoutHashAndRootInfo(ctx context.Context, r *buildpb.QueryResult) (targets map[string]Target, externalTargets map[string]Target, err error) {
	targets = make(map[string]Target)
	externalTargets = make(map[string]Target)
	for _, t := range r.Target {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		target, isExternalWorkspace, err := toTarget(t)
		if err != nil {
			return nil, nil, err
		}
		if isExternalWorkspace {
			externalTargets[target.Name] = target
		} else {
			targets[target.Name] = target
		}
	}
	return targets, externalTargets, nil
}

// GetTopologicalRootsAndIdentifyBuildableRoots returns a list of topological roots and marks buildable roots in the target graph
func GetTopologicalRootsAndIdentifyBuildableRoots(targets map[string]Target) []string {
	// get targets that cannot be root, i.e. dependencies of some other targets
	notRoots := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		for _, dep := range target.Deps {
			notRoots[dep] = struct{}{}
		}
	}

	// targets that are not marked as notRoots are roots
	var roots []string
	for name, target := range targets {
		if _, ok := notRoots[name]; !ok {
			roots = append(roots, name)
			if CanBeRoot(target.RuleType) {
				// `Root` field in Target has slightly different meaning than a `root` used in this
				// algorithm. Target only wants to mark buildable nodes as Root, but not other nodes, like package
				// groups - and unfortunately there is a lot of assumptions in downward code around this. So we will use
				// a separate `roots` variable for the purpose of this algorithm which will have all nodes not
				// referenced by other nodes.
				target.Root = true
				targets[name] = target
			}
		}
	}
	// Sorting roots here instead to guarantee the order of traversal for root
	// targets stay the same in between revisions. This is done to eliminate or
	// minimize the effect of the order of root targets traversed has on the
	// calculated hashes of targets that are on a loop/cycle in the build graph.
	sort.Strings(roots)
	return roots
}

func fromProto(ctx context.Context, r *buildpb.QueryResult, hasher SourceHasher) (Result, error) {
	// Build target graph with dependencies, but without hash and root information.
	targets, externalTargets, err := GetTargetsWithoutHashAndRootInfo(ctx, r)
	if err != nil {
		return EmptyResult(), err
	}
	// get topological roots and update buildable roots info
	roots := GetTopologicalRootsAndIdentifyBuildableRoots(targets)

	// Target graph is constructed with all the dependencies. Traverse it now and build Merkle DAG by hashing file
	// contents and rule's metainfo and hashes of dependencies.
	// This is potentially parallelizable, see https://t3.uberinternal.com/browse/GM-1523
	warns := make(map[string]error)
	for _, name := range roots {
		if _, err := HashRecursively(ctx, targets, externalTargets, name, hasher, warns); err != nil {
			return Result{}, err
		}
	}

	// Build topologically sorted list of targets. This is to comply with the Result{} interface. The list is required
	// to be topologically sorted for the diffing algorithm to work properly which compares two target graphs. It is
	// possible in the future to change diffing algorithm to work on unsorted graphs with the same theoretical
	// efficiency, in which case below sorting would deem unnecessary.
	targetNames := make([]string, 0, len(targets))
	visited := make(map[string]struct{}, len(targets))
	for _, name := range roots {
		targetNames, err = ToposortRecursively(ctx, targets, name, targetNames, visited)
		if err != nil {
			return EmptyResult(), err
		}
	}

	// This shouldn't happen on a correct build graph, but if a change introduces
	// cycles with no roots pointing into them, they won't appear in the list of
	// targets.
	if len(targetNames) != len(targets) {
		cyclicTargets := make([]string, 0, len(targets)-len(targetNames))
		for name := range targets {
			if _, ok := visited[name]; !ok {
				cyclicTargets = append(cyclicTargets, name)
			}
		}

		// Sort lexicographically since topological ordering is undefined.
		sort.Strings(cyclicTargets)
		targetNames = append(targetNames, cyclicTargets...)

		for _, name := range cyclicTargets {
			// Consider all root-uncreachable cyclic targets to be pseudo-roots.
			// An alternative would be to designate a deterministic cycle-breaker as
			// the root, but this is likely not worh the extra complexity.
			target := targets[name]
			target.Root = true
			targets[name] = target

			if _, err := HashRecursively(ctx, targets, externalTargets, name, hasher, warns); err != nil {
				return Result{}, err
			}
		}
	}

	return Result{
		TargetNames:     targetNames,
		Targets:         targets,
		ExternalTargets: externalTargets,
		Warnings:        warns,
	}, nil
}

// ToposortRecursively forms a slice of target names in the specific order, so that leaf targets always go before parent
// targets. Sibling targets are also sorted alphabetically by their name, this all gives back a stable topological sort.
// The function accepts `targets` parameter of currently formed slice of topologically sorted targets and returns
// it as well, conforming to Golang slice modification pattern.
//
// Additionally, this function needs to honor context cancellation
func ToposortRecursively(ctx context.Context, targetHashes map[string]Target, name string, targets []string, visited map[string]struct{}) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if _, ok := visited[name]; ok {
		return targets, nil
	}
	visited[name] = struct{}{}

	target, ok := targetHashes[name]
	if !ok {
		// This should never be true if dependency graph is formed properly
		return targets, nil
	}

	if len(target.Deps) > 0 {
		sort.Strings(target.Deps)
		// target.Deps are updated in-place, no need to update targetHashes map

		var err error
		for _, dep := range target.Deps {
			targets, err = ToposortRecursively(ctx, targetHashes, dep, targets, visited)
			if err != nil {
				return nil, err
			}
		}
	}

	// Append current target at the end, so dependencies always come before parent targets
	return append(targets, name), nil
}

// HashRecursively calculates hash for all of the target nodes in the target graph and updates Hash property for each
// entry in `targetHashes` map.
// It populates `warns` map with warnings when hash cannot be calculated but does not stop the calculation. An example
// of this could be a file added to a dependency graph which is not present on the file system in certain scenarios,
// like flaky test filter. This is potentially dangerous as it can silently render an empty hash target graph. At some
// point we should modify the code to only allow well-known files or errors to exhibit this behavior.
//
// Additionally, this function needs to honor context cancellation
func HashRecursively(ctx context.Context, targets, externalTargets map[string]Target, targetName string, hasher SourceHasher, warns map[string]error) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if external, ok := externalTargets[targetName]; ok {
		return []byte(external.Hash), nil
	}
	target, ok := targets[targetName]
	if !ok {
		return nil, fmt.Errorf("Unable to find target %s in the target graph", targetName)
	}

	if target.Hash != nil {
		// this node was already visited
		return []byte(target.Hash), nil
	}

	// Mark node as visited by setting it to an empty slice instead of nil slice which it got by default - to avoid
	// cycles. It would be reset to a real hash value once dependencies are traversed.
	target.Hash = []byte{}
	target.HashWithoutDeps = []byte{}
	targets[targetName] = target

	var hash []byte
	var hashWithoutDeps []byte

	switch target.RuleType {
	case SourceFileType:
		h, err := hasher.HashSourceFile(target.SourceFile, externalTargets)
		if err != nil {
			return nil, err
		}
		hash = h
	case GeneratedFileType:
		// In case of a generated file, it is sufficient to use the hash of the generating rule.
		// Note that generated file targets aren't explicitly defined targets,
		// but `out`'s of other rules: they have no attributes to hash.
		if dephash, err := HashRecursively(ctx, targets, externalTargets, target.Deps[0], hasher, warns); err == nil {
			hash = dephash
		} else {
			return nil, err
		}
	case PackageGroup:
		// Package groups do not execute any action nor can be used as inputs of any action
		// There is no need to actually compute the hash, we include only the name for consistency
		h := newHash()
		h.Write([]byte(targetName))
		hash = h.Sum(nil)
	default:
		// Regular rule
		h := newHash()
		noDepsHasher := newHash()
		HashRuleCommon(target.Rule, noDepsHasher)
		hashWithoutDeps = noDepsHasher.Sum(nil)
		h.Write(hashWithoutDeps)
		for _, dep := range target.Deps {
			if dephash, err := HashRecursively(ctx, targets, externalTargets, dep, hasher, warns); err == nil {
				h.Write(dephash)
			} else {
				return nil, err
			}
		}
		hash = h.Sum(nil)
	}

	if hash != nil {
		_, isNoOpHasher := hasher.(*noOpHasher)
		if isNoOpHasher {
			target.Hash = []byte{}
			target.HashWithoutDeps = []byte{}
		} else {
			target.Hash = hash
			target.HashWithoutDeps = hashWithoutDeps
		}
		targets[targetName] = target
	}

	return hash, nil
}

// TagsFromRule gets the tags in the rule's attributes
func TagsFromRule(r *buildpb.Rule) []string {
	for _, a := range r.Attribute {
		if a.GetType() == buildpb.Attribute_STRING_LIST && a.GetName() == "tags" {
			return a.GetStringListValue()
		}
	}
	return nil
}

// CanBeRoot returns true if the ruleType represents a buildable rule
func CanBeRoot(ruleType string) bool {
	return ruleType != SourceFileType && ruleType != PackageGroup
}

func isExternalTarget(targetName string) bool {
	return strings.HasPrefix(targetName, externalWorkspaceFilePrefix)
}

func isExternalWorkspaceType(t *buildpb.Target) bool {
	switch *t.Type {
	case buildpb.Target_SOURCE_FILE:
		return strings.HasPrefix(t.SourceFile.GetName(), externalWorkspaceRulePrefix)
	case buildpb.Target_RULE:
		return strings.HasPrefix(t.Rule.GetName(), externalWorkspaceRulePrefix)
	case buildpb.Target_GENERATED_FILE:
		return strings.HasPrefix(t.GeneratedFile.GetName(), externalWorkspaceRulePrefix)
	default:
		return false
	}
}

func isWorkspaceRootRule(t *buildpb.Target, rootRulePrefixes []string) bool {
	if *t.Type == buildpb.Target_RULE {
		return hasAnyPrefix(t, rootRulePrefixes)
	}
	return false
}

func hasAnyPrefix(t *buildpb.Target, rootRulePrefixes []string) bool {
	for _, rootPrefix := range rootRulePrefixes {
		if strings.HasPrefix(t.Rule.GetName(), rootPrefix) {
			return true
		}
	}
	return false
}
