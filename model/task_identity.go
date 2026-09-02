package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const taskUpstreamIdentityIndexName = "idx_tasks_upstream_identity"

// ErrTaskUpstreamIdentityImmutable is returned when an existing task is
// reused with a different provider identity. Provider IDs are the correlation
// key for polling and billing; changing one after insertion could make a
// response for task A settle task B.
var ErrTaskUpstreamIdentityImmutable = errors.New("task upstream identity is immutable")

// TaskUpstreamIdentityDigest returns the bounded value stored in the task
// identity index. The raw provider ID remains in PrivateData for provider
// requests, while the digest avoids dialect-specific index length limits and
// keeps secrets/opaque IDs out of index inspection tools.
func TaskUpstreamIdentityDigest(upstreamTaskID string) (string, bool) {
	canonical := strings.TrimSpace(upstreamTaskID)
	if canonical == "" {
		return "", false
	}
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:]), true
}

func (t *Task) syncUpstreamTaskIdentity() {
	if t == nil {
		return
	}
	// Explicit provider IDs take precedence. For legacy rows the public TaskID
	// was itself the provider ID, so use it as a bounded fallback when creating
	// a new row. A genuinely empty task remains NULL and is handled by the
	// existing null-task reconciliation path.
	source := strings.TrimSpace(t.PrivateData.UpstreamTaskID)
	if source == "" {
		source = strings.TrimSpace(t.TaskID)
	}
	digest, ok := TaskUpstreamIdentityDigest(source)
	if !ok {
		return
	}
	if t.UpstreamTaskIdentity == nil || strings.TrimSpace(*t.UpstreamTaskIdentity) == "" {
		t.UpstreamTaskIdentity = &digest
	}
}

// taskIdentityProjection is the small immutable slice needed by a status CAS.
// Keeping this separate from Task avoids loading (or accidentally writing)
// billing and provider metadata that a caller selected only partially.
type taskIdentityProjection struct {
	UpstreamTaskIdentity *string         `gorm:"column:upstream_task_identity"`
	PrivateData          TaskPrivateData `gorm:"column:private_data"`
	TaskID               string          `gorm:"column:task_id"`
}

// taskIdentityCAS carries the identity predicate and the merged private data
// for UpdateWithStatus.  The predicate is evaluated again by the UPDATE, so a
// provider identity changed between the projection read and the write causes
// a lost CAS rather than an overwrite.
type taskIdentityCAS struct {
	expectedDigest string
	// expectedNull distinguishes a legacy row whose denormalized identity
	// column is still NULL/empty from a row with a concrete digest. The
	// logical identity may be derivable from private_data/task_id, but the CAS
	// predicate must match the physical value observed by the projection.
	expectedNull bool
	setDigest    string
	privateData  TaskPrivateData
}

// prepareTaskIdentityCAS validates the in-memory provider identity against the
// durable row before a map-based UPDATE (which intentionally bypasses GORM
// hooks).  It also merges zero-valued fields from a partial caller object with
// the persisted PrivateData, preventing a status refresh from erasing the
// provider key, billing context, or result URL.
//
// The bool return is false for an identity mismatch.  Mismatches are ordinary
// CAS losses to callers: they must not be retried as a new task or trigger any
// billing side effect.
func prepareTaskIdentityCAS(t *Task) (taskIdentityCAS, bool, error) {
	if t == nil || t.ID <= 0 {
		return taskIdentityCAS{}, false, errors.New("task id is required for identity CAS")
	}
	if DB == nil {
		return taskIdentityCAS{}, false, errors.New("database is not initialized")
	}
	var persisted taskIdentityProjection
	if err := DB.Session(&gorm.Session{SkipHooks: true, NewDB: true}).
		Model(&Task{}).
		Select("upstream_task_identity, private_data, task_id").
		Where("id = ?", t.ID).First(&persisted).Error; err != nil {
		return taskIdentityCAS{}, false, err
	}

	// Keep the physical column value separate from the logical digest derived
	// from legacy JSON. The former is used for the atomic WHERE predicate;
	// conflating them makes a first update on a NULL legacy row lose every CAS.
	persistedDigest := ""
	if persisted.UpstreamTaskIdentity != nil {
		persistedDigest = strings.TrimSpace(*persisted.UpstreamTaskIdentity)
	}
	persistedExplicit := strings.TrimSpace(persisted.PrivateData.UpstreamTaskID)
	persistedFallback := ""
	if persistedExplicit == "" {
		persistedFallback = strings.TrimSpace(persisted.TaskID)
	}
	persistedSource := persistedExplicit
	if persistedSource == "" {
		persistedSource = persistedFallback
	}
	derivedPersistedDigest, persistedHasIdentity := TaskUpstreamIdentityDigest(persistedSource)
	// A non-empty denormalized digest must agree with the immutable provider
	// identity stored in the legacy payload. If it does not, the row is already
	// ambiguous/corrupt; fail closed instead of allowing a partial status
	// object to preserve or further mutate the mismatched association.
	if persistedDigest != "" && persistedHasIdentity && persistedDigest != derivedPersistedDigest {
		return taskIdentityCAS{}, false, nil
	}

	loadedDigest := ""
	if t.UpstreamTaskIdentity != nil {
		loadedDigest = strings.TrimSpace(*t.UpstreamTaskIdentity)
	}
	candidateExplicit := strings.TrimSpace(t.PrivateData.UpstreamTaskID)
	// A full Task load has the digest populated even when PrivateData contains
	// no explicit ID (legacy fallback rows). If PrivateData was omitted from a
	// partial SELECT, use the persisted explicit ID only when that loaded digest
	// already agrees with the durable row; otherwise fail closed below.
	candidateSource := candidateExplicit
	if candidateSource == "" && loadedDigest != "" &&
		(persistedDigest == "" || loadedDigest == persistedDigest ||
			(persistedHasIdentity && loadedDigest == derivedPersistedDigest)) {
		// A caller may have selected only mutable fields. If its immutable digest
		// agrees with the durable row, recover the provider identity from the
		// projection instead of falling back to the public TaskID.
		candidateSource = persistedSource
	}
	if candidateSource == "" && persistedExplicit != "" {
		// Partial projections commonly omit PrivateData and the digest while
		// retaining the local/public TaskID. The persisted explicit provider ID
		// is authoritative in that case; using TaskID would reject a valid
		// metadata refresh (or, worse, make callers try a second task).
		candidateSource = persistedExplicit
	}
	if candidateSource == "" {
		candidateSource = strings.TrimSpace(t.TaskID)
	}
	if candidateSource == "" && loadedDigest == "" {
		// An entirely partial object may omit both TaskID and PrivateData. Use
		// the persisted logical source; the physical digest/NULL predicate below
		// still fences a concurrent identity publication.
		candidateSource = persistedSource
	}
	candidateDigest, candidateHasIdentity := TaskUpstreamIdentityDigest(candidateSource)

	if loadedDigest != "" && (!candidateHasIdentity || loadedDigest != candidateDigest) {
		return taskIdentityCAS{}, false, nil
	}
	// The in-memory projection is also a version of the immutable identity.
	// If another writer replaced the persisted digest after this task was
	// loaded, reject the update even when private_data still carries the same
	// provider ID. Without this check the subsequent UPDATE would simply use
	// the newly observed digest and overwrite a task that has been rebound.
	if loadedDigest != "" && persistedDigest != "" && loadedDigest != persistedDigest {
		return taskIdentityCAS{}, false, nil
	}
	if loadedDigest != "" && persistedHasIdentity && loadedDigest != derivedPersistedDigest {
		return taskIdentityCAS{}, false, nil
	}
	if persistedHasIdentity && (!candidateHasIdentity || derivedPersistedDigest != candidateDigest) {
		return taskIdentityCAS{}, false, nil
	}

	merged := mergeTaskPrivateData(persisted.PrivateData, t.PrivateData)
	if candidateExplicit != "" {
		merged.UpstreamTaskID = candidateExplicit
	} else if strings.TrimSpace(merged.UpstreamTaskID) == "" && persistedExplicit != "" {
		merged.UpstreamTaskID = persistedExplicit
	}
	cas := taskIdentityCAS{
		expectedDigest: persistedDigest,
		expectedNull:   persistedDigest == "",
		privateData:    merged,
	}
	if persistedDigest == "" && candidateHasIdentity {
		cas.setDigest = candidateDigest
	}
	return cas, true, nil
}

// mergeTaskPrivateData treats zero values as omitted fields, which is how the
// JSON representation marks optional task metadata. This preserves fields that
// were not loaded by a caller while allowing polling to update ResultURL and
// other non-zero values normally.
func mergeTaskPrivateData(persisted, candidate TaskPrivateData) TaskPrivateData {
	merged := persisted
	if candidate.Key != "" {
		merged.Key = candidate.Key
	}
	if candidate.UpstreamTaskID != "" {
		merged.UpstreamTaskID = strings.TrimSpace(candidate.UpstreamTaskID)
	}
	if candidate.ResultURL != "" {
		merged.ResultURL = candidate.ResultURL
	}
	if candidate.BillingSource != "" {
		merged.BillingSource = candidate.BillingSource
	}
	if candidate.SubscriptionId != 0 {
		merged.SubscriptionId = candidate.SubscriptionId
	}
	if candidate.TokenId != 0 {
		merged.TokenId = candidate.TokenId
	}
	if candidate.NodeName != "" {
		merged.NodeName = candidate.NodeName
	}
	if candidate.BillingContext != nil {
		merged.BillingContext = candidate.BillingContext
	}
	return merged
}

// BeforeSave populates the denormalized identity on inserts and protects it
// on full-struct updates. Map-based CAS updates intentionally do not carry the
// field and therefore leave the persisted identity untouched.
func (t *Task) BeforeSave(tx *gorm.DB) error {
	if t == nil {
		return nil
	}
	if tx != nil && tx.Statement != nil {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
			if err := normalizeTaskPrivateDataUpdateMap(values); err != nil {
				return err
			}
			for _, alias := range []string{"fail_reason", "FailReason"} {
				if raw, present := values[alias]; present {
					value, ok := raw.(string)
					if !ok {
						return errors.New("task fail_reason must be a string")
					}
					delete(values, alias)
					values["fail_reason"] = common.SanitizeProviderDiagnosticForStorage(value, common.MaxHTTPURLLength)
				}
			}
			return nil
		}
		if err := rejectUnsupportedProtectedStructDestination[Task](tx, "private_data"); err != nil {
			return err
		}
	}
	t.FailReason = common.SanitizeProviderDiagnosticForStorage(t.FailReason, common.MaxHTTPURLLength)
	// Inserts have no persisted identity to compare against.
	if t.ID == 0 {
		source := strings.TrimSpace(t.PrivateData.UpstreamTaskID)
		if source == "" {
			source = strings.TrimSpace(t.TaskID)
		}
		expected, hasIdentity := TaskUpstreamIdentityDigest(source)
		if t.UpstreamTaskIdentity != nil && strings.TrimSpace(*t.UpstreamTaskIdentity) != "" {
			// Never trust a caller-supplied denormalized digest on INSERT. A
			// mismatched value would poison the uniqueness fence and make the
			// row impossible to reconcile safely later.
			if !hasIdentity || strings.TrimSpace(*t.UpstreamTaskIdentity) != expected {
				return ErrTaskUpstreamIdentityImmutable
			}
		} else if hasIdentity {
			t.UpstreamTaskIdentity = &expected
		}
		return nil
	}

	// Full-struct Save calls can arrive with a deliberately cleared
	// UpstreamTaskIdentity (or with PrivateData omitted from a partial SELECT).
	// Reload the narrow immutable identity projection with hooks disabled, then
	// preserve/validate it before GORM builds the UPDATE.
	if tx != nil {
		var persisted Task
		lookupErr := tx.Session(&gorm.Session{SkipHooks: true}).
			Select("upstream_task_identity, private_data, task_id").
			Where("id = ?", t.ID).First(&persisted).Error
		if lookupErr == nil {
			persistedDigest := ""
			if persisted.UpstreamTaskIdentity != nil {
				persistedDigest = strings.TrimSpace(*persisted.UpstreamTaskIdentity)
			}
			persistedExplicit := strings.TrimSpace(persisted.PrivateData.UpstreamTaskID)
			persistedFallback := ""
			if persistedExplicit == "" {
				persistedFallback = strings.TrimSpace(persisted.TaskID)
			}

			candidateExplicit := strings.TrimSpace(t.PrivateData.UpstreamTaskID)
			if candidateExplicit != "" {
				expected, ok := TaskUpstreamIdentityDigest(candidateExplicit)
				if !ok {
					return ErrTaskUpstreamIdentityImmutable
				}
				if persistedDigest != "" && expected != persistedDigest {
					return ErrTaskUpstreamIdentityImmutable
				}
				if persistedExplicit != "" && candidateExplicit != persistedExplicit {
					return ErrTaskUpstreamIdentityImmutable
				}
				if persistedFallback != "" && candidateExplicit != persistedFallback {
					// A legacy row used TaskID as its provider identity. Adding a
					// different explicit ID is a rebinding, not a harmless metadata
					// backfill, and must be rejected.
					return ErrTaskUpstreamIdentityImmutable
				}
			}
			// For historical rows whose provider identity was the public TaskID,
			// changing that field is also an identity mutation. If a row had an
			// explicit provider ID, an omitted PrivateData projection is treated as
			// just that—an omission—and the existing digest remains authoritative.
			if persistedFallback != "" && candidateExplicit == "" {
				candidateFallback := strings.TrimSpace(t.TaskID)
				if candidateFallback != "" && candidateFallback != persistedFallback {
					return ErrTaskUpstreamIdentityImmutable
				}
			}

			if persistedDigest != "" {
				if t.UpstreamTaskIdentity == nil || strings.TrimSpace(*t.UpstreamTaskIdentity) == "" {
					digest := persistedDigest
					t.UpstreamTaskIdentity = &digest
				} else if strings.TrimSpace(*t.UpstreamTaskIdentity) != persistedDigest {
					return ErrTaskUpstreamIdentityImmutable
				}
				return nil
			}
			// A legacy row may not have been backfilled yet. Preserve the digest
			// derived from its old identity before considering the candidate.
			if persistedExplicit != "" || persistedFallback != "" {
				source := persistedExplicit
				if source == "" {
					source = persistedFallback
				}
				if digest, ok := TaskUpstreamIdentityDigest(source); ok {
					t.UpstreamTaskIdentity = &digest
					return nil
				}
			}
		}
		// A not-found row can be an INSERT with a manually supplied primary key;
		// fall through to the normal candidate sync in that case. Other lookup
		// errors must not be converted into a new identity.
		if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
	}

	if t.UpstreamTaskIdentity == nil || strings.TrimSpace(*t.UpstreamTaskIdentity) == "" {
		t.syncUpstreamTaskIdentity()
	}
	if explicit := strings.TrimSpace(t.PrivateData.UpstreamTaskID); explicit != "" && t.UpstreamTaskIdentity != nil {
		if expected, ok := TaskUpstreamIdentityDigest(explicit); !ok || expected != strings.TrimSpace(*t.UpstreamTaskIdentity) {
			return ErrTaskUpstreamIdentityImmutable
		}
	}
	return nil
}
