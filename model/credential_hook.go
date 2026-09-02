package model

import (
	"fmt"
	"reflect"

	"gorm.io/gorm"
)

func gormStatementUsesSeparateDestination(statement *gorm.Statement) bool {
	if statement == nil || statement.Model == nil || statement.Dest == nil {
		return false
	}
	modelValue := reflect.ValueOf(statement.Model)
	destinationValue := reflect.ValueOf(statement.Dest)
	if modelValue.Type() != destinationValue.Type() {
		return true
	}
	if modelValue.Kind() == reflect.Ptr && !modelValue.IsNil() && !destinationValue.IsNil() {
		return modelValue.Pointer() != destinationValue.Pointer()
	}
	return false
}

// prepareProtectedStructDestination normalizes the value GORM will use to
// build an Updates(struct) assignment. Separate pointer destinations are
// copied so an update hook does not replace the caller's runtime plaintext
// with its durable encrypted representation.
func prepareProtectedStructDestination[T any](
	tx *gorm.DB,
	prepare func(*T) error,
	protectedColumns ...string,
) (bool, error) {
	if tx == nil || tx.Statement == nil {
		return false, nil
	}
	switch destination := tx.Statement.Dest.(type) {
	case T:
		prepared := destination
		if err := prepare(&prepared); err != nil {
			return true, err
		}
		tx.Statement.Dest = prepared
		return true, nil
	case *T:
		if destination == nil {
			return true, nil
		}
		if gormStatementUsesSeparateDestination(tx.Statement) {
			prepared := *destination
			if err := prepare(&prepared); err != nil {
				return true, err
			}
			tx.Statement.Dest = &prepared
			return true, nil
		}
		return true, prepare(destination)
	}
	return false, rejectUnsupportedProtectedStructDestination[T](tx, protectedColumns...)
}

func rejectUnsupportedProtectedStructDestination[T any](tx *gorm.DB, protectedColumns ...string) error {
	if tx == nil || tx.Statement == nil {
		return nil
	}
	switch tx.Statement.Dest.(type) {
	case T, *T:
		return nil
	}
	destinationType := reflect.TypeOf(tx.Statement.Dest)
	for destinationType != nil && destinationType.Kind() == reflect.Ptr {
		destinationType = destinationType.Elem()
	}
	if destinationType == nil || destinationType.Kind() != reflect.Struct {
		return nil
	}
	destinationStatement := &gorm.Statement{DB: tx}
	if err := destinationStatement.Parse(tx.Statement.Dest); err != nil || destinationStatement.Schema == nil {
		return nil
	}
	for _, column := range protectedColumns {
		if _, exists := destinationStatement.Schema.FieldsByDBName[column]; exists {
			return fmt.Errorf("unsupported credential update destination %T contains protected column %s", tx.Statement.Dest, column)
		}
	}
	return nil
}
