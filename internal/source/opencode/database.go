package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
)

var nativeSessionID = regexp.MustCompile(`^ses_[A-Za-z0-9_-]{1,120}$`)

// DatabasePath is opt-in. Ordinary scans never probe a user's live default DB.
func DatabasePath() (string, error) {
	p := os.Getenv("SESSION_REVIEWER_OPENCODE_DB")
	if p != "" && (!filepath.IsAbs(p) || filepath.Clean(p) != p) {
		return "", errors.New("OpenCode database path must be absolute and clean")
	}
	return p, nil
}
func checkSchema(ctx context.Context, db *sql.DB) error {
	for table, columns := range map[string][]string{"session": {"id", "directory", "time_created", "parent_id", "revert"}, "message": {"id", "session_id", "time_created", "data"}, "part": {"id", "message_id", "session_id", "data"}} {
		var kind string
		if err := db.QueryRowContext(ctx, `SELECT type FROM sqlite_schema WHERE name=?`, table).Scan(&kind); err != nil || kind != "table" {
			return errors.New("unsupported OpenCode SQLite schema")
		}
		rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return err
		}
		found := map[string]bool{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			found[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, col := range columns {
			if !found[col] {
				return fmt.Errorf("unsupported OpenCode schema: %s.%s missing", table, col)
			}
		}
	}
	return nil
}
func physicalDirectory(path string) (pathguard.IdentityToken, error) {
	d, err := pathguard.Open(path)
	if err != nil {
		return pathguard.IdentityToken{}, err
	}
	defer d.Close()
	return d.PhysicalIdentity()
}
func authenticateBinding(b projectidentity.Binding) error {
	identity, err := physicalDirectory(b.CanonicalRoot)
	if err != nil || identity != b.RootIdentity {
		return errors.New("OpenCode project physical identity changed")
	}
	return nil
}
func sourceIdentity(row sessionRow) (string, error) {
	identity, err := physicalDirectory(row.Directory)
	if err != nil {
		return "", err
	}
	return "source-" + hashBytes([]byte(strings.Join([]string{"opencode", row.ID, row.Directory, fmt.Sprint(row.Created), identity.Kind, identity.Volume, identity.File}, "\x00"))), nil
}

func projectSessions(ctx context.Context, db *sql.DB, bindings []projectidentity.Binding) ([]sessionRow, map[string]projectidentity.Binding, error) {
	directories, err := db.QueryContext(ctx, `SELECT DISTINCT directory FROM session LIMIT ?`, maxSessions+1)
	if err != nil {
		return nil, nil, err
	}
	var names []string
	for directories.Next() {
		var dir string
		if err = directories.Scan(&dir); err != nil {
			break
		}
		names = append(names, dir)
	}
	if err == nil {
		err = directories.Err()
	}
	directories.Close()
	if err != nil {
		return nil, nil, err
	}
	if len(names) > maxSessions {
		return nil, nil, errors.New("OpenCode directory budget exceeded")
	}
	associated := map[string]projectidentity.Binding{}
	var sessions []sessionRow
	for _, dir := range names {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			continue
		}
		identity, err := physicalDirectory(dir)
		if err != nil {
			continue
		}
		for _, binding := range bindings {
			if identity != binding.RootIdentity {
				continue
			}
			rows, err := db.QueryContext(ctx, `SELECT id,directory,time_created,revert FROM session WHERE directory=? ORDER BY time_created,id LIMIT ?`, dir, maxSessions+1)
			if err != nil {
				return nil, nil, err
			}
			for rows.Next() {
				var row sessionRow
				var revert sql.NullString
				if err = rows.Scan(&row.ID, &row.Directory, &row.Created, &revert); err != nil {
					break
				}
				if !nativeSessionID.MatchString(row.ID) || row.Created < 1 || (revert.Valid && revert.String != "" && revert.String != "null") {
					err = errors.New("unsupported OpenCode Session identity or reverted history")
					break
				}
				if _, duplicate := associated[row.ID]; duplicate {
					err = errors.New("ambiguous OpenCode project binding")
					break
				}
				associated[row.ID] = binding
				sessions = append(sessions, row)
				if len(sessions) > maxSessions {
					err = errors.New("OpenCode Session budget exceeded")
					break
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				return nil, nil, err
			}
		}
	}
	return sessions, associated, nil
}
func readSession(ctx context.Context, db *sql.DB, id string) (sessionRow, []rawMessage, error) {
	var row sessionRow
	var revert sql.NullString
	if !nativeSessionID.MatchString(id) {
		return row, nil, errors.New("invalid OpenCode Session ID")
	}
	if err := db.QueryRowContext(ctx, `SELECT id,directory,time_created,revert FROM session WHERE id=?`, id).Scan(&row.ID, &row.Directory, &row.Created, &revert); err != nil {
		return row, nil, err
	}
	if row.Created < 1 || (revert.Valid && revert.String != "" && revert.String != "null") {
		return row, nil, errors.New("unsupported reverted OpenCode history")
	}
	// The newer event-sourced storage has different replay semantics. Never infer
	// completeness from legacy rows if that format contains this Session.
	var modern int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='session_message' AND type='table'`).Scan(&modern); err != nil {
		return row, nil, err
	}
	if modern > 0 {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM session_message WHERE session_id=?`, id).Scan(&count); err != nil {
			return row, nil, err
		}
		if count > 0 {
			return row, nil, errors.New("unsupported OpenCode event-sourced Session schema")
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT id,time_created,data FROM message WHERE session_id=? ORDER BY time_created,id LIMIT ?`, id, maxRows+1)
	if err != nil {
		return row, nil, err
	}
	messages := []rawMessage{}
	byID := map[string]int{}
	totalBytes := 0
	for rows.Next() {
		var m rawMessage
		var data string
		if err = rows.Scan(&m.ID, &m.Created, &data); err != nil {
			break
		}
		m.Data = []byte(data)
		if m.ID == "" {
			err = errors.New("missing OpenCode message ID")
			break
		}
		if _, ok := byID[m.ID]; ok {
			err = errors.New("duplicate OpenCode message ID")
			break
		}
		m.Parts = []rawPart{}
		byID[m.ID] = len(messages)
		messages = append(messages, m)
		totalBytes += len(m.Data)
		if len(messages) > maxRows || totalBytes > maxContentBytes {
			err = errors.New("OpenCode message budget exceeded")
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return row, nil, err
	}
	parts, err := db.QueryContext(ctx, `SELECT id,message_id,data FROM part WHERE session_id=? ORDER BY message_id,id LIMIT ?`, id, maxRows+1)
	if err != nil {
		return row, nil, err
	}
	count := 0
	for parts.Next() {
		var p rawPart
		var owner, data string
		if err = parts.Scan(&p.ID, &owner, &data); err != nil {
			break
		}
		p.Data = []byte(data)
		count++
		totalBytes += len(p.Data)
		if count > maxRows || totalBytes > maxContentBytes {
			err = errors.New("OpenCode part budget exceeded")
			break
		}
		index, ok := byID[owner]
		if !ok || p.ID == "" {
			err = errors.New("orphan OpenCode part")
			break
		}
		messages[index].Parts = append(messages[index].Parts, p)
	}
	if err == nil {
		err = parts.Err()
	}
	parts.Close()
	return row, messages, err
}

// Native Session counters are optional in older schemas and remain outside
// canonical record identity because they can include an active turn's usage.
type sessionTokenTotals struct {
	Values  map[string]int64
	Invalid bool
}

func sessionTokenColumns(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(session)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := []string{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		switch name {
		case "tokens_input", "tokens_output", "tokens_reasoning", "tokens_cache_read", "tokens_cache_write":
			columns = append(columns, name)
		}
	}
	return columns, rows.Err()
}

func readSessionTokenTotals(ctx context.Context, db *sql.DB, id string, columns []string) (sessionTokenTotals, error) {
	totals := sessionTokenTotals{Values: map[string]int64{}}
	if len(columns) == 0 {
		return totals, nil
	}
	values, destinations := make([]any, len(columns)), make([]any, len(columns))
	for i := range values {
		destinations[i] = &values[i]
	}
	// columns contains only the fixed allowlist emitted by sessionTokenColumns.
	if err := db.QueryRowContext(ctx, "SELECT "+strings.Join(columns, ",")+" FROM session WHERE id=?", id).Scan(destinations...); err != nil {
		return totals, err
	}
	for i, raw := range values {
		if raw == nil {
			continue
		}
		value, ok := raw.(int64)
		if !ok || value < 0 || value > 1<<53-1 {
			totals.Invalid = true
			continue
		}
		totals.Values[columns[i]] = value
	}
	return totals, nil
}
