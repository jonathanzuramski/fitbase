package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fitbase/fitbase/internal/crypto"
	"github.com/fitbase/fitbase/internal/models"
)

// AISettings holds the configured AI provider, model, and decrypted API key.
type AISettings struct {
	Provider string
	Model    string
	APIKey   string
}

// GetAISettings returns the stored AI provider, model, and decrypted API key.
// Returns empty AISettings (no error) if nothing has been saved yet.
func (db *DB) GetAISettings() (AISettings, error) {
	var provider, model, encKey string
	err := db.QueryRow(`SELECT provider, model, api_key FROM ai_settings WHERE id = 1`).Scan(&provider, &model, &encKey)
	if err == sql.ErrNoRows {
		return AISettings{}, nil
	}
	if err != nil {
		return AISettings{}, err
	}
	if provider == "" {
		return AISettings{}, nil
	}
	if encKey == "" {
		return AISettings{Provider: provider, Model: model}, nil
	}
	keyBytes, err := crypto.Decrypt(db.key, encKey)
	if err != nil {
		return AISettings{}, fmt.Errorf("decrypt ai api_key: %w", err)
	}
	return AISettings{Provider: provider, Model: model, APIKey: string(keyBytes)}, nil
}

// SaveAISettings encrypts and stores the AI provider, model, and API key.
func (db *DB) SaveAISettings(s AISettings) error {
	encKey, err := crypto.Encrypt(db.key, []byte(s.APIKey))
	if err != nil {
		return fmt.Errorf("encrypt ai api_key: %w", err)
	}
	_, err = db.Exec(`
		INSERT INTO ai_settings (id, provider, model, api_key) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider   = excluded.provider,
			model      = excluded.model,
			api_key    = excluded.api_key,
			updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')`,
		s.Provider, s.Model, encKey)
	return err
}

// GetRecentZoneTotals sums time in each power zone (7 zones), HR zone (5 zones),
// and the Sweet Spot reference band for all workouts recorded in the last N
// days. SS is a parallel counter — it overlaps Z3/Z4 and is not part of the
// 7-zone partition.
func (db *DB) GetRecentZoneTotals(days int) ([7]int, [5]int, int, error) {
	rows, err := db.Query(`
		SELECT wzt.power_secs, wzt.hr_secs, wzt.ss_secs
		FROM workout_zone_times wzt
		JOIN workouts w ON w.id = wzt.workout_id
		WHERE w.recorded_at >= date('now', ?)`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return [7]int{}, [5]int{}, 0, err
	}
	defer rows.Close() //nolint:errcheck
	var power [7]int
	var hr [5]int
	var ss int
	for rows.Next() {
		var ps, hs string
		var ssRow sql.NullInt64
		if err := rows.Scan(&ps, &hs, &ssRow); err != nil {
			return [7]int{}, [5]int{}, 0, err
		}
		var p [7]int
		var h [5]int
		_ = json.Unmarshal([]byte(ps), &p)
		_ = json.Unmarshal([]byte(hs), &h)
		for i := range p {
			power[i] += p[i]
		}
		for i := range h {
			hr[i] += h[i]
		}
		if ssRow.Valid {
			ss += int(ssRow.Int64)
		}
	}
	return power, hr, ss, rows.Err()
}

// ListWorkoutsSince returns workouts with recorded_at >= since, newest first.
// Used by the AI coach to bound context to a recent window without pulling
// the full workout history. sport ("" = all sports, matched case-insensitively)
// and limit (0 = no cap) are applied in SQL so the DB never hydrates rows the
// caller would discard.
func (db *DB) ListWorkoutsSince(since time.Time, sport string, limit int) ([]models.Workout, error) {
	q := `
		SELECT id, filename, recorded_at, sport, duration_secs, elapsed_secs, distance_meters,
		       elevation_gain_meters, avg_power_watts, max_power_watts, normalized_power,
		       avg_heart_rate, max_heart_rate, avg_cadence, avg_speed_mps,
		       tss, intensity_factor, is_indoor, route_id, created_at
		FROM workouts
		WHERE recorded_at >= ?`
	args := []any{since.UTC().Format(time.RFC3339)}
	if sport != "" {
		q += ` AND LOWER(sport) = LOWER(?)`
		args = append(args, sport)
	}
	q += ` ORDER BY recorded_at DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var out []models.Workout
	for rows.Next() {
		w, err := scanWorkout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CachedInsights is the last AI coaching response persisted for instant display.
// Content is the full Markdown blob streamed back from the provider.
type CachedInsights struct {
	Provider    string
	Model       string
	Content     string
	GeneratedAt time.Time
}

// GetCachedInsights returns the most recently generated coaching response, or
// (nil, nil) if none has been generated yet.
func (db *DB) GetCachedInsights() (*CachedInsights, error) {
	var c CachedInsights
	var generatedAt string
	err := db.QueryRow(`
		SELECT provider, model, content, generated_at
		FROM ai_insights_cache_v2 WHERE id = 1`).
		Scan(&c.Provider, &c.Model, &c.Content, &generatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.Content == "" {
		return nil, nil
	}
	if t, perr := time.Parse(time.RFC3339, generatedAt); perr == nil {
		c.GeneratedAt = t
	}
	return &c, nil
}

// SaveCachedInsights overwrites the single cache row with the latest response.
func (db *DB) SaveCachedInsights(c CachedInsights) error {
	_, err := db.Exec(`
		INSERT INTO ai_insights_cache_v2 (id, provider, model, content, generated_at)
		VALUES (1, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
		ON CONFLICT(id) DO UPDATE SET
			provider     = excluded.provider,
			model        = excluded.model,
			content      = excluded.content,
			generated_at = excluded.generated_at`,
		c.Provider, c.Model, c.Content)
	return err
}

// ClearCachedInsights drops any cached response. Call after settings change so
// we don't show an insight generated by a provider the user no longer uses.
func (db *DB) ClearCachedInsights() error {
	_, err := db.Exec(`DELETE FROM ai_insights_cache_v2 WHERE id = 1`)
	return err
}
