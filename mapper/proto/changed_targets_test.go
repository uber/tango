package proto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/uber/tango/tangopb"
)

func TestProtoToGetChangedTargetsRequest(t *testing.T) {
	tests := []struct {
		name    string
		request *tangopb.GetChangedTargetsRequest
		wantErr bool
	}{
		{
			name:    "nil request",
			request: nil,
			wantErr: true,
		},
		{
			name: "missing first revision",
			request: &tangopb.GetChangedTargetsRequest{
				SecondRevision: &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision",
			request: &tangopb.GetChangedTargetsRequest{
				FirstRevision: &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
			},
			wantErr: true,
		},
		{
			name: "missing first revision remote",
			request: &tangopb.GetChangedTargetsRequest{
				FirstRevision:  &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, BaseSha: "sha1"},
				SecondRevision: &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing first revision base_sha",
			request: &tangopb.GetChangedTargetsRequest{
				FirstRevision:  &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code"},
				SecondRevision: &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision remote",
			request: &tangopb.GetChangedTargetsRequest{
				FirstRevision:  &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing second revision base_sha",
			request: &tangopb.GetChangedTargetsRequest{
				FirstRevision:  &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code"},
			},
			wantErr: true,
		},
		{
			name: "different remotes",
			request: &tangopb.GetChangedTargetsRequest{
				FirstRevision:  &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:other", BaseSha: "sha2"},
			},
			wantErr: true,
		},
		{
			name: "missing output_config defaults to no filtering",
			request: &tangopb.GetChangedTargetsRequest{
				FirstRevision:  &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
			},
		},
		{
			name: "valid request",
			request: &tangopb.GetChangedTargetsRequest{
				FirstRevision:  &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha1"},
				SecondRevision: &tangopb.BuildDescription{Strategy: tangopb.COMPUTATION_STRATEGY_UNSET, Remote: "repo:go-code", BaseSha: "sha2"},
				OutputConfig:   &tangopb.OutputConfig{MaxDistance: -1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ProtoToGetChangedTargetsRequest(tt.request)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
