package mapper

// ProtoToGetTargetGraphRequest converts a proto GetTargetGraphRequest to the entity type.
func ProtoToGetTargetGraphRequest(req *GetTargetGraphRequest) entity.GetTargetGraphRequest {
	if req == nil {
		return entity.GetTargetGraphRequest{}
	}
	return entity.GetTargetGraphRequest{
		Build:             ToBuildDescription(req.Build),
		ExcludeFilesRegex: req.ExcludeFilesRegex,
		BypassCache:       req.BypassCache,
	}
}
