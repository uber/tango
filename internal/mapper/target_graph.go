package mapper

import (
	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

// ToGetTargetGraphRequest converts a proto GetTargetGraphRequest to the domain type.
// OutputConfig is deliberately dropped here: it is a transport/presentation
// concern that must not reach the orchestrator, where it could poison the
// shared cache with stripped graphs.
func ToGetTargetGraphRequest(req *tangopb.GetTargetGraphRequest) entity.GetTargetGraphRequest {
	if req == nil {
		return entity.GetTargetGraphRequest{}
	}
	return entity.GetTargetGraphRequest{
		Build:             ToBuildDescription(req.GetBuildDescription()),
		ExcludeFilesRegex: req.GetRequestOptions().GetExtraExcludeFilesRegex(),
		BypassCache:       req.GetBypassCache(),
	}
}
