package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/yeomyeonggeori/bluememo"
)

type ProfileRepository struct {
	database *sql.DB
}

func NewProfileRepository(database *sql.DB) ProfileRepository {
	return ProfileRepository{database: database}
}

func (repository ProfileRepository) FindProfile(ctx context.Context, personID string) (bluememo.Profile, bool, error) {
	var profile bluememo.Profile
	var identityLinesDocument, currentLinesDocument string
	errorValue := repository.database.QueryRowContext(ctx, `
SELECT person_id, COALESCE(array_to_json(identity_lines)::text, '[]'), COALESCE(array_to_json(current_lines)::text, '[]'),
       built_from_fact_count, built_at
FROM memory_profile WHERE person_id = $1`, personID).Scan(
		&profile.PersonID, &identityLinesDocument, &currentLinesDocument, &profile.BuiltFromFactCount, &profile.BuiltAt,
	)
	if errors.Is(errorValue, sql.ErrNoRows) {
		return bluememo.Profile{}, false, nil
	}
	if errorValue != nil {
		return bluememo.Profile{}, false, errorValue
	}
	profile.IdentityLines = stringSliceFromDocument(identityLinesDocument)
	profile.CurrentLines = stringSliceFromDocument(currentLinesDocument)
	profile.BuiltAt = profile.BuiltAt.UTC()
	return profile, true, nil
}

func (repository ProfileRepository) SaveProfile(ctx context.Context, profile bluememo.Profile) error {
	_, errorValue := repository.database.ExecContext(ctx, `
INSERT INTO memory_profile (person_id, identity_lines, current_lines, built_from_fact_count, built_at)
VALUES ($1, $2::text[], $3::text[], $4, $5)
ON CONFLICT (person_id) DO UPDATE SET
  identity_lines = EXCLUDED.identity_lines,
  current_lines = EXCLUDED.current_lines,
  built_from_fact_count = EXCLUDED.built_from_fact_count,
  built_at = EXCLUDED.built_at`,
		profile.PersonID, nonNilStrings(profile.IdentityLines), nonNilStrings(profile.CurrentLines), profile.BuiltFromFactCount, profile.BuiltAt.UTC(),
	)
	return errorValue
}
