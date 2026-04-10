package dbcontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/spf13/viper"
	_ "modernc.org/sqlite"
)

const (
	defaultPostgresPort    = 5432
	defaultMySQLPort       = 3306
	maxObjects             = 8
	maxColumns             = 6
	maxQueryRows           = 50
	maxQueryValueChars     = 240
	runtimeDBConnectionEnv = "CLANKER_RUNTIME_DB_CONNECTION_JSON"
)

type Connection struct {
	Name          string
	Driver        string
	Vendor        string
	Host          string
	Port          int
	Database      string
	Username      string
	Password      string
	PasswordEnv   string
	Path          string
	DSN           string
	DSNEnv        string
	Description   string
	SSLMode       string
	PoolMode      string
	QueryExecMode string
	Params        map[string]string
}

type Column struct {
	Name     string
	Type     string
	Nullable bool
}

type Object struct {
	Schema  string
	Name    string
	Type    string
	Columns []Column
}

type Inspection struct {
	Connection      Connection
	PingMillis      int64
	Version         string
	CurrentDatabase string
	Objects         []Object
}

type QueryResult struct {
	Connection Connection
	Query      string
	Columns    []string
	Rows       []map[string]string
	Truncated  bool
}

type queryRunner interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

func ListConnections() ([]Connection, string, error) {
	connections, defaultName := loadConnections()
	if len(connections) == 0 {
		return nil, defaultName, nil
	}
	sort.Slice(connections, func(i, j int) bool {
		return connections[i].Name < connections[j].Name
	})
	if defaultName == "" || !hasConnection(connections, defaultName) {
		defaultName = connections[0].Name
	}
	return connections, defaultName, nil
}

func ResolveConnection(name string) (Connection, error) {
	connections, defaultName, err := ListConnections()
	if err != nil {
		return Connection{}, err
	}
	if len(connections) == 0 {
		return Connection{}, fmt.Errorf("no database connections configured under databases.connections or postgres.connections")
	}
	trimmedName := strings.TrimSpace(name)
	if trimmedName != "" {
		for _, connection := range connections {
			if strings.EqualFold(connection.Name, trimmedName) {
				return connection, nil
			}
		}
		return Connection{}, fmt.Errorf("database connection %q not found", trimmedName)
	}
	for _, connection := range connections {
		if connection.Name == defaultName {
			return connection, nil
		}
	}
	return connections[0], nil
}

func Inspect(ctx context.Context, name string) (Inspection, error) {
	connection, err := ResolveConnection(name)
	if err != nil {
		return Inspection{}, err
	}
	return inspectConnection(ctx, connection)
}

func ExecuteReadQuery(ctx context.Context, name string, query string) (QueryResult, error) {
	connection, err := ResolveConnection(name)
	if err != nil {
		return QueryResult{}, err
	}
	return ExecuteReadQueryOnConnection(ctx, connection, query)
}

func ExecuteReadQueryOnConnection(ctx context.Context, connection Connection, query string) (QueryResult, error) {
	normalizedQuery, err := normalizeReadOnlyQuery(query)
	if err != nil {
		return QueryResult{}, err
	}

	driverName, dsn, err := openConfig(connection)
	if err != nil {
		return QueryResult{}, err
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return QueryResult{}, fmt.Errorf("open %s: %w", connection.Name, err)
	}
	defer db.Close()

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(2 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		return QueryResult{}, fmt.Errorf("ping %s: %w", connection.Name, err)
	}

	runner, cleanup, err := prepareInspectionRunner(ctx, db, connection)
	if err != nil {
		return QueryResult{}, err
	}
	defer cleanup()

	rows, err := runner.QueryContext(ctx, normalizedQuery)
	if err != nil {
		return QueryResult{}, fmt.Errorf("query %s: %w", connection.Name, err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return QueryResult{}, fmt.Errorf("columns %s: %w", connection.Name, err)
	}

	result := QueryResult{
		Connection: connection,
		Query:      normalizedQuery,
		Columns:    columns,
		Rows:       make([]map[string]string, 0, maxQueryRows),
	}

	for rows.Next() {
		values := make([]interface{}, len(columns))
		scanTargets := make([]interface{}, len(columns))
		for i := range values {
			scanTargets[i] = &values[i]
		}
		if err := rows.Scan(scanTargets...); err != nil {
			return QueryResult{}, fmt.Errorf("scan %s: %w", connection.Name, err)
		}
		if len(result.Rows) >= maxQueryRows {
			result.Truncated = true
			break
		}
		row := make(map[string]string, len(columns))
		for i, columnName := range columns {
			row[columnName] = formatQueryValue(values[i])
		}
		result.Rows = append(result.Rows, row)
	}

	if err := rows.Err(); err != nil {
		return QueryResult{}, fmt.Errorf("iterate %s: %w", connection.Name, err)
	}

	return result, nil
}

func BuildRelevantContext(ctx context.Context, question string, name string) (string, error) {
	connections, defaultName, err := ListConnections()
	if err != nil {
		return "", err
	}
	if len(connections) == 0 {
		return "Configured Database Connections:\n- none configured\n", nil
	}

	focus, err := resolveFocusedConnection(connections, defaultName, question, name)
	if err != nil {
		return "", err
	}

	b := &strings.Builder{}
	b.WriteString(fmt.Sprintf("Configured Database Connections (default: %s):\n", defaultName))
	for _, connection := range connections {
		marker := ""
		if connection.Name == defaultName {
			marker = " (default)"
		}
		b.WriteString(fmt.Sprintf("- %s%s [%s] %s\n", connection.Name, marker, connection.Kind(), connection.Target()))
		if connection.Description != "" {
			b.WriteString(fmt.Sprintf("  Description: %s\n", connection.Description))
		}
	}

	inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	inspection, inspectErr := inspectConnection(inspectCtx, focus)
	b.WriteString(fmt.Sprintf("\nFocused Database: %s\n", focus.Name))
	if inspectErr != nil {
		b.WriteString(fmt.Sprintf("Connection Status: unavailable (%v)\n", inspectErr))
		return b.String(), nil
	}

	b.WriteString(fmt.Sprintf("Connection Status: reachable (%d ms)\n", inspection.PingMillis))
	if inspection.Version != "" {
		b.WriteString(fmt.Sprintf("Engine Version: %s\n", inspection.Version))
	}
	if inspection.CurrentDatabase != "" {
		b.WriteString(fmt.Sprintf("Current Database: %s\n", inspection.CurrentDatabase))
	}
	if len(inspection.Objects) == 0 {
		b.WriteString("Objects: none discovered\n")
		return b.String(), nil
	}

	b.WriteString("Objects:\n")
	for _, object := range inspection.Objects {
		qualifiedName := object.Name
		if object.Schema != "" {
			qualifiedName = object.Schema + "." + object.Name
		}
		b.WriteString(fmt.Sprintf("- %s [%s]\n", qualifiedName, object.Type))
		if len(object.Columns) > 0 {
			parts := make([]string, 0, len(object.Columns))
			for _, column := range object.Columns {
				typeName := column.Type
				if typeName == "" {
					typeName = "unknown"
				}
				nullability := "nullable"
				if !column.Nullable {
					nullability = "not null"
				}
				parts = append(parts, fmt.Sprintf("%s %s %s", column.Name, typeName, nullability))
			}
			b.WriteString(fmt.Sprintf("  Columns: %s\n", strings.Join(parts, ", ")))
		}
	}

	return b.String(), nil
}

func (c Connection) Kind() string {
	vendor := strings.TrimSpace(c.Vendor)
	if vendor == "" || strings.EqualFold(vendor, c.Driver) {
		return c.Driver
	}
	return c.Driver + "/" + vendor
}

func (c Connection) Target() string {
	if c.Driver == "sqlite" {
		if resolved := c.resolvePath(); resolved != "" {
			return resolved
		}
		if resolved := c.resolveDSN(); resolved != "" {
			return resolved
		}
		return "sqlite"
	}

	if strings.TrimSpace(c.Host) != "" {
		hostPort := c.Host
		if c.Port > 0 {
			hostPort = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
		}
		if c.Database != "" {
			return hostPort + "/" + c.Database
		}
		return hostPort
	}

	if rawDSN := c.resolveDSN(); rawDSN != "" {
		if parsed, err := url.Parse(rawDSN); err == nil {
			target := parsed.Host
			if parsed.Path != "" && parsed.Path != "/" {
				target += parsed.Path
			}
			if target != "" {
				return target
			}
		}
		return "configured-dsn"
	}

	return c.Name
}

func loadConnections() ([]Connection, string) {
	defaultName := strings.TrimSpace(viper.GetString("databases.default_connection"))
	rawConnections := viper.GetStringMap("databases.connections")
	legacy := false
	if len(rawConnections) == 0 {
		rawConnections = viper.GetStringMap("postgres.connections")
		if defaultName == "" {
			defaultName = strings.TrimSpace(viper.GetString("postgres.default_connection"))
		}
		legacy = true
	}

	connections := make([]Connection, 0, len(rawConnections))
	for name, raw := range rawConnections {
		entry, ok := toStringMap(raw)
		if !ok {
			continue
		}
		connection, err := connectionFromMap(strings.TrimSpace(name), entry, legacy)
		if err != nil {
			continue
		}
		connections = append(connections, connection)
	}
	if runtimeConnection := loadRuntimeConnection(); runtimeConnection != nil {
		defaultName = runtimeConnection.Name
		replaced := false
		for i := range connections {
			if strings.EqualFold(connections[i].Name, runtimeConnection.Name) {
				connections[i] = *runtimeConnection
				replaced = true
				break
			}
		}
		if !replaced {
			connections = append(connections, *runtimeConnection)
		}
	}
	return connections, defaultName
}

func loadRuntimeConnection() *Connection {
	raw := strings.TrimSpace(os.Getenv(runtimeDBConnectionEnv))
	if raw == "" {
		return nil
	}

	var connection Connection
	if err := json.Unmarshal([]byte(raw), &connection); err != nil {
		return nil
	}
	if strings.TrimSpace(connection.Name) == "" {
		connection.Name = "default"
	}
	normalizeConnection(&connection)
	if connection.Driver == "sqlite" {
		if connection.resolvePath() == "" && connection.resolveDSN() == "" {
			return nil
		}
		return &connection
	}
	if connection.resolveDSN() != "" {
		return &connection
	}
	if connection.Host == "" || connection.Database == "" {
		return nil
	}
	return &connection
}

func connectionFromMap(name string, entry map[string]interface{}, legacy bool) (Connection, error) {
	connection := Connection{
		Name:          name,
		Driver:        firstNonEmpty(stringValue(entry, "driver"), stringValue(entry, "type"), stringValue(entry, "engine")),
		Vendor:        stringValue(entry, "vendor"),
		Host:          stringValue(entry, "host"),
		Port:          intValue(entry, "port"),
		Database:      firstNonEmpty(stringValue(entry, "database"), stringValue(entry, "dbname")),
		Username:      firstNonEmpty(stringValue(entry, "username"), stringValue(entry, "user")),
		Password:      stringValue(entry, "password"),
		PasswordEnv:   firstNonEmpty(stringValue(entry, "password_env"), stringValue(entry, "passwordEnv")),
		Path:          firstNonEmpty(stringValue(entry, "path"), stringValue(entry, "file"), stringValue(entry, "filename")),
		DSN:           firstNonEmpty(stringValue(entry, "dsn"), stringValue(entry, "url"), stringValue(entry, "connection_string")),
		DSNEnv:        firstNonEmpty(stringValue(entry, "dsn_env"), stringValue(entry, "dsnEnv")),
		Description:   stringValue(entry, "description"),
		SSLMode:       firstNonEmpty(stringValue(entry, "sslmode"), stringValue(entry, "ssl_mode")),
		PoolMode:      firstNonEmpty(stringValue(entry, "pool_mode"), stringValue(entry, "poolMode")),
		QueryExecMode: firstNonEmpty(stringValue(entry, "query_exec_mode"), stringValue(entry, "queryExecMode")),
		Params:        stringMapValue(entry["params"]),
	}

	if legacy {
		if connection.Driver == "" {
			connection.Driver = "postgres"
		}
		if connection.Vendor == "" {
			connection.Vendor = "postgres"
		}
	}

	normalizeConnection(&connection)
	if connection.Name == "" {
		return Connection{}, fmt.Errorf("database connection name is required")
	}
	if connection.Driver == "" {
		return Connection{}, fmt.Errorf("database connection %q is missing a driver", connection.Name)
	}
	if connection.Driver == "sqlite" {
		if connection.resolvePath() == "" && connection.resolveDSN() == "" {
			return Connection{}, fmt.Errorf("sqlite connection %q requires path or dsn", connection.Name)
		}
		return connection, nil
	}
	if connection.resolveDSN() != "" {
		return connection, nil
	}
	if connection.Host == "" || connection.Database == "" {
		return Connection{}, fmt.Errorf("database connection %q requires host and database", connection.Name)
	}
	return connection, nil
}

func normalizeConnection(connection *Connection) {
	raw := strings.ToLower(strings.TrimSpace(firstNonEmpty(connection.Driver, connection.Vendor, guessDriverFromDSN(connection.resolveDSN()))))
	switch raw {
	case "postgresql", "postgres", "supabase", "neon":
		connection.Driver = "postgres"
		if raw == "supabase" || raw == "neon" {
			connection.Vendor = raw
		} else if strings.TrimSpace(connection.Vendor) == "" {
			connection.Vendor = "postgres"
		}
		if connection.Port == 0 {
			connection.Port = defaultPostgresPort
		}
	case "mysql", "mariadb":
		connection.Driver = "mysql"
		if strings.TrimSpace(connection.Vendor) == "" {
			connection.Vendor = "mysql"
		}
		if connection.Port == 0 {
			connection.Port = defaultMySQLPort
		}
	case "sqlite", "sqlite3":
		connection.Driver = "sqlite"
		if strings.TrimSpace(connection.Vendor) == "" {
			connection.Vendor = "sqlite"
		}
	default:
		connection.Driver = raw
		if strings.TrimSpace(connection.Vendor) == "" {
			connection.Vendor = raw
		}
	}
}

func resolveFocusedConnection(connections []Connection, defaultName string, question string, explicitName string) (Connection, error) {
	if strings.TrimSpace(explicitName) != "" {
		return ResolveConnection(explicitName)
	}
	questionLower := strings.ToLower(strings.TrimSpace(question))
	if questionLower != "" {
		for _, connection := range connections {
			if strings.Contains(questionLower, strings.ToLower(connection.Name)) {
				return connection, nil
			}
		}
	}
	for _, connection := range connections {
		if connection.Name == defaultName {
			return connection, nil
		}
	}
	return connections[0], nil
}

func inspectConnection(ctx context.Context, connection Connection) (Inspection, error) {
	driverName, dsn, err := openConfig(connection)
	if err != nil {
		return Inspection{}, err
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return Inspection{}, fmt.Errorf("open %s: %w", connection.Name, err)
	}
	defer db.Close()

	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(2 * time.Minute)

	start := time.Now()
	if err := db.PingContext(ctx); err != nil {
		return Inspection{}, fmt.Errorf("ping %s: %w", connection.Name, err)
	}

	runner, cleanup, err := prepareInspectionRunner(ctx, db, connection)
	if err != nil {
		return Inspection{}, err
	}
	defer cleanup()

	inspection := Inspection{
		Connection: connection,
		PingMillis: time.Since(start).Milliseconds(),
	}

	inspection.Version = queryVersion(ctx, runner, connection)
	inspection.CurrentDatabase = queryCurrentDatabase(ctx, runner, connection)
	objects, err := queryObjects(ctx, runner, connection)
	if err == nil {
		inspection.Objects = objects
	}

	return inspection, nil
}

func prepareInspectionRunner(ctx context.Context, db *sql.DB, connection Connection) (queryRunner, func(), error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s inspection session: %w", connection.Name, err)
	}

	cleanupConn := func() {
		_ = conn.Close()
	}

	switch connection.Driver {
	case "postgres":
		if _, err := conn.ExecContext(ctx, "set default_transaction_read_only = on"); err != nil {
			cleanupConn()
			return nil, nil, fmt.Errorf("set %s inspection session read-only: %w", connection.Name, err)
		}
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			cleanupConn()
			return nil, nil, fmt.Errorf("start %s read-only transaction: %w", connection.Name, err)
		}
		return tx, func() {
			_ = tx.Rollback()
			cleanupConn()
		}, nil
	case "mysql":
		if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
			cleanupConn()
			return nil, nil, fmt.Errorf("set %s inspection session read-only: %w", connection.Name, err)
		}
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			cleanupConn()
			return nil, nil, fmt.Errorf("start %s read-only transaction: %w", connection.Name, err)
		}
		return tx, func() {
			_ = tx.Rollback()
			cleanupConn()
		}, nil
	case "sqlite":
		if _, err := conn.ExecContext(ctx, "PRAGMA query_only = 1"); err != nil {
			cleanupConn()
			return nil, nil, fmt.Errorf("set %s inspection session query_only: %w", connection.Name, err)
		}
		return conn, cleanupConn, nil
	default:
		return conn, cleanupConn, nil
	}
}

func openConfig(connection Connection) (string, string, error) {
	switch connection.Driver {
	case "postgres":
		dsn, err := postgresDSN(connection)
		return "pgx", dsn, err
	case "mysql":
		dsn, err := mysqlDSN(connection)
		return "mysql", dsn, err
	case "sqlite":
		if resolved := connection.resolvePath(); resolved != "" {
			return "sqlite", sqliteReadOnlyDSN(resolved), nil
		}
		if resolved := connection.resolveDSN(); resolved != "" {
			return "sqlite", sqliteReadOnlyDSN(resolved), nil
		}
		return "", "", fmt.Errorf("sqlite connection %q is missing a path", connection.Name)
	default:
		return "", "", fmt.Errorf("unsupported database driver %q", connection.Driver)
	}
}

func postgresDSN(connection Connection) (string, error) {
	if raw := connection.resolveDSN(); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil {
			return raw, nil
		}
		query := parsed.Query()
		if query.Get("default_transaction_read_only") == "" {
			query.Set("default_transaction_read_only", "on")
		}
		if query.Get("sslmode") == "" {
			query.Set("sslmode", defaultPostgresSSLMode(connection))
		}
		if execMode := defaultPostgresQueryExecMode(connection); execMode != "" && query.Get("default_query_exec_mode") == "" {
			query.Set("default_query_exec_mode", execMode)
		}
		for key, value := range connection.Params {
			query.Set(key, value)
		}
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}

	query := url.Values{}
	query.Set("default_transaction_read_only", "on")
	query.Set("sslmode", defaultPostgresSSLMode(connection))
	if execMode := defaultPostgresQueryExecMode(connection); execMode != "" {
		query.Set("default_query_exec_mode", execMode)
	}
	for key, value := range connection.Params {
		query.Set(key, value)
	}

	u := &url.URL{
		Scheme:   "postgres",
		Host:     net.JoinHostPort(connection.Host, strconv.Itoa(connection.Port)),
		Path:     "/" + strings.TrimPrefix(connection.Database, "/"),
		RawQuery: query.Encode(),
	}
	if connection.Username != "" {
		password := connection.resolvePassword()
		if password != "" {
			u.User = url.UserPassword(connection.Username, password)
		} else {
			u.User = url.User(connection.Username)
		}
	}
	return u.String(), nil
}

func mysqlDSN(connection Connection) (string, error) {
	if raw := connection.resolveDSN(); raw != "" {
		parsed, err := mysql.ParseDSN(raw)
		if err != nil {
			return raw, nil
		}
		applyMySQLDefaults(parsed, connection)
		return parsed.FormatDSN(), nil
	}

	config := mysql.NewConfig()
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(connection.Host, strconv.Itoa(connection.Port))
	config.DBName = connection.Database
	config.User = connection.Username
	config.Passwd = connection.resolvePassword()
	applyMySQLDefaults(config, connection)
	return config.FormatDSN(), nil
}

func applyMySQLDefaults(config *mysql.Config, connection Connection) {
	if config.Params == nil {
		config.Params = map[string]string{}
	}
	if _, ok := connection.Params["parseTime"]; ok {
		config.ParseTime = strings.EqualFold(connection.Params["parseTime"], "true")
	} else {
		config.ParseTime = true
	}
	if tlsValue, ok := connection.Params["tls"]; ok {
		config.TLSConfig = tlsValue
	} else if strings.EqualFold(strings.TrimSpace(connection.SSLMode), "disable") {
		config.TLSConfig = "false"
	} else {
		config.TLSConfig = "true"
	}
	for key, value := range connection.Params {
		if key == "parseTime" || key == "tls" {
			continue
		}
		config.Params[key] = value
	}
}

func defaultPostgresSSLMode(connection Connection) string {
	if strings.TrimSpace(connection.SSLMode) != "" {
		return strings.TrimSpace(connection.SSLMode)
	}
	if strings.EqualFold(connection.Vendor, "neon") {
		return "verify-full"
	}
	if strings.EqualFold(connection.Vendor, "supabase") {
		return "require"
	}
	if connection.Host == "localhost" || connection.Host == "127.0.0.1" || connection.Host == "::1" {
		return "disable"
	}
	return "require"
}

func defaultPostgresQueryExecMode(connection Connection) string {
	if strings.TrimSpace(connection.QueryExecMode) != "" {
		return strings.TrimSpace(connection.QueryExecMode)
	}
	if strings.EqualFold(connection.PoolMode, "transaction") {
		return "simple_protocol"
	}
	return ""
}

func sqliteReadOnlyDSN(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	if strings.HasPrefix(trimmed, "file:") {
		parsed, err := url.Parse(trimmed)
		if err == nil {
			query := parsed.Query()
			if query.Get("mode") == "" {
				query.Set("mode", "ro")
			}
			parsed.RawQuery = query.Encode()
			return parsed.String()
		}
		if strings.Contains(trimmed, "?") {
			return trimmed + "&mode=ro"
		}
		return trimmed + "?mode=ro"
	}

	resolvedPath := trimmed
	if abs, err := filepath.Abs(trimmed); err == nil {
		resolvedPath = abs
	}

	parsed := &url.URL{Scheme: "file", Path: resolvedPath}
	query := parsed.Query()
	query.Set("mode", "ro")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func normalizeReadOnlyQuery(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimSuffix(trimmed, ";")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "", fmt.Errorf("query is required")
	}
	if strings.Contains(trimmed, ";") {
		return "", fmt.Errorf("only a single SQL statement is allowed")
	}
	if strings.Contains(trimmed, "--") || strings.Contains(trimmed, "/*") || strings.Contains(trimmed, "*/") {
		return "", fmt.Errorf("comments are not allowed in read queries")
	}

	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return "", fmt.Errorf("only SELECT queries are allowed")
	}

	disallowedFragments := []string{
		" insert ", " update ", " delete ", " drop ", " alter ", " create ", " truncate ",
		" replace ", " merge ", " grant ", " revoke ", " call ", " execute ", " exec ",
		" copy ", " attach ", " detach ", " pragma ", " vacuum ", " analyze ", " reindex ",
		" begin ", " commit ", " rollback ", " savepoint ", " release ", " lock table ",
		" for update", " into outfile", " load_file", "pg_terminate_backend", "pg_reload_conf",
	}
	padded := " " + lower + " "
	for _, fragment := range disallowedFragments {
		if strings.Contains(padded, fragment) {
			return "", fmt.Errorf("read query contains a forbidden operation")
		}
	}

	return trimmed, nil
}

func formatQueryValue(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return "NULL"
	case []byte:
		return truncateQueryValue(string(typed))
	case string:
		return truncateQueryValue(typed)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return truncateQueryValue(fmt.Sprint(typed))
	}
}

func truncateQueryValue(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return trimmed
	}
	if len(trimmed) <= maxQueryValueChars {
		return trimmed
	}
	return trimmed[:maxQueryValueChars] + "...<truncated>"
}

func queryVersion(ctx context.Context, runner queryRunner, connection Connection) string {
	query := ""
	switch connection.Driver {
	case "postgres", "mysql":
		query = "select version()"
	case "sqlite":
		query = "select sqlite_version()"
	}
	if query == "" {
		return ""
	}
	var value sql.NullString
	if err := runner.QueryRowContext(ctx, query).Scan(&value); err != nil || !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}

func queryCurrentDatabase(ctx context.Context, runner queryRunner, connection Connection) string {
	query := ""
	switch connection.Driver {
	case "postgres":
		query = "select current_database()"
	case "mysql":
		query = "select database()"
	case "sqlite":
		return filepath.Base(connection.resolvePath())
	}
	if query == "" {
		return ""
	}
	var value sql.NullString
	if err := runner.QueryRowContext(ctx, query).Scan(&value); err != nil || !value.Valid {
		return connection.Database
	}
	return strings.TrimSpace(value.String)
}

func queryObjects(ctx context.Context, runner queryRunner, connection Connection) ([]Object, error) {
	switch connection.Driver {
	case "postgres":
		return queryPostgresObjects(ctx, runner)
	case "mysql":
		return queryMySQLObjects(ctx, runner)
	case "sqlite":
		return querySQLiteObjects(ctx, runner)
	default:
		return nil, fmt.Errorf("unsupported database driver %q", connection.Driver)
	}
}

func queryPostgresObjects(ctx context.Context, runner queryRunner) ([]Object, error) {
	rows, err := runner.QueryContext(ctx, `
		select table_schema, table_name, table_type
		from information_schema.tables
		where table_schema not in ('pg_catalog', 'information_schema')
		order by table_schema, table_name
		limit $1
	`, maxObjects)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	objects := []Object{}
	for rows.Next() {
		var object Object
		var tableType string
		if err := rows.Scan(&object.Schema, &object.Name, &tableType); err != nil {
			return nil, err
		}
		object.Type = normalizeObjectType(tableType)
		object.Columns = queryPostgresColumns(ctx, runner, object.Schema, object.Name)
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func queryPostgresColumns(ctx context.Context, runner queryRunner, schema string, table string) []Column {
	rows, err := runner.QueryContext(ctx, `
		select column_name, data_type, is_nullable
		from information_schema.columns
		where table_schema = $1 and table_name = $2
		order by ordinal_position
		limit $3
	`, schema, table, maxColumns)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanColumns(rows, func(values []interface{}) Column {
		return Column{
			Name:     stringFromScan(values[0]),
			Type:     stringFromScan(values[1]),
			Nullable: strings.EqualFold(stringFromScan(values[2]), "YES"),
		}
	})
}

func queryMySQLObjects(ctx context.Context, runner queryRunner) ([]Object, error) {
	rows, err := runner.QueryContext(ctx, `
		select table_schema, table_name, table_type
		from information_schema.tables
		where table_schema = database()
		order by table_name
		limit ?
	`, maxObjects)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	objects := []Object{}
	for rows.Next() {
		var object Object
		var tableType string
		if err := rows.Scan(&object.Schema, &object.Name, &tableType); err != nil {
			return nil, err
		}
		object.Type = normalizeObjectType(tableType)
		object.Columns = queryMySQLColumns(ctx, runner, object.Name)
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func queryMySQLColumns(ctx context.Context, runner queryRunner, table string) []Column {
	rows, err := runner.QueryContext(ctx, `
		select column_name, data_type, is_nullable
		from information_schema.columns
		where table_schema = database() and table_name = ?
		order by ordinal_position
		limit ?
	`, table, maxColumns)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanColumns(rows, func(values []interface{}) Column {
		return Column{
			Name:     stringFromScan(values[0]),
			Type:     stringFromScan(values[1]),
			Nullable: strings.EqualFold(stringFromScan(values[2]), "YES"),
		}
	})
}

func querySQLiteObjects(ctx context.Context, runner queryRunner) ([]Object, error) {
	rows, err := runner.QueryContext(ctx, `
		select name, type
		from sqlite_master
		where type in ('table', 'view') and name not like 'sqlite_%'
		order by name
		limit ?
	`, maxObjects)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	objects := []Object{}
	for rows.Next() {
		var object Object
		if err := rows.Scan(&object.Name, &object.Type); err != nil {
			return nil, err
		}
		object.Type = normalizeObjectType(object.Type)
		object.Columns = querySQLiteColumns(ctx, runner, object.Name)
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func querySQLiteColumns(ctx context.Context, runner queryRunner, table string) []Column {
	quotedTable := strings.ReplaceAll(table, `"`, `""`)
	rows, err := runner.QueryContext(ctx, fmt.Sprintf(`pragma table_info("%s")`, quotedTable))
	if err != nil {
		return nil
	}
	defer rows.Close()

	columns := []Column{}
	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil
		}
		columns = append(columns, Column{
			Name:     strings.TrimSpace(name),
			Type:     strings.TrimSpace(dataType),
			Nullable: notNull == 0,
		})
		if len(columns) >= maxColumns {
			break
		}
	}
	return columns
}

func scanColumns(rows *sql.Rows, build func([]interface{}) Column) []Column {
	columns := []Column{}
	for rows.Next() {
		values := []interface{}{new(sql.NullString), new(sql.NullString), new(sql.NullString)}
		if err := rows.Scan(values...); err != nil {
			return nil
		}
		columns = append(columns, build(values))
	}
	return columns
}

func normalizeObjectType(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "base table":
		return "table"
	case "view":
		return "view"
	default:
		return value
	}
}

func hasConnection(connections []Connection, name string) bool {
	for _, connection := range connections {
		if connection.Name == name {
			return true
		}
	}
	return false
}

func toStringMap(raw interface{}) (map[string]interface{}, bool) {
	if raw == nil {
		return nil, false
	}
	switch typed := raw.(type) {
	case map[string]interface{}:
		return typed, true
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, value := range typed {
			out[fmt.Sprint(key)] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func stringMapValue(raw interface{}) map[string]string {
	entry, ok := toStringMap(raw)
	if !ok {
		return nil
	}
	params := make(map[string]string, len(entry))
	for key, value := range entry {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(fmt.Sprint(value))
		if trimmedKey == "" || trimmedValue == "" {
			continue
		}
		params[trimmedKey] = trimmedValue
	}
	return params
}

func stringValue(entry map[string]interface{}, key string) string {
	if entry == nil {
		return ""
	}
	value, ok := entry[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intValue(entry map[string]interface{}, key string) int {
	if entry == nil {
		return 0
	}
	value, ok := entry[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		parsed, _ := strconv.Atoi(strings.TrimSpace(fmt.Sprint(value)))
		return parsed
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func guessDriverFromDSN(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://"):
		return "postgres"
	case strings.Contains(trimmed, "@tcp("):
		return "mysql"
	case strings.HasSuffix(trimmed, ".db") || strings.HasSuffix(trimmed, ".sqlite") || strings.HasSuffix(trimmed, ".sqlite3"):
		return "sqlite"
	default:
		return ""
	}
}

func stringFromScan(raw interface{}) string {
	if raw == nil {
		return ""
	}
	switch typed := raw.(type) {
	case *sql.NullString:
		if typed.Valid {
			return strings.TrimSpace(typed.String)
		}
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(raw))
	}
}

func (c Connection) resolvePassword() string {
	if strings.TrimSpace(c.Password) != "" {
		return strings.TrimSpace(c.Password)
	}
	if strings.TrimSpace(c.PasswordEnv) != "" {
		return strings.TrimSpace(os.Getenv(strings.TrimSpace(c.PasswordEnv)))
	}
	return ""
}

func (c Connection) resolvePath() string {
	if strings.TrimSpace(c.Path) == "" {
		return ""
	}
	if abs, err := filepath.Abs(strings.TrimSpace(c.Path)); err == nil {
		return abs
	}
	return strings.TrimSpace(c.Path)
}

func (c Connection) resolveDSN() string {
	if strings.TrimSpace(c.DSN) != "" {
		return strings.TrimSpace(c.DSN)
	}
	if strings.TrimSpace(c.DSNEnv) != "" {
		return strings.TrimSpace(os.Getenv(strings.TrimSpace(c.DSNEnv)))
	}
	return ""
}
