package entity

// ChangeType classifies how a target differs between two revisions.
type ChangeType int

const (
	ChangeTypeInvalid ChangeType = iota
	ChangeTypeNew
	ChangeTypeDeleted
	ChangeTypeChanged
)

// String returns the enum name, matching the proto enum string representation.
func (c ChangeType) String() string {
	switch c {
	case ChangeTypeNew:
		return "CHANGE_TYPE_NEW"
	case ChangeTypeDeleted:
		return "CHANGE_TYPE_DELETED"
	case ChangeTypeChanged:
		return "CHANGE_TYPE_CHANGED"
	default:
		return "CHANGE_TYPE_INVALID"
	}
}
