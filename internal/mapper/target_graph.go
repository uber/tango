package mapper

import (
	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

// ProtoToGetTargetGraphRequest converts a proto GetTargetGraphRequest to the domain type.
func ProtoToGetTargetGraphRequest(req *tangopb.GetTargetGraphRequest) entity.GetTargetGraphRequest {
	if req == nil {
		return entity.GetTargetGraphRequest{}
	}
	return entity.GetTargetGraphRequest{
		Build:             ProtoToBuildDescription(req.GetBuildDescription()),
		ExcludeFilesRegex: req.GetRequestOptions().GetExtraExcludeFilesRegex(),
		BypassCache:       req.GetBypassCache(),
	}
}
