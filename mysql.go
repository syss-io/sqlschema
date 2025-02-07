package sqlschema

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type MySQLServer struct {
	conn   DB
	logger *slog.Logger
}

func MySQL(conn DB, logger *slog.Logger) *MySQLServer {
	if logger == nil {
		logger = slog.Default()
	}

	return &MySQLServer{conn, logger}
}

func (m *MySQLServer) DescribeServer(ctx context.Context) (*MySQLServerInfo, error) {
	version, err := m.serverVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't fetch MySQL server version")
	}

	configurations, err := m.serverConfigurations(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't fetch MySQL server configurations")
	}

	return &MySQLServerInfo{
		Version:        version,
		Configurations: configurations,
	}, nil
}

func (m *MySQLServer) DescribeAvailableDatabases(ctx context.Context) ([]string, error) {
	query := "SHOW DATABASES;"

	m.logger.Info("execute mysql query", slog.String("query", query))

	rows, err := m.conn.QueryContext(ctx, query)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("failed to fetch databases: %w", err)
	}
	defer rows.Close()

	var databases []string
	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			return nil, fmt.Errorf("failed to scan database name: %w", err)
		}
		databases = append(databases, dbName)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating over database rows: %w", err)
	}

	return databases, nil
}

func (m *MySQLServer) DescribeDatabase(ctx context.Context, name string) (*MySQLDatabaseDefinition, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// fetch foreign keys ..
	// TODO: make this work
	// g.Go(func() error {
	// 	foreignKeys, err := GetMySQLForeignKeys(ctx, name)
	// 	if err != nil {
	// 		return fmt.Errorf("can't fetch database foreign keys: %w", err)
	// 	}

	// 	schemaForeignKeys = foreignKeys
	// 	return nil
	// })

	// fetch functions ..
	schemaFunctions, err := m.DescribeSchemaFunctions(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("can't fetch database functions: %w", err)
	}

	// fetch stored procedures
	schemaStoredProcedures, err := m.DescribeSchemaStoredProcedures(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("can't fetch database stored procedures: %w", err)
	}

	// fetch triggers
	schemaTriggers, err := m.DescribeSchemaTriggers(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("can't fetch triggers: %w", err)
	}

	// fetch indexes
	schemaIndexes, err := m.DescribeSchemaIndexes(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("can't fetch database indexes: %w", err)
	}

	schemaColumns, err := m.DescribeSchemaColumns(ctx, name, nil, schemaIndexes)
	if err != nil {
		return nil, fmt.Errorf("can't fetch columns: %w", err)
	}

	tables, err := m.DescribeSchemaTables(ctx, name, nil, schemaIndexes, schemaColumns)
	if err != nil {
		return nil, fmt.Errorf("can't fetch tables: %w", err)
	}

	views, err := m.DescribeSchemaViews(ctx, name, schemaColumns)
	if err != nil {
		return nil, fmt.Errorf("can't fetch views: %w", err)
	}

	return &MySQLDatabaseDefinition{
		DatabaseName:     name,
		Tables:           tables,
		Functions:        schemaFunctions,
		StoredProcedures: schemaStoredProcedures,
		Triggers:         schemaTriggers,
		Views:            views,
	}, nil
}

func (m *MySQLServer) DescribeForeignKeys(ctx context.Context, database string) ([]*MySQLForeignKeyDefinition, error) {
	query := `
	SELECT
		kcu.CONSTRAINT_NAME,
		kcu.TABLE_NAME,
		kcu.COLUMN_NAME,
		kcu.REFERENCED_TABLE_NAME,
		kcu.REFERENCED_COLUMN_NAME,
		rc.UPDATE_RULE,
		rc.DELETE_RULE
	FROM
		INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
	JOIN
		INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS rc
	ON
		kcu.CONSTRAINT_NAME = rc.CONSTRAINT_NAME   
		AND kcu.TABLE_SCHEMA = rc.CONSTRAINT_SCHEMA
	WHERE
		kcu.REFERENCED_TABLE_SCHEMA = ?
		AND kcu.REFERENCED_TABLE_NAME IS NOT NULL;
	`

	m.logger.Info("execute mysql query", slog.String("query", query))

	rows, err := m.conn.QueryContext(ctx, query, database)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	var foreignKeys []*MySQLForeignKeyDefinition
	for rows.Next() {
		var fk MySQLForeignKeyDefinition
		err := rows.Scan(
			&fk.ConstraintName,
			&fk.TableName,
			&fk.ColumnName,
			&fk.ForeignTableName,
			&fk.ForeignColumnName,
			&fk.OnUpdate,
			&fk.OnDelete,
		)
		if err != nil {
			return nil, err
		}
		fk.DatabaseName = database
		foreignKeys = append(foreignKeys, &fk)
	}

	return foreignKeys, nil
}

func (m *MySQLServer) DescribeSchemaColumns(ctx context.Context, database string, foreignKeys []*MySQLForeignKeyDefinition, indexes []*MySQLIndexDefinition) ([]*MySQLColumnDefinition, error) {
	query := `
	SELECT
		-- ordinal_position AS ordinal_position,
		TABLE_NAME,
		column_name,
		column_type AS data_type,
		-- character_set_name AS character_set,
		-- collation_name AS collation,
		is_nullable
		-- column_default,
		-- extra,
		-- column_comment AS comment
	FROM
		information_schema.columns
	WHERE
		table_schema = ?;
	`

	m.logger.Info("execute mysql query", slog.String("query", query))

	rows, err := m.conn.QueryContext(ctx, query, database)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	var columns []*MySQLColumnDefinition
	for rows.Next() {
		var col MySQLColumnDefinition
		var isNullable string
		err := rows.Scan(
			&col.TableName,
			// &col.OrdinalPosition,
			&col.Name,
			&col.Type,
			// &col.Charset,
			//&col.Collation,
			&isNullable,
			// &col.DefaultValue,
			// &col.Extra,
			// &col.Comment,
		)
		if err != nil {
			return nil, err
		}

		col.IsNullable = isNullable != "NO"

		// Filter foreign keys and indexes based on column name and database
		col.ForeignKeys = m.filterForeignKeys(foreignKeys, col.Name, database)
		col.Indexes = m.filterIndexes(indexes, col.Name, database)

		columns = append(columns, &col)
	}

	return columns, nil
}

func (m *MySQLServer) DescribeSchemaTables(ctx context.Context, database string, foreignKeys []*MySQLForeignKeyDefinition, indexes []*MySQLIndexDefinition, columns []*MySQLColumnDefinition) ([]*MySQLTableDefinition, error) {
	query := `
	SELECT table_name
	FROM
		information_schema.tables
	WHERE
		table_schema = ?;
	`

	m.logger.Info("execute mysql query", slog.String("query", query))

	rows, err := m.conn.QueryContext(ctx, query, database)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	var tables []*MySQLTableDefinition
	for rows.Next() {
		var table MySQLTableDefinition
		err := rows.Scan(&table.TableName)
		if err != nil {
			return nil, err
		}
		table.DatabaseName = database

		// Filter columns, foreign keys, and indexes based on table name
		table.Columns = m.filterColumns(columns, table.TableName)
		table.ForeignKeys = m.filterForeignKeysByTable(foreignKeys, table.TableName)
		table.Indexes = m.filterIndexesByTable(indexes, table.TableName)

		tables = append(tables, &table)
	}

	slog.Info("tables", slog.Any("tables", tables))

	return tables, nil
}

func (m *MySQLServer) DescribeSchemaViews(ctx context.Context, database string, columns []*MySQLColumnDefinition) ([]*MySQLViewDefinition, error) {
	query := `
	SELECT TABLE_NAME, VIEW_DEFINITION, IS_UPDATABLE
	FROM
		information_schema.views
	WHERE
		table_schema = ?;
	`

	m.logger.Info("execute mysql query", slog.String("query", query))

	rows, err := m.conn.QueryContext(ctx, query, database)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	var views []*MySQLViewDefinition
	for rows.Next() {
		var isUpdatable string
		var view MySQLViewDefinition
		err := rows.Scan(
			&view.ViewName,
			&view.Definition,
			&isUpdatable,
		)
		if err != nil {
			return nil, err
		}

		view.DatabaseName = database
		view.IsUpdatable = isUpdatable != "NO"

		// Filter columns based on view name (assuming columns belong to views)
		view.Columns = m.filterColumns(columns, view.ViewName)

		views = append(views, &view)
	}

	return views, nil
}

func (m *MySQLServer) DescribeSchemaIndexes(ctx context.Context, database string) ([]*MySQLIndexDefinition, error) {
	query := `
	SELECT
		index_name,
		index_type AS index_algorithm,
		CASE 
			WHEN non_unique = 0 THEN TRUE
			ELSE FALSE
		END AS is_unique,
		GROUP_CONCAT(column_name ORDER BY seq_in_index ASC) AS columns
	FROM
		information_schema.statistics
	WHERE
		table_schema = ?
	GROUP BY
		index_name, index_type, non_unique
	ORDER BY
		index_name;
	`

	m.logger.Info("execute mysql query", slog.String("query", query))

	rows, err := m.conn.QueryContext(ctx, query, database)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	var indexes []*MySQLIndexDefinition
	for rows.Next() {
		var idx MySQLIndexDefinition
		var columnNames string
		err := rows.Scan(
			&idx.IndexName,
			&idx.Algorithm,
			&idx.IsUnique,
			&columnNames,
		)
		if err != nil {
			return nil, err
		}
		idx.ColumnNames = strings.Split(columnNames, ",")
		idx.DatabaseName = database
		indexes = append(indexes, &idx)
	}

	return indexes, nil
}

func (m *MySQLServer) DescribeSchemaTriggers(ctx context.Context, databaseName string) ([]*MySQLTriggerDefinition, error) {
	query := `
        SELECT
            TRIGGER_SCHEMA,
            TRIGGER_NAME,
            EVENT_MANIPULATION,
            ACTION_TIMING,
            ACTION_ORIENTATION,
            ACTION_STATEMENT
        FROM
            information_schema.triggers
        WHERE
            TRIGGER_SCHEMA = ?;
    `

	m.logger.Info("execute mysql query", slog.String("query", query))

	rows, err := m.conn.QueryContext(ctx, query, databaseName)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	var triggers []*MySQLTriggerDefinition
	for rows.Next() {
		var trigger MySQLTriggerDefinition
		err := rows.Scan(
			&trigger.DatabaseName,
			&trigger.TriggerName,
			&trigger.EventManipulation,
			&trigger.ActionTiming,
			&trigger.ActionOrientation,
			&trigger.ActionStatement,
		)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, &trigger)
	}

	return triggers, nil
}

func (m *MySQLServer) DescribeSchemaFunctions(ctx context.Context, database string) ([]*MySQLFunctionDefinition, error) {
	query := `
	SELECT 
		ROUTINE_SCHEMA,
		ROUTINE_NAME,
		DATA_TYPE,
		ROUTINE_DEFINITION
		-- LANGUAGE
	FROM 
		INFORMATION_SCHEMA.ROUTINES
	WHERE 
		ROUTINE_TYPE = 'FUNCTION'
		AND ROUTINE_SCHEMA = ?
	`

	m.logger.Info("execute mysql query", slog.String("query", query))

	rows, err := m.conn.QueryContext(ctx, query, database)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	var functions []*MySQLFunctionDefinition
	for rows.Next() {
		var funcDef MySQLFunctionDefinition
		err := rows.Scan(
			&funcDef.DatabaseName,
			&funcDef.FunctionName,
			&funcDef.ReturnType,
			&funcDef.Definition,
		)
		if err != nil {
			return nil, err
		}
		functions = append(functions, &funcDef)
	}

	return functions, nil
}

func (m *MySQLServer) DescribeSchemaStoredProcedures(ctx context.Context, database string) ([]*MySQLStoredProceduresDefinition, error) {
	query := `
	SELECT 
		ROUTINE_SCHEMA,
		ROUTINE_NAME,
		ROUTINE_DEFINITION
		-- LANGUAGE
	FROM 
		INFORMATION_SCHEMA.ROUTINES
	WHERE 
		ROUTINE_TYPE = 'PROCEDURE'
		AND ROUTINE_SCHEMA = ?
	`

	m.logger.Info("execute mysql query", slog.String("query", query))

	rows, err := m.conn.QueryContext(ctx, query, database)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	var procedures []*MySQLStoredProceduresDefinition
	for rows.Next() {
		var procDef MySQLStoredProceduresDefinition
		err := rows.Scan(
			&procDef.DatabaseName,
			&procDef.ProcedureName,
			&procDef.Definition,
		)
		if err != nil {
			return nil, err
		}
		procedures = append(procedures, &procDef)
	}

	return procedures, nil
}

func (m *MySQLServer) serverVersion(ctx context.Context) (string, error) {
	var version string
	err := m.conn.QueryRowContext(ctx, "SELECT VERSION() AS version").Scan(&version)
	return version, err
}

func (m *MySQLServer) serverConfigurations(ctx context.Context) ([]*MySQLServerInfo_Configuration, error) {
	rows, err := m.conn.QueryContext(ctx, "SHOW VARIABLES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configurations []*MySQLServerInfo_Configuration
	for rows.Next() {
		var config MySQLServerInfo_Configuration
		if err := rows.Scan(&config.Name, &config.Value); err != nil {
			return nil, err
		}
		configurations = append(configurations, &config)
	}

	return configurations, nil
}

// Helper funcs ..

func (m *MySQLServer) filterForeignKeys(fks []*MySQLForeignKeyDefinition, colName string, dbName string) []*MySQLForeignKeyDefinition {
	var filtered []*MySQLForeignKeyDefinition
	for _, fk := range fks {
		if fk.ColumnName == colName && fk.DatabaseName == dbName {
			filtered = append(filtered, fk)
		}
	}
	return filtered
}

func (m *MySQLServer) filterIndexes(idxs []*MySQLIndexDefinition, colName string, dbName string) []*MySQLIndexDefinition {
	var filtered []*MySQLIndexDefinition
	for _, idx := range idxs {
		if contains(idx.ColumnNames, colName) && idx.DatabaseName == dbName {
			filtered = append(filtered, idx)
		}
	}
	return filtered
}

func (m *MySQLServer) filterColumns(cols []*MySQLColumnDefinition, tableName string) []*MySQLColumnDefinition {
	var filtered []*MySQLColumnDefinition
	for _, col := range cols {
		if col.TableName == tableName {
			filtered = append(filtered, col)
		}
	}
	return filtered
}

func (m *MySQLServer) filterForeignKeysByTable(fks []*MySQLForeignKeyDefinition, tableName string) []*MySQLForeignKeyDefinition {
	var filtered []*MySQLForeignKeyDefinition
	for _, fk := range fks {
		if fk.TableName == tableName {
			filtered = append(filtered, fk)
		}
	}
	return filtered
}

func (m *MySQLServer) filterIndexesByTable(idxs []*MySQLIndexDefinition, tableName string) []*MySQLIndexDefinition {
	var filtered []*MySQLIndexDefinition
	for _, idx := range idxs {
		if idx.TableName == tableName {
			filtered = append(filtered, idx)
		}
	}
	return filtered
}
