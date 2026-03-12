package resourcedb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Store provides resource tracking storage operations
type Store struct {
	db   *sql.DB
	path string
	mu   sync.Mutex
}

// NewStore creates a new resource store
// If dbPath is empty, uses the default path (~/.clanker/resources.db)
func NewStore(dbPath string) (*Store, error) {
	if dbPath == "" {
		dbPath = DefaultDBPath()
	}

	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{
		db:   db,
		path: dbPath,
	}, nil
}

// Close closes the database connection
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Path returns the database file path
func (s *Store) Path() string {
	return s.path
}

// RecordResource stores a created resource
func (s *Store) RecordResource(r *Resource) error {
	if r == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	metadataJSON, err := json.Marshal(r.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}

	tagsJSON, err := json.Marshal(r.Tags)
	if err != nil {
		tagsJSON = []byte("{}")
	}

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO resources
		(run_id, command_index, provider, service, operation, resource_type,
		 resource_id, resource_arn, resource_name, region, profile, account_id,
		 parent_run_id, metadata, tags, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		r.RunID, r.CommandIndex, r.Provider, r.Service, r.Operation, r.ResourceType,
		r.ResourceID, r.ResourceARN, r.ResourceName, r.Region, r.Profile, r.AccountID,
		r.ParentRunID, string(metadataJSON), string(tagsJSON), r.CreatedAt,
	)

	return err
}

// GetResourcesByRun returns all resources for a run in reverse command order (for cleanup)
func (s *Store) GetResourcesByRun(runID string) ([]*Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`
		SELECT id, run_id, command_index, provider, service, operation, resource_type,
		       resource_id, resource_arn, resource_name, region, profile, account_id,
		       parent_run_id, metadata, tags, created_at
		FROM resources
		WHERE run_id = ?
		ORDER BY command_index DESC
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanResources(rows)
}

// GetResourcesByType returns all resources of a specific type
func (s *Store) GetResourcesByType(resourceType string) ([]*Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`
		SELECT id, run_id, command_index, provider, service, operation, resource_type,
		       resource_id, resource_arn, resource_name, region, profile, account_id,
		       parent_run_id, metadata, tags, created_at
		FROM resources
		WHERE resource_type = ?
		ORDER BY created_at DESC
	`, resourceType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanResources(rows)
}

// GetResourceByID returns a resource by its AWS resource ID
func (s *Store) GetResourceByID(resourceID string) (*Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(`
		SELECT id, run_id, command_index, provider, service, operation, resource_type,
		       resource_id, resource_arn, resource_name, region, profile, account_id,
		       parent_run_id, metadata, tags, created_at
		FROM resources
		WHERE resource_id = ?
		LIMIT 1
	`, resourceID)

	return scanResource(row)
}

// DeleteResourceRecord removes a resource record (after cleanup)
func (s *Store) DeleteResourceRecord(runID string, cmdIndex int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM resources WHERE run_id = ? AND command_index = ?`, runID, cmdIndex)
	return err
}

// DeleteRunResources removes all resource records for a run
func (s *Store) DeleteRunResources(runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM resources WHERE run_id = ?`, runID)
	return err
}

// ListRuns returns recent run IDs
func (s *Store) ListRuns(limit int) ([]RunSummary, error) {
	if limit <= 0 {
		limit = 50
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`
		SELECT run_id, provider, region, profile, COUNT(*) as count, MIN(created_at) as created_at
		FROM resources
		GROUP BY run_id
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []RunSummary
	for rows.Next() {
		var r RunSummary
		var createdAt string
		if err := rows.Scan(&r.RunID, &r.Provider, &r.Region, &r.Profile, &r.ResourceCount, &createdAt); err != nil {
			continue
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		runs = append(runs, r)
	}

	return runs, rows.Err()
}

// CountResources returns total resource count
func (s *Store) CountResources() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM resources`).Scan(&count)
	return count, err
}

// scanResources scans multiple resource rows
func scanResources(rows *sql.Rows) ([]*Resource, error) {
	var resources []*Resource
	for rows.Next() {
		r, err := scanResourceRow(rows)
		if err != nil {
			continue
		}
		resources = append(resources, r)
	}
	return resources, rows.Err()
}

// scanResource scans a single resource row
func scanResource(row *sql.Row) (*Resource, error) {
	r := &Resource{}
	var metadataJSON, tagsJSON sql.NullString
	var createdAt string
	var parentRunID sql.NullString

	err := row.Scan(
		&r.ID, &r.RunID, &r.CommandIndex, &r.Provider, &r.Service, &r.Operation,
		&r.ResourceType, &r.ResourceID, &r.ResourceARN, &r.ResourceName,
		&r.Region, &r.Profile, &r.AccountID, &parentRunID, &metadataJSON, &tagsJSON, &createdAt,
	)
	if err != nil {
		return nil, err
	}

	if parentRunID.Valid {
		r.ParentRunID = parentRunID.String
	}

	if metadataJSON.Valid {
		_ = json.Unmarshal([]byte(metadataJSON.String), &r.Metadata)
	}
	if r.Metadata == nil {
		r.Metadata = make(map[string]string)
	}

	if tagsJSON.Valid {
		_ = json.Unmarshal([]byte(tagsJSON.String), &r.Tags)
	}
	if r.Tags == nil {
		r.Tags = make(map[string]string)
	}

	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	return r, nil
}

// scanResourceRow scans a resource from rows iterator
func scanResourceRow(rows *sql.Rows) (*Resource, error) {
	r := &Resource{}
	var metadataJSON, tagsJSON sql.NullString
	var createdAt string
	var parentRunID sql.NullString

	err := rows.Scan(
		&r.ID, &r.RunID, &r.CommandIndex, &r.Provider, &r.Service, &r.Operation,
		&r.ResourceType, &r.ResourceID, &r.ResourceARN, &r.ResourceName,
		&r.Region, &r.Profile, &r.AccountID, &parentRunID, &metadataJSON, &tagsJSON, &createdAt,
	)
	if err != nil {
		return nil, err
	}

	if parentRunID.Valid {
		r.ParentRunID = parentRunID.String
	}

	if metadataJSON.Valid {
		_ = json.Unmarshal([]byte(metadataJSON.String), &r.Metadata)
	}
	if r.Metadata == nil {
		r.Metadata = make(map[string]string)
	}

	if tagsJSON.Valid {
		_ = json.Unmarshal([]byte(tagsJSON.String), &r.Tags)
	}
	if r.Tags == nil {
		r.Tags = make(map[string]string)
	}

	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	return r, nil
}

// Vacuum compacts the database
func (s *Store) Vacuum() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("VACUUM")
	return err
}

// PruneOldRuns removes resources older than the specified duration
func (s *Store) PruneOldRuns(olderThan time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-olderThan).Format(time.RFC3339)
	result, err := s.db.Exec(`DELETE FROM resources WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// GetResourcesForCleanup returns resources for a run in the correct order for deletion
// (reverse of creation order to handle dependencies)
func (s *Store) GetResourcesForCleanup(runID string) ([]*Resource, error) {
	return s.GetResourcesByRun(runID) // Already returns in reverse order
}

// String returns a summary of the resource for logging
func (r *Resource) String() string {
	if r.ResourceID != "" {
		return fmt.Sprintf("%s:%s", r.ResourceType, r.ResourceID)
	}
	if r.ResourceARN != "" {
		return fmt.Sprintf("%s:%s", r.ResourceType, r.ResourceARN)
	}
	if r.ResourceName != "" {
		return fmt.Sprintf("%s:%s", r.ResourceType, r.ResourceName)
	}
	return r.ResourceType
}
