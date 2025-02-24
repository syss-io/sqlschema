package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/syss-io/sqlschema"
)

func main() {
	db, err := sqlx.Open("postgres", "user=postgres dbname=postgres  password=password port=54321 sslmode=disable")

	if err != nil {
		panic(err)
	}

	slog.Info("successfully connected!")

	dbx := sqlschema.NewSqlxDBWrapper(db)

	server := sqlschema.NewPostgreSQLServer(dbx, sqlschema.WithLogger(slog.Default(), "PostgreSQL"))

	ctx := context.Background()

	//serverInfo, err := server.DescribeServer(ctx)
	//if err != nil {
	//	panic(err)
	//}

	//fmt.Printf("PostgreSQL server version: %s", serverInfo.GetVersionFull())
	//for _, i := range serverInfo.GetConfigurations() {
	//	fmt.Printf("   %s, %#v, %#v\n", i.GetName(), i.GetUnit(), i.GetValue())
	//}

	dbs, err := server.DescribeAvailableDatabases(ctx)
	if err != nil {
		panic(err)
	}
	for _, db := range dbs {
		fmt.Printf("db: %s\n", db)

		schemas, err := server.DescribeAvailableSchemas(ctx, db)
		if err != nil {
			panic(err)
		}

		fmt.Printf("schemas for %s\n\n", db)

		for _, schema := range schemas {
			fmt.Printf("\tschema: %s\n", schema)
		}
	}

}
