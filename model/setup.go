package model

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type Setup struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	Version       string `json:"version" gorm:"type:varchar(50);not null"`
	InitializedAt int64  `json:"initialized_at" gorm:"type:bigint;not null"`
}

func GetSetup() *Setup {
	setup, err := GetSetupWithError()
	if err != nil {
		return nil
	}
	return setup
}

// GetSetupWithError distinguishes an empty setup table from a database
// outage.  The legacy pointer-only helper remains for callers that explicitly
// treat both cases as "not found"; bootstrap/security paths should use this
// variant so they do not make decisions on a failed read.
func GetSetupWithError() (*Setup, error) {
	if DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	var setup Setup
	err := DB.Order("id").First(&setup).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &setup, nil
}

// EnsureSetupMarkerForExistingRoot repairs a legacy database that already has
// a root user but no Setup row.  It only creates the marker; it never changes
// options or credentials.  The setup row's fixed primary key is the durable
// cross-process fence, while the root lookup is locked when supported by the
// active SQL dialect.
func EnsureSetupMarkerForExistingRoot(version string) (bool, error) {
	if DB == nil {
		return false, fmt.Errorf("database is not initialized")
	}
	if version == "" {
		version = common.Version
	}
	var created bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		var existing Setup
		err := lockForUpdate(tx).First(&existing).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var root User
		err = lockForUpdate(tx).Where("role = ?", common.RoleRootUser).Order("id").First(&root).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.Create(&Setup{ID: 1, Version: version, InitializedAt: time.Now().Unix()}).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

// SetupInitParams contains the values needed for the one-time bootstrap.
// The password must already be hashed by the HTTP layer; keeping hashing out
// of the transaction avoids doing expensive work while holding a database
// writer lock.
type SetupInitParams struct {
	Username           string
	HashedPassword     string
	SelfUseModeEnabled bool
	DemoSiteEnabled    bool
	Version            string
}

// ErrSetupAlreadyInitialized is returned as a non-error result by
// InitializeSetup. It is kept private to the implementation so callers do not
// accidentally treat a concurrent second request as a database failure.
var errSetupAlreadyInitialized = errors.New("setup is already initialized")

// setupInitMutex only reduces duplicate work inside one process. The durable
// singleton row (ID=1) and the transaction below are the cross-process fence.
var setupInitMutex sync.Mutex

// InitializeSetup atomically creates the first root user, persists bootstrap
// options, and inserts the setup marker. It returns (true, nil) only for the
// request that committed initialization; a concurrent/second caller returns
// (false, nil). All writes are in one transaction, so an error cannot leave a
// root user or a partially written option set behind.
//
// The fixed setup ID is intentional. A plain "check then insert" on an empty
// table is racy across processes; competing transactions may both observe no
// row. Inserting the same singleton key makes the database arbitrate the
// winner on SQLite, MySQL, and PostgreSQL.
func InitializeSetup(params SetupInitParams) (bool, error) {
	if DB == nil {
		return false, fmt.Errorf("database is not initialized")
	}
	if params.Version == "" {
		params.Version = common.Version
	}

	setupInitMutex.Lock()
	defer setupInitMutex.Unlock()

	var initialized bool
	err := DB.Transaction(func(tx *gorm.DB) error {
		var existing Setup
		err := lockForUpdate(tx).Order("id").First(&existing).Error
		switch {
		case err == nil:
			return errSetupAlreadyInitialized
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		// Lock an existing root row while deciding whether credentials are
		// needed. On an empty database the setup singleton insert below still
		// fences concurrent creators.
		var root User
		err = lockForUpdate(tx).Where("role = ?", common.RoleRootUser).Order("id").First(&root).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if params.Username == "" || params.HashedPassword == "" {
				return fmt.Errorf("root credentials are required")
			}
			root = User{
				Username:    params.Username,
				Password:    params.HashedPassword,
				Role:        common.RoleRootUser,
				Status:      common.UserStatusEnabled,
				DisplayName: "Root User",
				AccessToken: nil,
				Quota:       100000000,
			}
			if err := tx.Create(&root).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		optionValues := map[string]string{
			"SelfUseModeEnabled": boolToOptionValue(params.SelfUseModeEnabled),
			"DemoSiteEnabled":    boolToOptionValue(params.DemoSiteEnabled),
		}
		for key, value := range optionValues {
			value = normalizeOptionValue(key, value)
			if err := validateOptionValue(key, value); err != nil {
				return err
			}
			if err := upsertOption(tx, key, value); err != nil {
				return err
			}
		}

		// Use a deterministic singleton primary key rather than an auto-
		// incremented row. This is the cross-process compare-and-swap fence.
		if err := tx.Create(&Setup{
			ID:            1,
			Version:       params.Version,
			InitializedAt: time.Now().Unix(),
		}).Error; err != nil {
			return err
		}
		initialized = true
		return nil
	})
	if errors.Is(err, errSetupAlreadyInitialized) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// Publish runtime settings only after the transaction commits. The values
	// are validated before the writes above, so this should not fail; keeping
	// the error visible still prevents silently running with stale settings if a
	// future handler adds a fallible side effect.
	if initialized {
		optionValues := map[string]string{
			"SelfUseModeEnabled": boolToOptionValue(params.SelfUseModeEnabled),
			"DemoSiteEnabled":    boolToOptionValue(params.DemoSiteEnabled),
		}
		if err := publishSetupOptions(optionValues); err != nil {
			return true, err
		}
	}
	return initialized, nil
}

func boolToOptionValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// publishSetupOptions mirrors UpdateOptionsBulk's post-commit publication,
// but is kept local to the bootstrap transaction so the HTTP layer cannot
// accidentally persist the two options in separate transactions.
func publishSetupOptions(values map[string]string) error {
	optionUpdateMutex.Lock()
	defer optionUpdateMutex.Unlock()
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	for key, value := range values {
		if err := updateOptionMapLocked(key, value); err != nil {
			return err
		}
	}
	return nil
}
