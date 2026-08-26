package repository

import (
	"context"

	"github.com/rootless-dev/aegis/internal/service"
	"gorm.io/gorm"
)

type store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) service.Store {
	return store{db: db}
}

func (s store) Realms() service.RealmRepository {
	return realmRepository{db: s.db}
}

// Nesting reuses the outer transaction rather than opening a savepoint, which
// is what DisableNestedTransaction in database.Open buys: the outermost InTx is
// the only rollback boundary. SkipDefaultTransaction there makes this the only
// place a transaction begins at all.
func (s store) InTx(ctx context.Context, fn func(service.Store) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(store{db: tx})
	})
}
