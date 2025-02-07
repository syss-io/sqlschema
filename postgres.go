package sqlschema

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

type PostgresServer struct {
	conn   DB
	logger *slog.Logger
}

func Postgres(conn DB, logger *slog.Logger) *PostgresServer {
	if logger == nil {
		logger = slog.Default()
	}

	return &PostgresServer{conn, logger}
}

// DescribePostgresServer fetches Postgres server full version, version number and
// server configurations
//
// Server version - https://www.postgresql.org/docs/current/functions-info.html#FUNCTIONS-INFO-VERSION
// Server configs - https://www.postgresql.org/docs/current/view-pg-settings.html
func (p *PostgresServer) DescribeServer(ctx context.Context) (*PostgresServerInfo, error) {
	query := `
		SELECT 
			version() AS versionFull, 
			regexp_replace(version(), '([0-9]+\.[0-9]+).*', '\1') AS versionShort
	`

	p.logger.Info("execute postgres query", slog.String("query", query))

	var versionFull, versionNumber string
	if err := p.conn.QueryRowContext(ctx, query).Scan(&versionFull, &versionNumber); err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("failed to get version information: %w", err)
	}

	num, err := strconv.Atoi(versionNumber)
	if err != nil {
		slog.WarnContext(ctx, "can't fetch server version. expected number got "+versionNumber)
	}

	configurations, err := p.getServerConfigurations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get server configurations: %w", err)
	}

	return &PostgresServerInfo{
		VersionFull:    versionFull,
		VersionNumber:  int32(num),
		Configurations: configurations,
	}, nil
}

// PostgresAvailableDatabases retrieves the list of available database names.
//
// See https://www.postgresql.org/docs/current/catalog-pg-database.html
func (p *PostgresServer) DescribeAvailableDatabases(ctx context.Context) ([]string, error) {
	query := "SELECT datname FROM pg_database;"

	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query)
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

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating over database rows: %w", rows.Err())
	}

	return databases, nil
}

// AvailableSchemas retrieves the list of available schemas in the given database.
//
// See https://www.postgresql.org/docs/current/infoschema-schemata.html
func (p *PostgresServer) DescribeAvailableSchemas(ctx context.Context, database string) ([]string, error) {
	query := "SELECT schema_name FROM information_schema.schemata WHERE catalog_name = $1;"
	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query, database)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("failed to fetch schemas: %w", err)
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var schemaName string
		if err := rows.Scan(&schemaName); err != nil {
			return nil, fmt.Errorf("failed to scan schema name: %w", err)
		}
		schemas = append(schemas, schemaName)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating over schema rows: %w", rows.Err())
	}

	return schemas, nil
}

func (p *PostgresServer) DescribeSchema(ctx context.Context, schema string) (*PostgresSchemaDefinition, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(1)

	var (
		schemaForeignKeys []*PostgresForeignKeyDefinition
		schemaFunctions   []*PostgresFunctionDefinition
		schemaProcedures  []*PostgresProcedureDefinition
		schemaIndexes     []*PostgresIndexDefinition
		schemaTriggers    []*PostgresTriggerDefinition
		schemaTables      []*PostgresTableDefinition
		schemaViews       []*PostgresViewDefinition
		schemaMatViews    []*PostgresMaterializedViewDefinition
		schemaComment     string
	)

	// Fetch schema foreign keys..
	g.Go(func() error {
		foreignKeys, err := p.getSchemaForeignKeys(gctx, schema)
		if err != nil {
			return fmt.Errorf("can't fetch schema foreign keys: %w", err)
		}

		schemaForeignKeys = foreignKeys
		return nil
	})

	// Fetch schema functions..
	g.Go(func() error {
		functions, err := p.getSchemaFunctions(gctx, schema)
		if err != nil {
			return fmt.Errorf("can't fetch schema functions: %w", err)
		}

		schemaFunctions = functions
		return nil
	})

	// Fetch schema procedures..
	g.Go(func() error {
		procedures, err := p.getSchemaProcedures(gctx, schema)
		if err != nil {
			return fmt.Errorf("can't fetch schema procedures: %w", err)
		}

		schemaProcedures = procedures
		return nil
	})

	// Fetch schema triggers..
	g.Go(func() error {
		triggers, err := p.getSchemaTriggers(gctx, schema)

		if err != nil {
			return fmt.Errorf("can't fetch schema triggers: %w", err)
		}

		schemaTriggers = triggers
		return nil
	})

	// Fetch schema indexes..
	// g.Go(func() error {
	// 	indexes, err := p.GetPostgresSchemaIndexes(ctx, schema)
	// 	if err != nil {
	// 		return fmt.Errorf("can't fetch schema indexes: %w", err)
	// 	}

	// 	schemaIndexes = indexes
	// 	return nil
	// })

	if err := g.Wait(); err != nil {
		return nil, err
	}

	columns, err := p.getSchemaColumns(ctx, schema, schemaForeignKeys, schemaIndexes)
	if err != nil {
		return nil, fmt.Errorf("can't fetch schema columns: %w", err)
	}

	// Fetch schema tables..
	g.Go(func() error {
		tables, err := p.getSchemaTables(ctx, schema, columns, schemaForeignKeys, schemaIndexes, schemaTriggers)
		if err != nil {
			return fmt.Errorf("can't fetch schema tables: %w", err)
		}

		schemaTables = tables
		return nil
	})

	// fetch schema views..
	g.Go(func() error {
		views, err := p.getSchemaViews(ctx, schema, columns)
		if err != nil {
			return fmt.Errorf("can't fetch schema views: %w", err)
		}

		schemaViews = views
		return nil
	})

	// fetch schema materialised views
	g.Go(func() error {
		matViews, err := p.getSchemaMaterializedViews(ctx, schema, columns)
		if err != nil {
			return fmt.Errorf("can't fetch schema materialized views: %w", err)
		}

		schemaMatViews = matViews
		return nil
	})

	g.Go(func() error {
		comment, err := p.getSchameComment(ctx, schema)
		if err != nil {
			return fmt.Errorf("can't fetch schema comment: %w", err)
		}

		schemaComment = comment
		return nil
	})

	return &PostgresSchemaDefinition{
		SchemaName:        schema,
		Tables:            schemaTables,
		Functions:         schemaFunctions,
		Procedures:        schemaProcedures,
		Views:             schemaViews,
		MaterializedViews: schemaMatViews,
		Comment:           &schemaComment,
	}, nil
}

func (p *PostgresServer) DescribeSchemaTables(ctx context.Context, schema string) ([]*PostgresTableDefinition, error) {
	var (
		schemaForeignKeys []*PostgresForeignKeyDefinition
		schemaIndexes     []*PostgresIndexDefinition
		schemaTriggers    []*PostgresTriggerDefinition
	)

	g, gctx := errgroup.WithContext(ctx)

	// Fetch schema foreign keys..
	g.Go(func() error {
		foreignKeys, err := p.getSchemaForeignKeys(gctx, schema)
		if err != nil {
			return fmt.Errorf("can't fetch schema foreign keys: %w", err)
		}

		schemaForeignKeys = foreignKeys
		return nil
	})

	// Fetch schema indexes..
	// g.Go(func() error {
	// 	indexes, err := p.GetPostgresSchemaIndexes(ctx, schema)
	// 	if err != nil {
	// 		return fmt.Errorf("can't fetch schema indexes: %w", err)
	// 	}

	// 	schemaIndexes = indexes
	// 	return nil
	// })

	// Fetch schema triggers..
	g.Go(func() error {
		triggers, err := p.getSchemaTriggers(gctx, schema)

		if err != nil {
			return fmt.Errorf("can't fetch schema triggers: %w", err)
		}

		schemaTriggers = triggers
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	columns, err := p.getSchemaColumns(ctx, schema, schemaForeignKeys, schemaIndexes)
	if err != nil {
		return nil, fmt.Errorf("can't fetch schema columns: %w", err)
	}

	tables, err := p.getSchemaTables(ctx, schema, columns, schemaForeignKeys, schemaIndexes, schemaTriggers)
	if err != nil {
		return nil, fmt.Errorf("can't fetch schema tables: %w", err)

	}

	return tables, nil
}

func (p *PostgresServer) getSchemaTables(ctx context.Context, schema string, columns []*PostgresColumnDefinition, foreignKeys []*PostgresForeignKeyDefinition, indexes []*PostgresIndexDefinition, triggers []*PostgresTriggerDefinition) ([]*PostgresTableDefinition, error) {
	query := `
		SELECT 
			table_name 
		FROM 
			information_schema.tables 
		WHERE 
			table_schema = $1 
			AND table_type = 'BASE TABLE';
	`

	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("querying tables: %w", err)
	}
	defer rows.Close()

	var tables []*PostgresTableDefinition
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}

		table := &PostgresTableDefinition{
			SchemaName:  schema,
			TableName:   tableName,
			Columns:     p.filterColumns(columns, tableName),
			ForeignKeys: p.filterForeignKeys(foreignKeys, tableName, ""),
			Indexes:     p.filterIndexes(indexes, tableName, ""),
			Triggers:    p.filterTriggers(triggers, tableName),
		}

		tables = append(tables, table)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}

	return tables, nil
}

// getSchemaFunctions retrieves the functions in the specified schema from the Postgres database.
//
// This function is using information_schema.routines to filters
// out schema functions and system cataloginformation functions
// to get function return type and function definition.
//
// pg_get_functiondef - Reconstructs the creating command for a function or procedure.
// pg_get_function_result - Reconstructs the RETURNS clause of a function, in the form it would need to appear in within CREATE FUNCTION.
func (p *PostgresServer) getSchemaFunctions(ctx context.Context, schema string) ([]*PostgresFunctionDefinition, error) {
	// Query to fetch function names, return type, and definitions
	query := `
		SELECT 
			r.routine_name AS function_name,
			pg_catalog.pg_get_function_result(p.oid) AS return_type,
			pg_catalog.pg_get_functiondef(p.oid) AS definition
		FROM 
			information_schema.routines r
		JOIN 
			pg_proc p
		ON 
			r.routine_name = p.proname
			AND r.specific_schema = $1
		WHERE 
			r.routine_schema = $1 AND r.routine_type = 'FUNCTION';`

	p.logger.Info("execute postgres query", slog.String("query", query))

	// Execute the query with the provided schema
	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("failed to fetch functions: %w", err)
	}
	defer rows.Close()

	var functions []*PostgresFunctionDefinition
	for rows.Next() {
		var fn PostgresFunctionDefinition
		if err := rows.Scan(&fn.FunctionName, &fn.ReturnType, &fn.Definition); err != nil {
			return nil, fmt.Errorf("failed to scan function row: %w", err)
		}
		fn.SchemaName = schema
		functions = append(functions, &fn)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating over function rows: %w", rows.Err())
	}

	return functions, nil
}

// p.getSchemaForeignKeys fetches foreign keys for a given schema from the PostgreSQL database.
func (p *PostgresServer) getSchemaForeignKeys(ctx context.Context, schema string) ([]*PostgresForeignKeyDefinition, error) {
	// Define the query
	query := `
		SELECT
			tc.table_schema, 
			tc.constraint_name, 
			-- tc.constraint_type,
			tc.table_name, 
			tc.is_deferrable,
			tc.initially_deferred,
			kcu.column_name, 
			-- ccu.table_schema AS foreign_table_schema,
			ccu.table_name AS foreign_table_name,
			ccu.column_name AS foreign_column_name,
			rc.update_rule AS on_update,
			rc.delete_rule AS on_delete
		FROM information_schema.table_constraints AS tc 
		JOIN information_schema.key_column_usage AS kcu
			ON tc.constraint_name = kcu.constraint_name
			AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage AS ccu
			ON ccu.constraint_name = tc.constraint_name
		JOIN information_schema.referential_constraints AS rc
			ON tc.constraint_name = rc.constraint_name
			AND tc.table_schema = rc.constraint_schema
		WHERE tc.table_schema=$1 AND tc.constraint_type='FOREIGN KEY';`

	p.logger.Info("execute postgres query", slog.String("query", query))

	// Execute the query
	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	// Define a slice to store the foreign key definitions
	var foreignKeys []*PostgresForeignKeyDefinition

	// Iterate over the rows and map them to PostgresForeignKeyDefinition
	for rows.Next() {
		var fk PostgresForeignKeyDefinition
		var initiallyDeferred string
		var isDeferrable string

		// Scan row into variables
		err := rows.Scan(
			&fk.SchemaName,
			&fk.ConstraintName,
			// &fk.ConstraintType,
			&fk.TableName,
			&isDeferrable,
			&initiallyDeferred,
			&fk.ColumnName,
			// &fk.ForeignSchemaName,
			&fk.ForeignTableName,
			&fk.ForeignColumnName,
			&fk.OnUpdate,
			&fk.OnDelete,
		)
		if err != nil {
			return nil, err
		}

		// Convert string values to the appropriate types
		fk.Validation = isDeferrable
		fk.IsInitiallyDeferred = initiallyDeferred == "YES"

		// Append to the result slice
		foreignKeys = append(foreignKeys, &fk)
	}

	// Check for any error encountered during iteration
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return foreignKeys, nil
}

// GetViews fetches view definitions for a schema and associates them with columns.
func (p *PostgresServer) getSchemaViews(ctx context.Context, schema string, columns []*PostgresColumnDefinition) ([]*PostgresViewDefinition, error) {
	query := `
		SELECT 
			t.table_name,
			v.is_updatable,
			pv.definition AS view_definition
		FROM 
			information_schema.tables t
		JOIN 
			information_schema.views v
		ON 
			t.table_name = v.table_name
			AND t.table_schema = v.table_schema
		JOIN 
			pg_views pv
		ON 
			t.table_name = pv.viewname
			AND t.table_schema = pv.schemaname
		WHERE 
			t.table_schema = $1
			AND t.table_type = 'VIEW';
	`

	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("querying views: %w", err)
	}
	defer rows.Close()

	var views []*PostgresViewDefinition
	for rows.Next() {
		var tableName, viewDefinition string
		var isUpdatable string

		if err := rows.Scan(&tableName, &isUpdatable, &viewDefinition); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}

		view := &PostgresViewDefinition{
			SchemaName:  schema,
			TableName:   tableName,
			IsUpdatable: isUpdatable == "YES",
			Definition:  viewDefinition,
			Columns:     p.filterColumns(columns, tableName),
		}

		views = append(views, view)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}

	return views, nil
}

// GetMaterializedViews fetches materialized view definitions for a schema and associates them with columns.
func (p *PostgresServer) getSchemaMaterializedViews(ctx context.Context, schema string, columns []*PostgresColumnDefinition) ([]*PostgresMaterializedViewDefinition, error) {
	query := `
		SELECT 
			mv.matviewname AS materialized_view_name,
			mv.definition AS view_definition
		FROM 
			pg_matviews mv
		WHERE 
			mv.schemaname = $1;
	`

	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("querying materialized views: %w", err)
	}
	defer rows.Close()

	var materializedViews []*PostgresMaterializedViewDefinition
	for rows.Next() {
		var viewName, viewDefinition string

		if err := rows.Scan(&viewName, &viewDefinition); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}

		view := &PostgresMaterializedViewDefinition{
			SchemaName: schema,
			TableName:  viewName,
			Definition: viewDefinition,
			Columns:    p.filterColumns(columns, viewName),
		}

		materializedViews = append(materializedViews, view)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}

	return materializedViews, nil
}

// p.getSchemaColumns fetches column definitions for a schema and associates them with foreign keys and indexes.
func (p *PostgresServer) getSchemaColumns(ctx context.Context, schema string, foreignKeys []*PostgresForeignKeyDefinition, indexes []*PostgresIndexDefinition) ([]*PostgresColumnDefinition, error) {
	query := `
		SELECT 
			c.relname AS table_name,
			a.attname AS column_name,
			t.typname AS data_type,
			CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END AS is_nullable,
			pg_get_expr(ad.adbin, ad.adrelid) AS column_default,
			a.attlen AS character_maximum_length
		FROM 
			pg_catalog.pg_attribute a
		JOIN 
			pg_catalog.pg_class c ON a.attrelid = c.oid
		JOIN 
			pg_catalog.pg_type t ON a.atttypid = t.oid
		LEFT JOIN 
			pg_catalog.pg_attrdef ad ON a.attrelid = ad.adrelid AND a.attnum = ad.adnum
		JOIN 
			pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE 
			n.nspname = $1
			AND c.relkind IN ('r', 'v', 'm')
			AND a.attnum > 0
		ORDER BY 
			c.relname, a.attnum;
	`

	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("querying columns: %w", err)
	}
	defer rows.Close()

	var columns []*PostgresColumnDefinition
	for rows.Next() {
		var (
			tableName              string
			columnName             string
			dataType               string
			isNullable             string
			columnDefault          sql.NullString
			characterMaximumLength sql.NullInt64
		)

		err := rows.Scan(&tableName, &columnName, &dataType, &isNullable, &columnDefault, &characterMaximumLength)
		if err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}

		column := PostgresColumnDefinition{
			SchemaName:  schema,
			TableName:   tableName,
			ColumnName:  columnName,
			DataType:    dataType,
			IsNullable:  isNullable == "YES",
			IsPrimary:   p.isPrimary(indexes, tableName, columnName),
			IsUnique:    p.isUnique(indexes, tableName, columnName),
			ForeignKeys: p.filterForeignKeys(foreignKeys, tableName, columnName),
			Indexes:     p.filterIndexes(indexes, tableName, columnName),
		}

		if columnDefault.Valid {
			column.DefaultValue = &columnDefault.String
		}

		if characterMaximumLength.Valid {
			column.CharacterMaximumLength = &characterMaximumLength.Int64
		}

		columns = append(columns, &column)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}

	return columns, nil
}

func (p *PostgresServer) getSchemaProcedures(ctx context.Context, schema string) ([]*PostgresProcedureDefinition, error) {
<<<<<<< HEAD
	sqlQuery := `
=======
	query := `
>>>>>>> ccbc083 (add logger)
		SELECT 
			r.routine_name AS procedure_name,
			r.routine_definition AS definition
		FROM 
			information_schema.routines r
		WHERE 
			r.routine_schema = $1 
			AND r.routine_type = 'PROCEDURE';`

	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("error querying database: %v", err)
	}
	defer rows.Close()

	var procedures []*PostgresProcedureDefinition
	for rows.Next() {
		var procedure PostgresProcedureDefinition
		if err := rows.Scan(&procedure.ProcedureName, &procedure.Definition); err != nil {
			return nil, fmt.Errorf("error scanning row: %v", err)
		}
		procedure.SchemaName = schema
		procedures = append(procedures, &procedure)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating over rows: %v", err)
	}

	return procedures, nil
}

// etSchemaIndexes fetches index definitions from the PostgreSQL database.
func (p *PostgresServer) getSchemaIndexes(ctx context.Context, schema string) ([]*PostgresIndexDefinition, error) {
	// SQL query to fetch index details
	query := fmt.Sprintf(`
		SELECT 
			ix.relname AS index_name,
			UPPER(am.amname) AS index_algorithm,
			i.indisunique AS is_unique,
			pg_get_indexdef(i.indexrelid) AS index_definition,
			array_to_string(array_agg(a.attname ORDER BY a.attnum), ', ') AS index_columns,
			t.relname AS table_name,
			-- Extract column names from index definition
			REPLACE(
				REGEXP_REPLACE(
					REGEXP_REPLACE(
						REGEXP_REPLACE(pg_get_indexdef(i.indexrelid), ' WHERE .+', ''), 
						' INCLUDE .+', ''
					), 
					' WITH .+', ''
				), 
				'.*\\((.*)\\)', '\\1'
			) AS column_names,
	
			-- Extract condition from index definition
			CASE 
				WHEN POSITION(' WHERE ' IN pg_get_indexdef(i.indexrelid)) > 0 THEN 
					REGEXP_REPLACE(pg_get_indexdef(i.indexrelid), '.+WHERE ', '') 
				WHEN POSITION(' WITH ' IN pg_get_indexdef(i.indexrelid)) > 0 THEN 
					REGEXP_REPLACE(pg_get_indexdef(i.indexrelid), '.+WITH ', '') 
				ELSE ''
			END AS condition,
	
			-- Extract included columns from index definition
			CASE 
				WHEN POSITION(' INCLUDE ' IN pg_get_indexdef(i.indexrelid)) > 0 THEN 
					REGEXP_REPLACE(pg_get_indexdef(i.indexrelid), '.+INCLUDE ', '') 
				WHEN POSITION(' WITH ' IN pg_get_indexdef(i.indexrelid)) > 0 THEN 
					REGEXP_REPLACE(pg_get_indexdef(i.indexrelid), '.+WITH ', '') 
				ELSE ''
			END AS include,
	
			-- Get the index comment
			pg_catalog.obj_description(i.indexrelid, 'pg_class') AS comment,
	
			-- Get the constraint type
			CASE 
				WHEN i.indisprimary THEN 'PRIMARY KEY'
				WHEN i.indisunique THEN 'UNIQUE'
				ELSE 'INDEX'
			END AS constraint_type
		FROM 
			pg_index i
		JOIN 
			pg_class t ON t.oid = i.indrelid
		JOIN 
			pg_class ix ON ix.oid = i.indexrelid
		JOIN
			pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(i.indkey)
		JOIN 
			pg_namespace n ON t.relnamespace = n.oid
		JOIN 
			pg_am AS am ON ix.relam = am.oid
		LEFT JOIN
			pg_constraint c ON c.conindid = i.indexrelid
		WHERE n.nspname = %s
		GROUP BY ix.relname, am.amname, i.indisunique, i.indexrelid, t.relname, constraint_type;`, schema)

	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, err
	}
	defer rows.Close()

	var indexes []*PostgresIndexDefinition
	for rows.Next() {
		var index PostgresIndexDefinition
		var columnNames string
		var indexColumnNames string
		var condition string
		var include string

		err := rows.Scan(
			&index.IndexName,
			&index.Algorithm,
			&index.IsUnique,
			&index.Definition,
			&indexColumnNames,
			&index.TableName,
			&columnNames,
			&condition,
			&include,
			&index.Comment,
			&index.ConstraintType,
		)
		if err != nil {
			return nil, err
		}

		index.SchemaName = schema
		index.ColumnNames = strings.Split(columnNames, ",")
		index.Condition = condition
		index.Include = include
		index.IsPartial = len(strings.Split(columnNames, ",")) > 1

		// Check for primary key constraint
		if index.ConstraintType == "PRIMARY KEY" {
			index.IsPrimary = true
		}

		indexes = append(indexes, &index)
	}

	return indexes, nil
}

// p.getSchemaTriggers fetches trigger definitions for the specified schema.
func (p *PostgresServer) getSchemaTriggers(ctx context.Context, schema string) ([]*PostgresTriggerDefinition, error) {
	query := `
		SELECT 
			t.trigger_name AS name,
			t.trigger_schema AS schema,
			t.event_object_table AS table,
			t.action_timing AS when,
			t.event_manipulation AS events,
			t.action_statement AS statement
		FROM 
			information_schema.triggers t
		WHERE 
			t.trigger_schema = $1;
	`

	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("querying triggers: %w", err)
	}
	defer rows.Close()

	var triggers []*PostgresTriggerDefinition
	for rows.Next() {
		var trigger PostgresTriggerDefinition
		err := rows.Scan(&trigger.TriggerName, &trigger.SchemaName, &trigger.TableName, &trigger.When, &trigger.Event, &trigger.Statement)
		if err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		triggers = append(triggers, &trigger)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating rows: %w", err)
	}

	return triggers, nil
}

// getServerConfigurations retrieves PostgreSQL server configurations
func (p *PostgresServer) getServerConfigurations(ctx context.Context) ([]*PostgresServerInfo_Configuration, error) {
	query := `
		SELECT 
			name, 
			category, 
			current_setting(name) AS value, 
			unit, 
			short_desc AS description 
		FROM 
			pg_settings;
	`

	p.logger.Info("execute postgres query", slog.String("query", query))

	rows, err := p.conn.QueryContext(ctx, query)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return nil, fmt.Errorf("failed to fetch configurations: %w", err)
	}
	defer rows.Close()

	var configurations []*PostgresServerInfo_Configuration
	for rows.Next() {
		var config PostgresServerInfo_Configuration
		err := rows.Scan(&config.Name, &config.Category, &config.Value, &config.Unit, &config.Description)
		if err != nil {
			return nil, fmt.Errorf("failed to parse configuration row: %w", err)
		}
		configurations = append(configurations, &config)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating over rows: %w", rows.Err())
	}

	return configurations, nil
}

func (p *PostgresServer) getSchameComment(ctx context.Context, schema string) (string, error) {
	query := `
		SELECT 
			d.description AS comment
		FROM 
			pg_namespace n
		LEFT JOIN 
			pg_description d ON d.objoid = n.oid
		WHERE 
			n.nspname = $1;
	`

	p.logger.Info("execute postgres query", slog.String("query", query))

	// Execute the query
	rows, err := p.conn.QueryContext(ctx, query, schema)
	if err != nil {
		slog.Error("query execution error", slog.String("error", err.Error()))

		return "", err
	}
	defer rows.Close()

	var comment string
	for rows.Next() {
		err = rows.Scan(&comment)
		if err != nil {
			return "", fmt.Errorf("failed to parse configuration row: %w", err)
		}
	}

	// Check for errors after iterating through rows
	if rows.Err() != nil {
		log.Fatalf("Row iteration error: %v\n", rows.Err())
	}

	return comment, nil
}

// Helper functions:

func (p *PostgresServer) isPrimary(indexes []*PostgresIndexDefinition, tableName, columnName string) bool {
	for _, index := range indexes {
		if index.TableName == tableName && contains(index.ColumnNames, columnName) && index.ConstraintType == "PRIMARY KEY" {
			return true
		}
	}
	return false
}

func (p *PostgresServer) isUnique(indexes []*PostgresIndexDefinition, tableName, columnName string) bool {
	for _, index := range indexes {
		if index.TableName == tableName && contains(index.ColumnNames, columnName) && index.ConstraintType == "UNIQUE" {
			return true
		}
	}
	return false
}

func (p *PostgresServer) filterForeignKeys(foreignKeys []*PostgresForeignKeyDefinition, tableName, columnName string) []*PostgresForeignKeyDefinition {
	var result []*PostgresForeignKeyDefinition
	for _, fk := range foreignKeys {
		if fk.TableName == tableName && (fk.ColumnName == columnName || columnName == "") {
			result = append(result, fk)
		}
	}
	return result
}

func (p *PostgresServer) filterColumns(columns []*PostgresColumnDefinition, tableName string) []*PostgresColumnDefinition {
	var result []*PostgresColumnDefinition
	for _, column := range columns {
		if column.TableName == tableName {
			result = append(result, column)
		}
	}
	return result
}

func (p *PostgresServer) filterIndexes(indexes []*PostgresIndexDefinition, tableName string, columnName string) []*PostgresIndexDefinition {
	var result []*PostgresIndexDefinition
	for _, index := range indexes {
		if index.TableName == tableName && (contains(index.ColumnNames, columnName) || columnName == "") {
			result = append(result, index)
		}
	}
	return result
}

func (p *PostgresServer) filterTriggers(triggers []*PostgresTriggerDefinition, tableName string) []*PostgresTriggerDefinition {
	var result []*PostgresTriggerDefinition
	for _, trigger := range triggers {
		if trigger.TableName == tableName {
			result = append(result, trigger)
		}
	}
	return result
}
