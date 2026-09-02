package model

import "gorm.io/gorm"

// boundedPrimaryKeyCount returns the number of rows matched by query, capped
// at limit.  A LIMIT applied directly to GORM's Count is not a bound: SQL
// evaluates the aggregate before LIMIT and can still scan every matching row.
// Reading only limit+1 primary keys keeps the work bounded while preserving
// the list APIs' historical "total" field (with an explicit cap).
//
// The helper is intentionally limited to model queries whose primary key is
// named id, which is true for all current callers.  The column name is not
// caller-controlled, avoiding an SQL identifier injection footgun.
func boundedPrimaryKeyCount(query *gorm.DB, limit int) (int64, error) {
	if query == nil || limit <= 0 {
		return 0, nil
	}

	probeLimit := limit
	maxInt := int(^uint(0) >> 1)
	if probeLimit < maxInt {
		probeLimit++
	}

	var ids []int
	// Session clones the statement while retaining all existing predicates.
	// Without the clone, GORM may leave Select("id") on the caller's query and
	// the subsequent paginated list would return zero-valued fields (including
	// a blank token key).
	probe := query.Session(&gorm.Session{})
	if err := probe.Select("id").Limit(probeLimit).Find(&ids).Error; err != nil {
		return 0, err
	}
	if len(ids) > limit {
		return int64(limit), nil
	}
	return int64(len(ids)), nil
}
