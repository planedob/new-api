package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func readImage2AsyncSchemaSQL(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "bin", name))
	require.NoError(t, err)
	return string(data)
}

func TestImage2AsyncSQLiteMigrationIsIdempotentAndRollbackable(t *testing.T) {
	dsn := "file:image2_async_schema_" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	schema := readImage2AsyncSchemaSQL(t, "migration_image2_async_job_sqlite.sql")
	require.NoError(t, db.Exec(schema).Error)
	require.NoError(t, db.Exec(schema).Error, "second application must be a no-op")
	require.True(t, db.Migrator().HasTable("image2_async_jobs"))

	var count int64
	require.NoError(t, db.Raw("SELECT count(*) FROM pragma_index_list('image2_async_jobs') WHERE name = ?", "idx_image2_async_jobs_scope_key").Scan(&count).Error)
	require.Equal(t, int64(1), count)
	rollback := readImage2AsyncSchemaSQL(t, "rollback_image2_async_job_sqlite.sql")
	require.NoError(t, db.Exec(rollback).Error)
	require.False(t, db.Migrator().HasTable("image2_async_jobs"))
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		_ = sqlDB.Close()
	}
}

func TestImage2AsyncDialectMigrationsDeclareSameFailClosedContract(t *testing.T) {
	for _, name := range []string{
		"migration_image2_async_job_sqlite.sql",
		"migration_image2_async_job_mysql.sql",
		"migration_image2_async_job_postgres.sql",
	} {
		t.Run(name, func(t *testing.T) {
			schema := readImage2AsyncSchemaSQL(t, name)
			for _, fragment := range []string{
				"CREATE TABLE IF NOT EXISTS",
				"image2_async_jobs",
				"scope_key",
				"idempotency_key",
				"response_body",
				"error_code",
				"expires_at",
				"lease_owner",
				"lease_until",
				"idx_image2_async_jobs_scope_key",
			} {
				require.Contains(t, strings.ToLower(schema), strings.ToLower(fragment))
			}
			if name == "migration_image2_async_job_mysql.sql" {
				require.Contains(t, strings.ToLower(schema), "response_body` longtext")
			}
			rollback := readImage2AsyncSchemaSQL(t, strings.Replace(name, "migration_", "rollback_", 1))
			require.Contains(t, strings.ToLower(rollback), "drop")
		})
	}
}
