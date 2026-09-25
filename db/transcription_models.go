package db

import (
	"context"
	"fmt"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/transcription"
)

const (
	transcriptionDefaultTranscriptSettingKey = "transcription.default.transcript"
	transcriptionDefaultTimecodedSettingKey  = "transcription.default.timecoded"
)

type TranscriptionModelDefault struct {
	Role    string
	ModelID string
}

func transcriptionDefaultSettingKey(role transcription.Role) string {
	return "transcription.default." + string(role)
}

// ListTranscriptionModels returns durable transcription model records. Chat
// models are intentionally stored and queried separately.
func (db *DB) ListTranscriptionModels(ctx context.Context) ([]generated.TranscriptionModel, error) {
	var result []generated.TranscriptionModel
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		result, err = generated.New(rx.Conn()).ListTranscriptionModels(ctx)
		return err
	})
	return result, err
}

func (db *DB) GetTranscriptionModel(ctx context.Context, modelID string) (*generated.TranscriptionModel, error) {
	var result generated.TranscriptionModel
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		result, err = generated.New(rx.Conn()).GetTranscriptionModel(ctx, modelID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (db *DB) CreateTranscriptionModel(ctx context.Context, params generated.CreateTranscriptionModelParams) (*generated.TranscriptionModel, error) {
	profile, err := transcription.NormalizeAPIProfile(transcription.APIProfile(params.ApiProfile), transcription.Protocol(params.Protocol))
	if err != nil {
		return nil, err
	}
	params.ApiProfile = string(profile)
	encoding, err := transcription.NormalizeRequestEncoding(transcription.RequestEncoding(params.RequestEncoding), transcription.Protocol(params.Protocol))
	if err != nil {
		return nil, err
	}
	params.RequestEncoding = string(encoding)
	var result generated.TranscriptionModel
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		result, err = generated.New(tx.Conn()).CreateTranscriptionModel(ctx, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (db *DB) UpdateTranscriptionModel(ctx context.Context, params generated.UpdateTranscriptionModelParams) (*generated.TranscriptionModel, error) {
	profile, err := transcription.NormalizeAPIProfile(transcription.APIProfile(params.ApiProfile), transcription.Protocol(params.Protocol))
	if err != nil {
		return nil, err
	}
	params.ApiProfile = string(profile)
	encoding, err := transcription.NormalizeRequestEncoding(transcription.RequestEncoding(params.RequestEncoding), transcription.Protocol(params.Protocol))
	if err != nil {
		return nil, err
	}
	params.RequestEncoding = string(encoding)
	var result generated.TranscriptionModel
	err = db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		result, err = generated.New(tx.Conn()).UpdateTranscriptionModel(ctx, params)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (db *DB) DeleteTranscriptionModel(ctx context.Context, modelID string) (bool, error) {
	var deleted int64
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		deleted, err = generated.New(tx.Conn()).DeleteTranscriptionModel(ctx, modelID)
		return err
	})
	return deleted == 1, err
}

func (db *DB) ListTranscriptionModelDefaults(ctx context.Context) ([]TranscriptionModelDefault, error) {
	var rows []generated.ListTranscriptionModelDefaultsRow
	err := db.pool.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var err error
		rows, err = generated.New(rx.Conn()).ListTranscriptionModelDefaults(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	result := make([]TranscriptionModelDefault, 0, len(rows))
	for _, row := range rows {
		switch row.Key {
		case transcriptionDefaultTranscriptSettingKey:
			result = append(result, TranscriptionModelDefault{Role: string(transcription.RoleTranscript), ModelID: row.Value})
		case transcriptionDefaultTimecodedSettingKey:
			result = append(result, TranscriptionModelDefault{Role: string(transcription.RoleTimecoded), ModelID: row.Value})
		}
	}
	return result, nil
}

func (db *DB) InitializeTranscriptionModelDefault(ctx context.Context, role transcription.Role, modelID string) (bool, error) {
	if err := transcription.ValidateRole(role); err != nil {
		return false, err
	}
	if modelID == "" {
		return false, fmt.Errorf("transcription default model id is required")
	}
	var inserted int64
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		inserted, err = generated.New(tx.Conn()).InitializeTranscriptionModelDefault(ctx, generated.InitializeTranscriptionModelDefaultParams{
			Key: transcriptionDefaultSettingKey(role), Value: modelID,
		})
		return err
	})
	return inserted == 1, err
}

func (db *DB) SetTranscriptionModelDefault(ctx context.Context, role transcription.Role, modelID string) (*TranscriptionModelDefault, error) {
	if err := transcription.ValidateRole(role); err != nil {
		return nil, err
	}
	if modelID == "" {
		return nil, fmt.Errorf("transcription default model id is required")
	}
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		return generated.New(tx.Conn()).SetTranscriptionModelDefault(ctx, generated.SetTranscriptionModelDefaultParams{
			Key: transcriptionDefaultSettingKey(role), Value: modelID,
		})
	})
	if err != nil {
		return nil, err
	}
	return &TranscriptionModelDefault{Role: string(role), ModelID: modelID}, nil
}

func (db *DB) DeleteTranscriptionModelDefault(ctx context.Context, role transcription.Role) (bool, error) {
	if err := transcription.ValidateRole(role); err != nil {
		return false, err
	}
	var deleted int64
	err := db.pool.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		var err error
		deleted, err = generated.New(tx.Conn()).DeleteTranscriptionModelDefault(ctx, transcriptionDefaultSettingKey(role))
		return err
	})
	return deleted == 1, err
}
