package db

import (
	"context"
	"database/sql"

	"shelley.exe.dev/db/generated"
)

func (db *DB) GetModelProviders(ctx context.Context) ([]generated.ModelProvider, error) {
	var providers []generated.ModelProvider
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		providers, err = generated.New(rx.Conn()).GetModelProviders(ctx)
		return err
	})
	return providers, err
}

func (db *DB) GetModelProvider(ctx context.Context, providerID string) (*generated.ModelProvider, error) {
	var provider generated.ModelProvider
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		provider, err = generated.New(rx.Conn()).GetModelProvider(ctx, providerID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &provider, nil
}

func (db *DB) CreateModelProvider(ctx context.Context, params generated.CreateModelProviderParams) (*generated.ModelProvider, error) {
	var provider generated.ModelProvider
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		provider, err = generated.New(tx.Conn()).CreateModelProvider(ctx, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &provider, nil
}

func (db *DB) UpdateModelProvider(ctx context.Context, params generated.UpdateModelProviderParams) (*generated.ModelProvider, error) {
	var provider generated.ModelProvider
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		provider, err = generated.New(tx.Conn()).UpdateModelProvider(ctx, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &provider, nil
}

func (db *DB) DeleteModelProvider(ctx context.Context, providerID string) error {
	return db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		rows, err := generated.New(tx.Conn()).DeleteModelProvider(ctx, providerID)
		if err != nil {
			return err
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}
