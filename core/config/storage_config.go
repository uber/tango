package config

// StorageType represents the type of storage backend.
type StorageType string

const (
	StorageTypeMemory StorageType = "memory"
	StorageTypeDisk   StorageType = "disk"
	// Future storage types can be added here:
	// StorageTypeS3     StorageType = "s3"
	// StorageTypeGCS    StorageType = "gcs"
)

// StorageConfig holds configuration for storage backends.
// Similar to Athens' storage configuration pattern.
type StorageConfig struct {
	// Type specifies which storage backend to use.
	// Supported values: "memory", "disk"
	// Defaults to "memory" if not specified.
	Type StorageType `yaml:"type"`

	// Disk holds configuration for disk-based storage.
	Disk *DiskStorageConfig `yaml:"disk,omitempty"`
}

// DiskStorageConfig holds configuration for disk-based storage.
type DiskStorageConfig struct {
	// RootPath is the directory where blobs will be stored.
	RootPath string `yaml:"root_path"`
}
