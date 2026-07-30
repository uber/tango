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

package storage

import (
	"encoding/json"
	"errors"
	"fmt"
)

// streamFormatVersion is the current stream format version written by the writer.
const streamFormatVersion = 1

// ErrStreamCorrupted indicates that a persisted stream is incomplete or
// internally inconsistent — for example a missing footer or a record count
// mismatch. Callers should treat this as a corrupt cache entry.
var ErrStreamCorrupted = errors.New("stream corrupted")

// streamEnvelope is the top-level JSON structure used for header and footer
// records. Data records never contain these keys, so the discriminator is
// unambiguous.
type streamEnvelope struct {
	Header *streamHeader `json:"__tango_stream_header,omitempty"`
	Footer *streamFooter `json:"__tango_stream_footer,omitempty"`
}

// streamHeader marks the beginning of a versioned stream.
type streamHeader struct {
	Version int `json:"version"`
}

// streamFooter marks the end of a complete stream and carries the number of
// data records written between the header and footer.
type streamFooter struct {
	RecordCount int `json:"record_count"`
}

// encodeHeader writes a stream header record to enc.
func encodeHeader(enc *json.Encoder) error {
	return enc.Encode(streamEnvelope{
		Header: &streamHeader{Version: streamFormatVersion},
	})
}

// encodeFooter writes a stream footer record carrying recordCount to enc.
func encodeFooter(enc *json.Encoder, recordCount int) error {
	return enc.Encode(streamEnvelope{
		Footer: &streamFooter{RecordCount: recordCount},
	})
}

// parseEnvelope attempts to decode raw JSON as a stream envelope. It returns
// the envelope and true if the JSON contains a header or footer key, or a
// zero envelope and false if the JSON is a plain data record.
func parseEnvelope(raw json.RawMessage) (streamEnvelope, bool) {
	var env streamEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return streamEnvelope{}, false
	}
	if env.Header != nil || env.Footer != nil {
		return env, true
	}
	return streamEnvelope{}, false
}

// newCorruptedError returns an error wrapping ErrStreamCorrupted with a
// descriptive message.
func newCorruptedError(msg string) error {
	return fmt.Errorf("%s: %w", msg, ErrStreamCorrupted)
}
