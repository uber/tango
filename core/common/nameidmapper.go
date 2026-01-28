package common

// NameIDMapper assigns stable int32 IDs to string names on demand.
// IDs are assigned sequentially starting from 0.
type NameIDMapper struct {
	nameToID map[string]int32
	nextID   int32
}

// NewNameIdMapper creates a new NameIdMapper.
func NewNameIDMapper() *NameIDMapper {
	return &NameIDMapper{
		nameToID: make(map[string]int32),
		nextID:   0,
	}
}

// ID returns the existing ID for the provided name or assigns a new one.
func (a *NameIDMapper) ID(name string) int32 {
	if id, ok := a.nameToID[name]; ok {
		return id
	}
	id := a.nextID
	a.nextID++
	a.nameToID[name] = id
	return id
}

// Mapping returns the underlying name->id map.
func (a *NameIDMapper) Mapping() map[string]int32 {
	return a.nameToID
}

// Invert returns an id->name map built from the current name->id map.
func (a *NameIDMapper) Invert() map[int32]string {
	out := make(map[int32]string, len(a.nameToID))
	for name, id := range a.nameToID {
		out[id] = name
	}
	return out
}
