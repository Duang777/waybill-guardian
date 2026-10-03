package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

var ErrUniqueConflict = errors.New("unique PostgreSQL constraint conflict")

type UniqueConflictError struct {
	Constraint string
	Cause      error
}

func (e *UniqueConflictError) Error() string {
	return fmt.Sprintf("%s: %s", ErrUniqueConflict, e.Constraint)
}

func (e *UniqueConflictError) Unwrap() error {
	return errors.Join(ErrUniqueConflict, e.Cause)
}

func MapError(err error) error {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		return err
	}
	return &UniqueConflictError{
		Constraint: postgresError.ConstraintName,
		Cause:      err,
	}
}
