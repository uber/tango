package mapper

import (
	"github.com/uber/tango/entity"
	"github.com/uber/tango/tangopb"
)

// ToRequestOptions converts a proto RequestOptions pointer to the domain type.
// A nil proto value produces a zero-value RequestOptions (no options).
func ToRequestOptions(opts *tangopb.RequestOptions) entity.RequestOptions {
	if opts == nil {
		return entity.RequestOptions{}
	}
	return entity.RequestOptions{
		ExtraExcludeFilesRegex: opts.GetExtraExcludeFilesRegex(),
	}
}
