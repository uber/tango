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

package metrics

import "github.com/uber-go/tally"

// Option customizes a single emission. Options compose; later WithTags
// override earlier on key collision.
type Option func(*emitOpts)

type emitOpts struct {
	tags            map[string]string
	durationBuckets tally.DurationBuckets
	valueBuckets    tally.ValueBuckets
}

// applyOptions folds opts into a single emitOpts.
func applyOptions(opts []Option) emitOpts {
	var o emitOpts
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithTags attaches key/value tags to a single emission.
func WithTags(tags map[string]string) Option {
	return func(o *emitOpts) {
		if len(tags) == 0 {
			return
		}
		if o.tags == nil {
			o.tags = make(map[string]string, len(tags))
		}
		for k, v := range tags {
			o.tags[k] = v
		}
	}
}

// WithDurationBuckets overrides the emitter's default duration buckets for
// a single RecordDur call.
func WithDurationBuckets(b tally.DurationBuckets) Option {
	return func(o *emitOpts) { o.durationBuckets = b }
}

// WithValueBuckets overrides the emitter's default value buckets for a
// single RecordCount call.
func WithValueBuckets(b tally.ValueBuckets) Option {
	return func(o *emitOpts) { o.valueBuckets = b }
}
