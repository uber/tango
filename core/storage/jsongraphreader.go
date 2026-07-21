package storage

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/uber/tango/entity"
	pb "github.com/uber/tango/tangopb"
)

// jsonGraphReader streams targets from a JSON blob one at a time,
// batching them into proto responses bounded by maxMessageBytes.
//
// Storage format:
//
//	{"targets": [{...}, {...}, ...], "metadata": {...}}
//
// The reader uses json.Decoder to walk the JSON token-by-token, decoding
// one entity.OptimizedTarget at a time. Targets are converted to proto,
// accumulated until the batch's proto wire size would exceed
// maxMessageBytes, then yielded as a *pb.GetTargetGraphResponse. Metadata
// is read last and returned as a single message.
type jsonGraphReader struct {
	dec             *json.Decoder
	rc              io.ReadCloser
	maxMessageBytes int

	// State machine: targets phase → metadata phase → done.
	targetsExhausted bool
	metadataDone     bool

	// Buffered target from a previous iteration that didn't fit.
	carry     *pb.OptimizedTarget
	carrySize int
}

// NewJSONGraphReader opens the blob at key and returns a GraphReader that
// streams targets from JSON, batching them into proto responses whose
// wire size stays at or under maxMessageBytes.
func NewJSONGraphReader(rc io.ReadCloser, maxMessageBytes int) (GraphReader, error) {
	dec := json.NewDecoder(rc)

	// Advance past opening '{' and "targets" key and opening '['.
	if err := expectToken(dec, json.Delim('{')); err != nil {
		rc.Close()
		return nil, fmt.Errorf("expected opening '{': %w", err)
	}
	fieldKey, err := readStringToken(dec)
	if err != nil || fieldKey != "targets" {
		rc.Close()
		return nil, fmt.Errorf("expected 'targets' key, got %q: %w", fieldKey, err)
	}
	if err := expectToken(dec, json.Delim('[')); err != nil {
		rc.Close()
		return nil, fmt.Errorf("expected opening '[': %w", err)
	}

	return &jsonGraphReader{
		dec:             dec,
		rc:              rc,
		maxMessageBytes: maxMessageBytes,
	}, nil
}

// Read returns the next batched proto response. Returns io.EOF when all
// targets and metadata have been yielded.
func (r *jsonGraphReader) Read() (*pb.GetTargetGraphResponse, error) {
	if !r.targetsExhausted {
		return r.readTargetBatch()
	}
	if !r.metadataDone {
		r.metadataDone = true
		return r.readMetadata()
	}
	return nil, io.EOF
}

// readTargetBatch reads individual targets from the JSON array, converts
// each to proto, and accumulates until the batch would exceed maxMessageBytes.
func (r *jsonGraphReader) readTargetBatch() (*pb.GetTargetGraphResponse, error) {
	var batch []*pb.OptimizedTarget
	batchBytes := 0

	// Start with any carry-over from the previous call.
	if r.carry != nil {
		batch = append(batch, r.carry)
		batchBytes = r.carrySize
		r.carry = nil
		r.carrySize = 0
	}

	for r.dec.More() {
		var et entity.OptimizedTarget
		if err := r.dec.Decode(&et); err != nil {
			return nil, fmt.Errorf("decode target: %w", err)
		}

		pt := entityTargetToProto(&et)
		itemBytes := pt.Size()

		if len(batch) > 0 && batchBytes+itemBytes > r.maxMessageBytes {
			// This target doesn't fit — carry it for the next Read().
			r.carry = pt
			r.carrySize = itemBytes
			return wrapTargets(batch), nil
		}

		batch = append(batch, pt)
		batchBytes += itemBytes
	}

	// Consume the closing ']'.
	if err := expectToken(r.dec, json.Delim(']')); err != nil {
		return nil, fmt.Errorf("expected closing ']': %w", err)
	}
	r.targetsExhausted = true

	if len(batch) > 0 {
		return wrapTargets(batch), nil
	}

	// No targets at all — fall through to metadata.
	if !r.metadataDone {
		r.metadataDone = true
		return r.readMetadata()
	}
	return nil, io.EOF
}

// readMetadata reads the metadata object from the JSON blob.
func (r *jsonGraphReader) readMetadata() (*pb.GetTargetGraphResponse, error) {
	fieldKey, err := readStringToken(r.dec)
	if err != nil {
		return nil, io.EOF
	}
	if fieldKey != "metadata" {
		return nil, fmt.Errorf("expected 'metadata' key, got %q", fieldKey)
	}

	var meta entity.Metadata
	if err := r.dec.Decode(&meta); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}

	return &pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Metadata{
			Metadata: entityMetadataToProto(&meta),
		},
	}, nil
}

func (r *jsonGraphReader) Close() error {
	if r.rc != nil {
		return r.rc.Close()
	}
	return nil
}

// --- entity → proto converters (local to storage, not exported) ---

func entityTargetToProto(et *entity.OptimizedTarget) *pb.OptimizedTarget {
	pt := &pb.OptimizedTarget{
		Id:                 et.ID,
		Hash:               et.Hash,
		DirectDependencies: et.DirectDependencies,
		RuleType:           et.RuleType,
		Tags:               et.Tags,
		Root:               et.Root,
		External:           et.External,
	}
	if len(et.Attributes) > 0 {
		pt.Attributes = make(map[int32]int32, len(et.Attributes))
		for k, v := range et.Attributes {
			pt.Attributes[k] = v
		}
	}
	return pt
}

func entityMetadataToProto(em *entity.Metadata) *pb.Metadata {
	return &pb.Metadata{
		TargetIdMapping:             em.TargetIDMapping,
		RuleTypeMapping:             em.RuleTypeMapping,
		TagMapping:                  em.TagMapping,
		AttributeNameMapping:        em.AttributeNameMapping,
		AttributeStringValueMapping: em.AttributeStringValueMapping,
	}
}

// --- helpers ---

func wrapTargets(batch []*pb.OptimizedTarget) *pb.GetTargetGraphResponse {
	return &pb.GetTargetGraphResponse{
		Item: &pb.GetTargetGraphResponse_Targets{
			Targets: &pb.OptimizedTargets{Targets: batch},
		},
	}
}

func expectToken(dec *json.Decoder, expected json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != expected {
		return fmt.Errorf("expected %v, got %v", expected, tok)
	}
	return nil
}

func readStringToken(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected string token, got %T", tok)
	}
	return s, nil
}
