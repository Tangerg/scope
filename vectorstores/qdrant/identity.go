package qdrant

import (
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

func parsePointID(id string) (*qdrant.PointId, error) {
	if number, err := strconv.ParseUint(id, 10, 64); err == nil && strconv.FormatUint(number, 10) == id {
		return qdrant.NewIDNum(number), nil
	}
	if !isCanonicalUUID(id) {
		return nil, fmt.Errorf("%w %q: must be a canonical uint64 or lowercase hyphenated UUID", ErrInvalidPointID, id)
	}
	return qdrant.NewIDUUID(id), nil
}

// UUID aliases address one native point, so accepting them would merge distinct
// caller-assigned IDs and change the ID returned by a subsequent search.
func isCanonicalUUID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

func formatPointID(id *qdrant.PointId) (string, error) {
	if id == nil {
		return "", fmt.Errorf("%w: query result has no point ID", ErrInvalidPointID)
	}
	switch value := id.GetPointIdOptions().(type) {
	case *qdrant.PointId_Num:
		return strconv.FormatUint(value.Num, 10), nil
	case *qdrant.PointId_Uuid:
		if !isCanonicalUUID(value.Uuid) {
			return "", fmt.Errorf("%w %q: query result UUID must be lowercase and hyphenated", ErrInvalidPointID, value.Uuid)
		}
		return value.Uuid, nil
	default:
		return "", fmt.Errorf("%w: query result uses an unsupported point ID", ErrInvalidPointID)
	}
}
