package proto

import (
	"errors"
	"fmt"

	"github.com/uber/tango/entity"
	"github.com/uber/tango/internal/mapper"
	"github.com/uber/tango/tangopb"
)

// ProtoToGetChangedTargetsRequest converts a proto GetChangedTargetsRequest to
// the domain type. It returns an error if either revision is invalid or the
// remotes differ. OutputConfig is output-only and is not mapped.
func ProtoToGetChangedTargetsRequest(req *tangopb.GetChangedTargetsRequest) (entity.GetChangedTargetsRequest, error) {
	if req == nil {
		return entity.GetChangedTargetsRequest{}, errors.New("get changed targets request is required")
	}
	first, err := mapper.ProtoToBuildDescription(req.GetFirstRevision())
	if err != nil {
		return entity.GetChangedTargetsRequest{}, fmt.Errorf("first revision: %w", err)
	}
	second, err := mapper.ProtoToBuildDescription(req.GetSecondRevision())
	if err != nil {
		return entity.GetChangedTargetsRequest{}, fmt.Errorf("second revision: %w", err)
	}
	if first.Remote != second.Remote {
		return entity.GetChangedTargetsRequest{}, errors.New("first and second revision must have the same remote")
	}
	return entity.GetChangedTargetsRequest{
		First:             first,
		Second:            second,
		ExcludeFilesRegex: req.GetRequestOptions().GetExtraExcludeFilesRegex(),
		BypassCache:       req.GetBypassCache(),
	}, nil
}
